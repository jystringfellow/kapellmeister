package loop

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func testerEngine(t *testing.T) (*Engine, *fakeStepper) {
	t.Helper()
	e, f, _ := newEngine(t, passing)
	e.Config.Chairs.Tester = &Chair{Provider: "p", Profile: "tonmeister", Address: "leads.tonmeister@thorough"}
	return e, f
}

func testerSteps(f *fakeStepper, name string) []Step {
	var steps []Step
	for _, s := range f.steps {
		if s.Name == name {
			steps = append(steps, s)
		}
	}
	return steps
}

func TestWithoutATesterTheStepSequenceIsExactlyTheScoreWeHadBefore(t *testing.T) {
	e, f, _ := newEngine(t, passing)
	if res := run(t, e); res.Outcome != Approved {
		t.Fatal(res)
	}
	equal(t, f.names(), []string{"plan", "implement[1]", "review[1]", "classify-review[1]", "gate[1]", "classify-gate[1]"})
}

func TestATestPlanStartsAfterThePlanAndRunsAlongsideRoundOnesImplementer(t *testing.T) {
	e, f := testerEngine(t)
	planStarted, implementStarted := make(chan struct{}), make(chan struct{})
	f.actions["test-plan"] = func(s Step) {
		if _, err := os.Stat(filepath.Join(f.dir, e.rel("plan.md"))); err != nil {
			t.Error(err)
		}
		close(planStarted)
		select {
		case <-implementStarted:
		case <-time.After(2 * time.Second):
			t.Error("test-plan did not overlap implement")
		}
		f.write(s.ReplyPath, "blind test plan")
	}
	f.actions["implement"] = func(s Step) {
		close(implementStarted)
		select {
		case <-planStarted:
		case <-time.After(2 * time.Second):
			t.Error("implement did not overlap test-plan")
		}
		f.write(s.ReplyPath, "implementation")
	}
	f.actions["test"] = func(s Step) {
		data, err := os.ReadFile(filepath.Join(f.dir, e.rel("test-plan.md")))
		if err != nil || string(data) != "blind test plan" {
			t.Errorf("test plan was not joined: %s, %v", data, err)
		}
		f.write(s.ReplyPath, "APPROVE")
	}
	if res := run(t, e); res.Outcome != Approved {
		t.Fatal(res)
	}
	if len(testerSteps(f, "test-plan")) != 1 || len(testerSteps(f, "test")) != 1 {
		t.Fatalf("steps: %v", f.names())
	}
}

func TestTheTestPlanPromptCarriesTheTaskAndThePlanButNoImplementationSummary(t *testing.T) {
	e, f := testerEngine(t)
	run(t, e)
	steps := testerSteps(f, "test-plan")
	if len(steps) != 1 {
		t.Fatalf("test-plan steps = %d", len(steps))
	}
	p := steps[0].Prompt
	for _, want := range []string{e.Config.Task, e.rel("plan.md"), "real environment", "missing", "stale", "concurrent", "external services", "unit tests", "git diff", "git status", "no other file"} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt lacks %q: %s", want, p)
		}
	}
	for _, forbidden := range []string{"implement-1.md", "verify-1", "review-1", "gate-1"} {
		if strings.Contains(p, forbidden) {
			t.Errorf("blind prompt contains %q", forbidden)
		}
	}
}

func TestTheTestStepRunsOnlyAfterAGateApproveAndATestApproveYieldsApproved(t *testing.T) {
	e, f := testerEngine(t)
	res := run(t, e)
	if res.Outcome != Approved || res.Rounds != 1 {
		t.Fatal(res)
	}
	steps := testerSteps(f, "test")
	if len(steps) != 1 {
		t.Fatalf("steps: %v", f.names())
	}
	s := steps[0]
	if s.Chair != *e.Config.Chairs.Tester {
		t.Errorf("test chair = %+v", s.Chair)
	}
	names := f.names()
	if slices.Index(names, "test[1]") <= slices.Index(names, "classify-gate[1]") || names[len(names)-1] != "classify-test[1]" {
		t.Fatal(names)
	}
	for _, want := range []string{e.Config.Task, e.rel("plan.md"), e.rel("test-plan.md"), e.rel("implement-1.md"), e.rel("verify-1.txt"), "exited 0", "scratch", "clean up", "did not create", "APPROVE", "CHANGES"} {
		if !strings.Contains(s.Prompt, want) {
			t.Errorf("prompt lacks %q: %s", want, s.Prompt)
		}
	}
	classifier := testerSteps(f, "classify-test")[0]
	if !strings.Contains(classifier.Prompt, "every scenario passed") {
		t.Error(classifier.Prompt)
	}
}

func TestNoTestStepRunsWhenTheGateWithholdsApproval(t *testing.T) {
	e, f := testerEngine(t)
	e.Config.MaxRounds = 1
	f.verdicts["gate"] = []string{"CHANGES"}
	if res := run(t, e); res.Outcome != RoundsExhausted {
		t.Fatal(res)
	}
	if len(testerSteps(f, "test")) != 0 {
		t.Fatal(f.names())
	}
}

