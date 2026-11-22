package loop

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestParallelReplyChairsEachPlayAndHandTheirRepliesOn(t *testing.T) {
	score, err := decodeScore(`
[[steps]]
name = "research"
kind = "produce"
part = "scout"
writes = "reply"
chairs = "all"
next = { done = "plan" }
[[steps]]
name = "plan"
kind = "produce"
part = "writer"
reads = ["research"]
writes = "reply"
next = { done = "approved" }
`, "score.toml", "example")
	if err != nil {
		t.Fatal(err)
	}
	e, f := otherScoreEngine(t)
	e.Score = score
	for i := range e.Score.Parts {
		if e.Score.Parts[i].Name == "writer" {
			e.Score.Parts[i].Prompt = map[string]string{"plan": "Inputs: {inputs}"}
		}
	}
	e.Config.Chairs.Parts = map[string][]Chair{"scout": {{Profile: "one", Focus: "code"}, {Profile: "two", Focus: "tests"}, {Profile: "three", Focus: "docs"}}, "writer": {scoreChair("writer")}}
	var started atomic.Int32
	ready := make(chan struct{})
	f.actions["research"] = func(s Step) {
		if started.Add(1) == 3 {
			close(ready)
		}
		select {
		case <-ready:
		case <-time.After(2 * time.Second):
			t.Error("chairs did not start in parallel")
		}
		f.write(s.ReplyPath, "notes")
	}
	f.actions["plan"] = func(s Step) { f.write(s.ReplyPath, "plan") }
	res := run(t, e)
	if res.Outcome != Approved {
		t.Fatalf("result = %+v", res)
	}
	count := 0
	var prompt string
	for _, step := range f.steps {
		if step.Name == "research" {
			count++
			for _, focus := range []string{"code", "tests", "docs"} {
				block := FocusBlock(focus)
				if strings.Contains(step.Prompt, block) != (focus == step.Chair.Focus) {
					t.Errorf("wrong focus in %s", step.Prompt)
				}
			}
		}
		if step.Name == "plan" {
			prompt = step.Prompt
		}
	}
	if count != 3 {
		t.Fatalf("research chairs = %d", count)
	}
	last := -1
	for _, name := range []string{"research-1.md", "research-2.md", "research-3.md"} {
		index := strings.Index(prompt, name)
		if index <= last {
			t.Errorf("replies out of order: %s", prompt)
		}
		last = index
		if !strings.Contains(prompt, name) {
			t.Errorf("missing %s in %s", name, prompt)
		}
	}
	if !strings.Contains(prompt, "Inputs: ") || !strings.Contains(prompt, "Read ") {
		t.Fatal(prompt)
	}
}

func TestParallelRepliesContinueAfterOneChairFailsAndFailWhenAllFail(t *testing.T) {
	for _, all := range []bool{false, true} {
		t.Run(map[bool]string{false: "one", true: "all"}[all], func(t *testing.T) {
			score, err := decodeScore(`
[[steps]]
name = "research"
kind = "produce"
part = "scout"
writes = "reply"
chairs = "all"
next = { done = "plan" }
[[steps]]
name = "plan"
kind = "produce"
part = "writer"
reads = ["research"]
writes = "reply"
next = { done = "approved" }
`, "score.toml", "example")
			if err != nil {
				t.Fatal(err)
			}
			e, f := otherScoreEngine(t)
			var records bytes.Buffer
			e.Records = &records
			e.Score = score
			e.Config.Chairs.Parts = map[string][]Chair{"scout": {{Profile: "one"}, {Profile: "two"}, {Profile: "three"}}, "writer": {scoreChair("writer")}}
			f.errFor = func(s Step) error {
				if s.Name == "research" && (all || s.Chair.Profile == "two") {
					return errors.New("broken")
				}
				return nil
			}
			res := run(t, e)
			failed := recordsOf(t, &records, "research")
			found := false
			for _, record := range failed {
				if record.ChairName == "scout-2" && strings.Contains(record.Error, "broken") {
					found = true
				}
			}
			if !found {
				t.Fatalf("missing failed chair record: %+v", failed)
			}
			if all {
				if len(testerSteps(f, "plan")) != 0 {
					t.Fatal("plan played after every chair failed")
				}
				if res.Outcome != "failed" || !strings.Contains(res.Reason, "every scout chair failed") {
					t.Fatalf("result = %+v", res)
				}
				return
			}
			if res.Outcome != Approved {
				t.Fatalf("result = %+v", res)
			}
			for _, s := range f.steps {
				if s.Name == "plan" {
					if !strings.Contains(s.Prompt, "scout-2 (") || strings.Contains(s.Prompt, "research-2.md") || !strings.Contains(s.Prompt, "research-1.md") || !strings.Contains(s.Prompt, "research-3.md") {
						t.Fatalf("prompt = %s", s.Prompt)
					}
					return
				}
			}
			t.Fatal("plan did not play")
		})
	}
}

