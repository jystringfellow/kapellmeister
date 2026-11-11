package loop

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"
)

func TestTheEngineReportsEachStepAsPlayingWhileItRunsAndAsFinishedWhenItEnds(t *testing.T) {
	eng, fake, _ := newEngine(t, passing)
	twoReviewers(eng)
	var mu sync.Mutex
	var active []ActiveStep
	began, ended := map[string]int{}, map[string]int{}
	snapshots := 0
	eng.Playing = func(round int, steps []ActiveStep) {
		mu.Lock()
		defer mu.Unlock()
		snapshots++
		for _, s := range steps {
			if s.Started.IsZero() {
				t.Error("step has no start")
			}
			found := false
			for _, old := range active {
				if old == s {
					found = true
				}
			}
			if !found {
				began[s.Step]++
			}
		}
		for _, old := range active {
			found := false
			for _, s := range steps {
				if old == s {
					found = true
				}
			}
			if !found {
				ended[old.Step]++
			}
		}
		active = append([]ActiveStep{}, steps...)
	}
	check := func(name, chair string) {
		mu.Lock()
		defer mu.Unlock()
		for _, s := range active {
			if s.Step == name && (chair == "" || s.ChairName == chair) {
				return
			}
		}
		t.Errorf("%s %s absent from playing snapshot: %+v", name, chair, active)
	}
	for _, name := range []string{"plan", "implement", "review", "gate", "classify-review", "classify-gate"} {
		fake.actions[name] = func(s Step) { check(s.Name, s.ChairName); fake.write(s.ReplyPath, "APPROVE") }
	}
	var reviewers sync.WaitGroup
	reviewers.Add(2)
	both := make(chan struct{})
	go func() { reviewers.Wait(); close(both) }()
	fake.actions["review"] = func(s Step) {
		check(s.Name, s.ChairName)
		reviewers.Done()
		select {
		case <-both:
		case <-time.After(2 * time.Second):
			t.Error("reviewers did not overlap")
		}
		fake.write(s.ReplyPath, "APPROVE")
	}
	verifyRound := 0
	eng.Verify = func(context.Context, string) (VerifyResult, error) {
		check(fmt.Sprintf("verify-%d", verifyRound), "")
		verifyRound++
		return VerifyResult{}, nil
	}
	if res := run(t, eng); res.Outcome != Approved {
		t.Fatal(res)
	}
	if snapshots == 0 || len(active) != 0 || began["review"] != 2 || began["verify-0"] != 1 || !reflect.DeepEqual(began, ended) {
		t.Fatalf("snapshots=%d active=%v began=%v ended=%v", snapshots, active, began, ended)
	}
}

func TestTheTelemetryStreamIsUnchangedByStepStartNotifications(t *testing.T) {
	var streams [][]string
	for _, notify := range []bool{false, true} {
		eng, _, records := newEngine(t, passing)
		if notify {
			eng.Playing = func(int, []ActiveStep) {}
		}
		run(t, eng)
		var steps []string
		scanner := bufio.NewScanner(records)
		for scanner.Scan() {
			var r Record
			if err := json.Unmarshal(scanner.Bytes(), &r); err != nil {
				t.Fatal(err)
			}
			steps = append(steps, label(r.Step, r.Round))
		}
		streams = append(streams, steps)
	}
	if !reflect.DeepEqual(streams[0], streams[1]) {
		t.Fatalf("streams differ: %v", streams)
	}
}

func TestPlayingKeepsTheCurrentRoundWhenTheTestPlanFinishesDuringImplementation(t *testing.T) {
	eng, fake := testerEngine(t)
	var previous int
	sawTestPlan := false
	implementationStarted := make(chan struct{})
	testPlanFinished := make(chan struct{})
	var finished sync.Once
	eng.Playing = func(round int, active []ActiveStep) {
		if round < previous {
			t.Errorf("round regressed from %d to %d", previous, round)
		}
		previous = round
		implement, testPlan := false, false
		for _, step := range active {
			implement = implement || step.Step == "implement"
			testPlan = testPlan || step.Step == "test-plan"
		}
		sawTestPlan = sawTestPlan || testPlan
		if sawTestPlan && implement && !testPlan {
			finished.Do(func() { close(testPlanFinished) })
		}
	}
	fake.actions["test-plan"] = func(s Step) {
		select {
		case <-implementationStarted:
		case <-time.After(2 * time.Second):
			t.Error("implementation never started")
		}
		fake.write(s.ReplyPath, "plan")
	}
	fake.actions["implement"] = func(s Step) {
		close(implementationStarted)
		select {
		case <-testPlanFinished:
		case <-time.After(2 * time.Second):
			t.Error("test plan never finished")
		}
		fake.write(s.ReplyPath, "implemented")
	}
	if res := run(t, eng); res.Outcome != Approved {
		t.Fatal(res)
	}
}