func TestATestChangesStartsAnotherRoundWithTheTestReportLeadingTheFeedback(t *testing.T) {
	e, f := testerEngine(t)
	f.verdicts["test"] = []string{"CHANGES"}
	res := run(t, e)
	if res.Outcome != Approved || res.Rounds != 2 {
		t.Fatal(res)
	}
	impl := testerSteps(f, "implement")
	p := impl[1].Prompt
	testAt := strings.Index(p, e.rel("test-1.md"))
	for _, name := range []string{"review-1.md", "verify-1.txt"} {
		if testAt < 0 || testAt >= strings.Index(p, e.rel(name)) {
			t.Errorf("test report must lead feedback: %s", p)
		}
	}
	if !strings.Contains(p, "failing regression test") || !strings.Contains(p, "confirm it fails") || !strings.Contains(p, "then fix") {
		t.Error(p)
	}
	if len(testerSteps(f, "test-plan")) != 1 {
		t.Fatal("test plan was repeated")
	}
	for _, s := range testerSteps(f, "test") {
		if !strings.Contains(s.Prompt, e.rel("test-plan.md")) {
			t.Error(s.Prompt)
		}
	}
}

func TestATestStepThatChangesTheWorktreeFailsTheRunNamingTheTesterChair(t *testing.T) {
	for _, path := range []string{"main.go", "untracked.txt"} {
		t.Run(path, func(t *testing.T) {
			e, f := testerEngine(t)
			if path == "main.go" {
				f.actions["implement"] = func(s Step) {
					f.write(path, "before")
					f.write(s.ReplyPath, "implemented")
				}
			}
			f.actions["test"] = func(s Step) {
				f.write(path, "changed")
				f.write(s.ReplyPath, "APPROVE")
			}
			res := run(t, e)
			if res.Outcome != Failed || !strings.Contains(res.Reason, "test chair p:tonmeister") {
				t.Fatal(res)
			}
			if !strings.Contains(fmt.Sprint(e.Records), `"tree_changed":true`) || len(testerSteps(f, "classify-test")) != 0 {
				t.Fatalf("records: %s, steps: %v", e.Records, f.names())
			}
		})
	}
}

func TestAFailedTestPlanFailsTheRun(t *testing.T) {
	for _, gate := range []string{"APPROVE", "CHANGES"} {
		t.Run(gate, func(t *testing.T) {
			e, f := testerEngine(t)
			e.Config.MaxRounds = 1
			f.verdicts["gate"] = []string{gate}
			f.errFor = func(s Step) error {
				if s.Name == "test-plan" {
					return errors.New("test-plan failed")
				}
				return nil
			}
			res := run(t, e)
			if res.Outcome != Failed || !strings.Contains(res.Reason, "test-plan failed") {
				t.Fatal(res)
			}
		})
	}
}

func TestRoundsAreExhaustedWhenTheTesterNeverApproves(t *testing.T) {
	e, f := testerEngine(t)
	f.verdicts["test"] = []string{"CHANGES", "UNCLEAR", "CHANGES"}
	res := run(t, e)
	if res.Outcome != RoundsExhausted || res.Rounds != 3 || len(testerSteps(f, "test-plan")) != 1 || len(testerSteps(f, "test")) != 3 {
		t.Fatalf("%+v, steps: %v", res, f.names())
	}
}

func TestTheTesterHasItsOwnStepKeyDefaultingLikeTheGate(t *testing.T) {
	p := DefaultPolicy()
	if !slices.Contains(StepKeys, "test") || p.Steps["test"] != p.Steps["gate"] {
		t.Fatalf("test policy = %+v", p.Steps["test"])
	}
	e, f := testerEngine(t)
	chair := Chair{Provider: "claude_code", Profile: "km-claude"}
	e.Config.Chairs.Tester, e.Config.Chairs.Classifier = &chair, chair
	p.Sizing.Mode = SizingOn
	f.actions["plan"] = func(s Step) { f.write(s.ReplyPath, "Size: large — live testing") }
	e.Config.Policy = &p
	run(t, e)
	for _, name := range []string{"test-plan", "test", "classify-test"} {
		steps := testerSteps(f, name)
		if len(steps) != 1 {
			t.Fatalf("%s steps: %v", name, steps)
		}
		want := "large"
		if name == "classify-test" {
			want = "small"
		}
		if steps[0].Assignment.Tier != want {
			t.Errorf("%s assignment: %+v", name, steps[0].Assignment)
		}
		if !strings.Contains(fmt.Sprint(e.Records), `"step":"`+name+`"`) {
			t.Errorf("missing telemetry for %s", name)
		}
	}
}

func TestTheTestPlanAndTestStepsAreLabelledAsTheTester(t *testing.T) {
	for _, name := range []string{"test-plan", "test"} {
		if got := TerminalLabel("r1", name); got != "km r1 · tester" {
			t.Errorf("%s label = %s", name, got)
		}
	}
}