func TestParallelReplyChairsAreAllRetriedAfterAWorktreeChange(t *testing.T) {
	for _, twice := range []bool{false, true} {
		t.Run(map[bool]string{false: "once", true: "twice"}[twice], func(t *testing.T) {
			e, f := otherScoreEngine(t)
			e.Score.Steps = []ScoreStep{{Name: "research", Kind: KindProduce, Part: "scout", Writes: "reply", Chairs: ChairsAll, Exits: []Exit{{"done", "approved"}}}}
			e.Score.Parts = []Part{{Name: "scout"}}
			e.Config.Chairs.Parts = map[string][]Chair{"scout": {{Profile: "one"}, {Profile: "two"}, {Profile: "three"}}}
			f.write("main.go", "base\n")
			snapshotGit(t, e, "add", "main.go")
			snapshotGit(t, e, "-c", "user.name=t", "-c", "user.email=t@example.invalid", "commit", "-qm", "base")
			f.actions["research"] = func(s Step) {
				if s.Retry == 1 && !strings.Contains(s.Prompt, "your previous attempt at this step changed the worktree") {
					t.Error("missing retry warning")
				}
				if s.Chair.Profile == "one" && (s.Retry == 0 || twice) {
					f.write("main.go", "changed\n")
				}
				f.write(s.ReplyPath, "notes")
			}
			res := run(t, e)
			want := Approved
			if twice {
				want = Failed
			}
			if res.Outcome != want || len(testerSteps(f, "research")) != 6 {
				t.Fatalf("result = %+v, steps = %v", res, f.names())
			}
			if twice && !strings.Contains(res.Reason, "after a second change") {
				t.Fatal(res.Reason)
			}
			data, err := os.ReadFile(filepath.Join(f.dir, "main.go"))
			if err != nil || string(data) != "base\n" {
				t.Fatalf("worktree = %q, %v", data, err)
			}
		})
	}
}

func TestParallelReplyNamesFollowTheRoundAndChairCount(t *testing.T) {
	for _, tc := range []struct {
		name         string
		round, count int
		want         []string
	}{
		{"before one", 0, 1, []string{"research.md"}},
		{"before several", 0, 3, []string{"research-1.md", "research-2.md", "research-3.md"}},
		{"round one", 1, 1, []string{"research-1.md"}},
		{"round several", 1, 3, []string{"research-1-1.md", "research-1-2.md", "research-1-3.md"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, f := otherScoreEngine(t)
			spec := ScoreStep{Name: "research", Kind: KindProduce, Part: "scout", Writes: "reply", Chairs: ChairsAll, Exits: []Exit{{"done", "approved"}}}
			e.Score.Parts = []Part{{Name: "scout"}, {Name: "writer"}}
			e.Score.Steps = []ScoreStep{spec}
			e.Config.Chairs.Parts = map[string][]Chair{"scout": make([]Chair, tc.count), "writer": {scoreChair("writer")}}
			for i := range e.Config.Chairs.Parts["scout"] {
				e.Config.Chairs.Parts["scout"][i] = scoreChair("scout")
			}
			if tc.round > 0 {
				e.Score.Steps = append([]ScoreStep{{Name: "change", Kind: KindProduce, Part: "writer", Exits: []Exit{{"done", "research"}}}}, e.Score.Steps...)
			}
			if result := run(t, e); result.Outcome != Approved {
				t.Fatalf("result = %+v", result)
			}
			seen := map[string]bool{}
			for _, step := range f.steps {
				if step.Name == "research" {
					seen[filepath.Base(step.ReplyPath)] = true
				}
			}
			for _, want := range tc.want {
				if !seen[want] {
					t.Errorf("missing %s in %v", want, seen)
				}
			}

		})
	}
}

