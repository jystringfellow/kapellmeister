package loop

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func checkReplyOnlyEdits(t *testing.T, custom, twice bool) {
	t.Helper()
	var e *Engine
	var f *fakeStepper
	var records *bytes.Buffer
	name, next, chair := "plan", "implement", "p:x"
	if custom {
		e, f = otherScoreEngine(t)
		records = &bytes.Buffer{}
		e.Records = records
		e.Score.Steps = append([]ScoreStep{{Name: "outline", Kind: KindProduce, Part: "drafter", Writes: "reply", Exits: []Exit{{"done", "draft"}}}}, e.Score.Steps...)
		e.Score.Steps[1].Reads = []string{"outline"}
		name, next, chair = "outline", "draft", "p:drafter"
	} else {
		e, f, records = newEngine(t, passing)
	}
	f.write("main.go", "base\n")
	snapshotGit(t, e, "add", "main.go")
	snapshotGit(t, e, "-c", "user.name=t", "-c", "user.email=t@example.invalid", "commit", "-qm", "base file")
	var progress bytes.Buffer
	e.Progress = &progress
	calls := 0
	f.actions[name] = func(s Step) {
		calls++
		if s.Retry != calls-1 || strings.Contains(s.Prompt, "your previous attempt at this step changed the worktree") != (calls == 2) {
			t.Errorf("attempt %d: retry=%d prompt=%s", calls, s.Retry, s.Prompt)
		}
		if calls == 2 {
			data, err := os.ReadFile(filepath.Join(f.dir, "main.go"))
			if err != nil || string(data) != "base\n" {
				t.Errorf("before retry: %q %v", data, err)
			}
		}
		if calls == 1 || twice {
			f.write("main.go", "unwanted\n")
		}
		f.write(s.ReplyPath, "reply from attempt "+string(rune('0'+calls)))
	}
	f.actions[next] = func(s Step) {
		data, err := os.ReadFile(filepath.Join(f.dir, e.rel(name+".md")))
		if err != nil || string(data) != "reply from attempt 2" || !strings.Contains(s.Prompt, e.rel(name+".md")) {
			t.Errorf("handoff: %q %v prompt=%s", data, err, s.Prompt)
		}
		if custom {
			f.write("main.go", "draft edit\n")
		}
		f.write(s.ReplyPath, "done")
	}
	res := run(t, e)
	want := Approved
	if twice {
		want = Failed
	}
	if res.Outcome != want || calls != 2 {
		t.Errorf("result=%+v calls=%d progress=%s", res, calls, &progress)
	}
	if twice && (!strings.Contains(res.Reason, name+" chair "+chair) || !strings.Contains(res.Reason, "after a second change") || len(testerSteps(f, next)) != 0) {
		t.Errorf("failure=%+v steps=%v", res, f.names())
	}
	expected := "base\n"
	if custom && !twice {
		expected = "draft edit\n"
	}
	data, err := os.ReadFile(filepath.Join(f.dir, "main.go"))
	if err != nil || string(data) != expected {
		t.Errorf("work=%q want=%q err=%v", data, expected, err)
	}
	restores := recordsOf(t, records, "restore")
	changed := false
	for _, r := range recordsOf(t, records, name) {
		changed = changed || r.TreeChanged
	}
	if len(restores) == 0 || !restores[0].Restored || !changed || !strings.Contains(progress.String(), "restored the work from the snapshot; running "+name+" again, told not to change the worktree") {
		t.Errorf("records=%s progress=%s", records, &progress)
	}
	if custom && !twice && len(testerSteps(f, "draft")) != 1 {
		t.Errorf("draft calls=%d", len(testerSteps(f, "draft")))
	}
}

func TestAPlannerThatEditsASourceFileIsRestoredRetriedWithTheReadOnlyPromptAndTheRunContinues(t *testing.T) {
	checkReplyOnlyEdits(t, false, false)
}
func TestAPlannerThatEditsTheWorktreeOnBothAttemptsFailsTheRunWithTheWorkRestoredNamingThePlanStepAndItsChair(t *testing.T) {
	checkReplyOnlyEdits(t, false, true)
}
func TestAForegroundReplyOnlyProduceStepInAnotherScoreIsRestoredRetriedAndContinues(t *testing.T) {
	checkReplyOnlyEdits(t, true, false)
}
func TestAForegroundReplyOnlyProduceStepInAnotherScoreThatEditsTwiceFailsWithTheWorkRestored(t *testing.T) {
	checkReplyOnlyEdits(t, true, true)
}

func TestABackgroundReplyOnlyStepThatOverlapsTheImplementersEditsIsNeitherRestoredNorFailed(t *testing.T) {
	e, f := testerEngine(t)
	records := &bytes.Buffer{}
	e.Records = records
	var progress bytes.Buffer
	e.Progress = &progress
	started, edited := make(chan struct{}), make(chan struct{})
	f.actions["test-plan"] = func(s Step) {
		close(started)
		select {
		case <-edited:
		case <-time.After(2 * time.Second):
			t.Error("implement did not edit")
		}
		f.write(s.ReplyPath, "test plan")
	}
	f.actions["implement"] = func(s Step) {
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Error("test-plan did not start")
		}
		f.write("main.go", "implementation")
		close(edited)
		f.write(s.ReplyPath, "done")
	}
	res := run(t, e)
	data, err := os.ReadFile(filepath.Join(f.dir, "main.go"))
	if res.Outcome != Approved || len(testerSteps(f, "test-plan")) != 1 || err != nil || string(data) != "implementation" || len(recordsOf(t, records, "restore")) != 0 || strings.Contains(records.String(), `"tree_changed":true`) || strings.Contains(progress.String(), "restored the work") {
		t.Fatalf("result=%+v file=%q err=%v records=%s progress=%s", res, data, err, records, &progress)
	}
}

func TestAPlannerThatLeavesTheWorktreeAloneProducesExactlyTheRecordsAndProgressItDidBefore(t *testing.T) {
	e, f, records := newEngine(t, passing)
	var progress bytes.Buffer
	e.Progress = &progress
	res := run(t, e)
	if res.Outcome != Approved || len(testerSteps(f, "plan")) != 1 || len(res.Restores) != 0 || len(recordsOf(t, records, "restore")) != 0 || strings.Contains(records.String(), `"tree_changed":true`) || strings.Contains(progress.String(), "snapshot") || strings.Contains(progress.String(), "retry") {
		t.Fatalf("%+v records=%s progress=%s", res, records, &progress)
	}
	equal(t, f.names(), []string{"plan", "implement[1]", "review[1]", "classify-review[1]", "gate[1]", "classify-gate[1]"})
}