func TestChairsTesterAndEnsemblesTesterAreAcceptedAndOnlyReviewerTakesAList(t *testing.T) {
	for _, table := range []string{"chairs", "ensembles.live"} {
		for _, spec := range []string{"'claude'", "['claude']"} {
			t.Run(table+spec, func(t *testing.T) {
				home := t.TempDir()
				if err := os.WriteFile(UserConfigPath(home), []byte("["+table+"]\ntester = "+spec+"\n[steps.test]\ntier = 'large'\n"), 0600); err != nil {
					t.Fatal(err)
				}
				s, err := LoadSettings(home, "")
				if strings.HasPrefix(spec, "[") {
					if err == nil || !strings.Contains(err.Error(), "only reviewer takes a list") {
						t.Fatalf("list error: %v", err)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				chairs := s.Chairs
				if table != "chairs" {
					chairs = s.Ensembles["live"].Chairs()
				}
				if chairs["tester"] != "claude" || s.Policy.Steps["test"].Tier != "large" {
					t.Fatalf("settings: %+v", s)
				}
			})
		}
	}
}

func TestAnUnknownChairRoleStillListsTheValidRoles(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(UserConfigPath(home), []byte("[chairs]\nunknown = 'claude'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := LoadSettings(home, "")
	if err == nil || !strings.Contains(err.Error(), "tester") || !strings.Contains(err.Error(), "unknown role") {
		t.Fatal(err)
	}
}

func TestATestStepFailureFailsTheRun(t *testing.T) {
	e, f := testerEngine(t)
	f.errFor = func(s Step) error {
		if s.Name == "test" {
			return errors.New("live execution failed")
		}
		return nil
	}
	if res := run(t, e); res.Outcome != Failed || !strings.Contains(res.Reason, "live execution failed") {
		t.Fatal(res)
	}
	if len(testerSteps(f, "classify-test")) != 0 {
		t.Fatal(f.names())
	}
}

func TestRunCancelsAndJoinsTheTestPlanBeforeReturningAfterAnImplementationFailure(t *testing.T) {
	e, f := testerEngine(t)
	started, stopped := make(chan struct{}), make(chan struct{})
	e.Stepper = &cancellingTestPlanStepper{fakeStepper: f, started: started, stopped: stopped}
	f.errFor = func(s Step) error {
		if s.Name == "implement" {
			select {
			case <-started:
			case <-time.After(2 * time.Second):
				t.Error("test-plan never started")
			}
			return errors.New("implement failed")
		}
		return nil
	}
	if res := run(t, e); res.Outcome != Failed {
		t.Fatal(res)
	}
	select {
	case <-stopped:
	default:
		t.Fatal("test-plan outlived Run")
	}
}

type cancellingTestPlanStepper struct {
	*fakeStepper
	started chan struct{}
	stopped chan struct{}
}

func (f *cancellingTestPlanStepper) Run(ctx context.Context, s Step) (StepResult, error) {
	if s.Name != "test-plan" {
		return f.fakeStepper.Run(ctx, s)
	}
	close(f.started)
	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		f.t.Error("test-plan was not cancelled")
	}
	close(f.stopped)
	return StepResult{}, ctx.Err()
}

func TestATestReportEndingInABareVerdictLineIsTakenAtItsWordWithoutTheClassifier(t *testing.T) {
	for _, tc := range []struct {
		last    string
		outcome string
		rounds  int
	}{{"APPROVE", Approved, 1}, {"  CHANGES  ", Approved, 2}} {
		t.Run(strings.TrimSpace(tc.last), func(t *testing.T) {
			e, f := testerEngine(t)
			f.verdicts["test"] = []string{"UNCLEAR", "UNCLEAR"}
			reports := 0
			f.actions = map[string]func(Step){"test": func(s Step) {
				reports++
				last := tc.last
				if reports > 1 {
					last = "APPROVE"
				}
				f.write(s.ReplyPath, "| scenario | Observed, not a failure |\n\n"+last+"\n\n")
			}}
			res := run(t, e)
			if res.Outcome != tc.outcome || res.Rounds != tc.rounds {
				t.Fatal(res)
			}
			if n := len(testerSteps(f, "classify-test")); n != 0 {
				t.Errorf("classifier ran %d time(s) on a report with a bare verdict line", n)
			}
		})
	}
}

func TestATestReportWithoutABareVerdictLineIsStillClassified(t *testing.T) {
	e, f := testerEngine(t)
	f.verdicts["test"] = []string{"APPROVE"}
	f.actions = map[string]func(Step){"test": func(s Step) {
		f.write(s.ReplyPath, "Everything passed, so I APPROVE.\n")
	}}
	if res := run(t, e); res.Outcome != Approved {
		t.Fatal(res)
	}
	if n := len(testerSteps(f, "classify-test")); n != 1 {
		t.Errorf("classify-test ran %d time(s)", n)
	}
}