func TestParallelReplyChairsAreReseatedBeforeTheSnapshotAndFirstStepsAreNot(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			score, err := decodeScore(`
[[steps]]
name = "first"
kind = "produce"
part = "scout"
writes = "reply"
next = { done = "research" }
[[steps]]
name = "research"
kind = "produce"
part = "scout"
writes = "reply"
chairs = "all"
next = { done = "approved" }
`, "score.toml", "example")
			if err != nil {
				t.Fatal(err)
			}
			e, f := otherScoreEngine(t)
			var records bytes.Buffer
			e.Records = &records
			e.Score = score
			e.Config.Chairs.Parts = map[string][]Chair{"scout": {{Address: "winds.one", Profile: "one"}, {Address: "winds.two", Profile: "two"}, {Address: "winds.three", Profile: "three"}}}
			r := &fakeReseater{}
			r.play = func(chair Chair) (Reseated, error) {
				if len(testerSteps(f, "first")) != 1 || len(testerSteps(f, "research")) != 0 {
					t.Error("reseating happened outside the parallel step")
				}
				if fail && chair.Address == "winds.two" {
					return Reseated{}, errors.New("briefing failed")
				}
				if err := os.WriteFile(filepath.Join(f.dir, "reseated.txt"), []byte(chair.Address), 0600); err != nil {
					return Reseated{}, err
				}
				return Reseated{Old: "old-" + chair.Address, New: "new-" + chair.Address}, nil
			}
			e.Reseater = r
			f.actions["research"] = func(s Step) {
				if !slices.Equal(r.calls, []string{"winds.one", "winds.two", "winds.three"}) {
					t.Errorf("reseats = %v", r.calls)
				}
				f.write(s.ReplyPath, "notes")
			}
			result := run(t, e)
			expected := []string{"winds.one", "winds.two", "winds.three"}
			if fail {
				expected = expected[:2]
			}
			if !slices.Equal(r.calls, expected) {
				t.Fatalf("reseats = %v", r.calls)
			}
			got := recordsOf(t, &records, "reseat")
			wantCount := len(expected)
			if fail {
				wantCount--
			}
			if len(got) != wantCount {
				t.Fatalf("records = %+v", got)
			}
			for i, rec := range got {
				if rec.Chair == nil || rec.Chair.Address != expected[i] || rec.Part != "scout" || rec.TerminalID != "new-"+expected[i] {
					t.Fatalf("record = %+v", rec)
				}
			}
			if fail {
				if result.Outcome != Failed || !strings.Contains(result.Reason, "winds.two") || !strings.Contains(result.Reason, "briefing failed") || len(testerSteps(f, "research")) != 0 {
					t.Fatalf("result = %+v", result)
				}
				return
			}
			if result.Outcome != Approved || len(result.Restores) != 0 {
				t.Fatalf("result = %+v", result)
			}
			data, err := os.ReadFile(filepath.Join(f.dir, "reseated.txt"))
			if err != nil || string(data) != "winds.three" {
				t.Fatalf("snapshot lost reseat work: %q, %v", data, err)
			}
		})
	}
}

func TestFirstAndImplicitChairStepsPlayOnlyTheFirstOfSeveralSeatedChairs(t *testing.T) {
	for _, key := range []string{"", "chairs = \"first\""} {
		t.Run(fmt.Sprintf("key=%s", key), func(t *testing.T) {
			score, err := decodeScore(`
[[steps]]
name = "outline"
kind = "produce"
part = "scout"
writes = "reply"
`+key+`
next = { done = "research" }
[[steps]]
name = "research"
kind = "produce"
part = "scout"
writes = "reply"
chairs = "all"
next = { done = "approved" }
`, "score.toml", "example")
			if err != nil {
				t.Fatal(err)
			}
			e, f := otherScoreEngine(t)
			e.Score = score
			e.Config.Chairs.Parts = map[string][]Chair{"scout": {{Profile: "first"}, {Profile: "second"}, {Profile: "third"}}}
			if result := run(t, e); result.Outcome != Approved {
				t.Fatalf("result = %+v", result)
			}
			steps := testerSteps(f, "outline")
			if len(steps) != 1 || steps[0].Chair.Profile != "first" || filepath.Base(steps[0].ReplyPath) != "outline.md" {
				t.Fatalf("steps = %+v", steps)
			}
			if len(testerSteps(f, "research")) != 3 {
				t.Fatal("parallel step did not play all chairs")
			}
		})
	}
}

func TestBuiltInPlannerInAFileScoreReadsEveryParallelReply(t *testing.T) {
	score, err := decodeScore(`
[[steps]]
name = "research"
kind = "produce"
part = "scout"
writes = "reply"
chairs = "all"
next = { done = "plan" }
[[steps]]
name = "plan"
kind = "produce"
part = "planner"
reads = ["research"]
writes = "reply"
next = { done = "approved" }
`, "score.toml", "example")
	if err != nil {
		t.Fatal(err)
	}
	e, f := otherScoreEngine(t)
	e.Score = score
	e.Config.Chairs.Parts = map[string][]Chair{"scout": {{Profile: "one"}, {Profile: "two"}, {Profile: "three"}}}
	if result := run(t, e); result.Outcome != Approved {
		t.Fatalf("result = %+v", result)
	}
	steps := testerSteps(f, "plan")
	if len(steps) != 1 {
		t.Fatalf("plan steps = %+v", steps)
	}
	for _, name := range []string{"research-1.md", "research-2.md", "research-3.md"} {
		if !strings.Contains(steps[0].Prompt, name) {
			t.Errorf("missing %s in %s", name, steps[0].Prompt)
		}
	}
	defaultStep, _ := DefaultScore().Step("plan")
	e.Score = DefaultScore()
	askSize := defaultStep.Sizes && e.sizing().Mode != SizingOff && e.sizing().Decider == DeciderPlanner
	want := planPrompt(e.Config.Task, "plan.md", askSize, e.lessonsFor("plan", Chair{}))
	if got := e.brief(defaultStep, handoff{}, "plan.md", Chair{}); got != want {
		t.Fatalf("default prompt changed: %s", got)
	}
}
