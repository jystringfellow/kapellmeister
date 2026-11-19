package loop

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func strictEngine(t *testing.T, strict, ready string) (*Engine, *fakeStepper, *bytes.Buffer, string) {
	t.Helper()
	e, f, _ := newEngine(t, passing)
	mark := filepath.Join(t.TempDir(), "ran")
	e.Config.StrictCommand = strings.ReplaceAll(strict, "MARK", mark)
	e.Config.StrictReadyCommand = ready
	var progress bytes.Buffer
	e.Progress = &progress
	return e, f, &progress, mark
}

func strictRuns(t *testing.T, mark string) int {
	t.Helper()
	data, err := os.ReadFile(mark)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return strings.Count(string(data), "ran\n")
}

func TestStrictRunsOnceAfterTheGateApprovesAndAPassIsSaidPlainly(t *testing.T) {
	e, _, progress, mark := strictEngine(t, "echo ran >>MARK", "true")
	res := run(t, e)
	if res.Outcome != Approved || res.Strict == nil || res.Strict.Status != "passed" || res.Strict.Command != e.Config.StrictCommand || strictRuns(t, mark) != 1 {
		t.Fatalf("got %+v %+v", res, res.Strict)
	}
	if !strings.Contains(progress.String(), "→ strict "+e.Config.StrictCommand) || !strings.Contains(progress.String(), "  strict passed in ") {
		t.Fatal(progress.String())
	}
}

func TestAFailedStrictIsItsOwnOutcomeAndLeavesTheApprovedChangeInPlace(t *testing.T) {
	e, f, _, mark := strictEngine(t, "echo ran >>MARK; echo slow suite FAIL; exit 7", "")
	f.actions["implement"] = func(s Step) {
		if err := os.WriteFile(filepath.Join(e.Config.Worktree, "change.txt"), []byte("approved\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	res := run(t, e)
	if res.Outcome != StrictFailed || res.Strict == nil || res.Strict.Status != "failed" || res.Strict.ExitCode != 7 || strictRuns(t, mark) != 1 {
		t.Fatalf("got %+v %+v", res, res.Strict)
	}
	if !strings.Contains(res.Reason, "change approved by the gate, but strict failed (exit 7)") || !strings.Contains(res.Reason, "the approved change is left in place") {
		t.Fatal(res.Reason)
	}
	if _, err := os.Stat(filepath.Join(e.Config.Worktree, "change.txt")); err != nil {
		t.Fatal(err)
	}
	report, err := os.ReadFile(filepath.Join(e.Config.Worktree, res.Strict.Reply))
	if err != nil || !strings.Contains(string(report), "slow suite FAIL") {
		t.Fatal(string(report), err)
	}
}

func TestStrictNotRunIsItsOwnOutcomeNeverAPassAndPrintsTheProbesOutput(t *testing.T) {
	e, _, progress, mark := strictEngine(t, "echo ran >>MARK", "echo no device attached; exit 3")
	res := run(t, e)
	if res.Outcome != StrictNotRun || res.Outcome == Approved || res.Strict == nil || res.Strict.Status != "not_run" || strictRuns(t, mark) != 0 {
		t.Fatalf("got %+v %+v", res, res.Strict)
	}
	if !strings.Contains(res.Reason, "change approved by the gate, but strict not run: echo no device attached; exit 3 exited 3: no device attached") {
		t.Fatal(res.Reason)
	}
	if !strings.Contains(progress.String(), "  strict not run: ") || !strings.Contains(progress.String(), "no device attached") {
		t.Fatal(progress.String())
	}
}

func TestWithoutAStrictCommandNothingRunsAndTheResultSaysNothingAboutStrict(t *testing.T) {
	e, _, progress, _ := strictEngine(t, "", "")
	res := run(t, e)
	if res.Outcome != Approved || res.Strict != nil || strings.Contains(progress.String(), "strict") {
		t.Fatalf("got %+v %s", res, progress)
	}
}

func TestStrictDoesNotRunWhenTheChangeWasNotApproved(t *testing.T) {
	e, f, _, mark := strictEngine(t, "echo ran >>MARK", "")
	f.verdicts["gate"] = []string{"CHANGES", "CHANGES", "CHANGES"}
	res := run(t, e)
	if res.Outcome != RoundsExhausted || res.Strict != nil || strictRuns(t, mark) != 0 {
		t.Fatalf("got %+v", res)
	}
}

func TestStrictRunsAfterTheTesterApprovesToo(t *testing.T) {
	e, _, _, mark := strictEngine(t, "echo ran >>MARK", "")
	tester := Chair{Provider: "p", Profile: "x"}
	e.Config.Chairs.Tester = &tester
	res := run(t, e)
	if res.Outcome != Approved || res.Strict == nil || res.Strict.Status != "passed" || strictRuns(t, mark) != 1 {
		t.Fatalf("got %+v %+v", res, res.Strict)
	}
}

func strictConfig(t *testing.T, test string) (TestConfig, error) {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, filepath.Dir(ConfigFile)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ConfigFile), []byte("[test]\nverify = 'scripts/verify'\n"+test), 0o644); err != nil {
		t.Fatal(err)
	}
	return ResolveTest(dir)
}

func TestTheStrictLevelIsTwoKeysBesideCheckAndVerifyAndAProbeNeedsAStrictCommand(t *testing.T) {
	test, err := strictConfig(t, "strict = 'scripts/strict'\nstrict_ready = 'scripts/strict-ready'\n")
	if err != nil || test.Strict != "scripts/strict" || test.StrictReady != "scripts/strict-ready" || test.Verify != "scripts/verify" {
		t.Fatal(test, err)
	}
	if test, err := strictConfig(t, ""); err != nil || test.Strict != "" || test.StrictReady != "" {
		t.Fatal(test, err)
	}
	if _, err := strictConfig(t, "strict_ready = 'scripts/strict-ready'\n"); err == nil || !strings.Contains(err.Error(), "test.strict_ready: needs test.strict") {
		t.Fatal(err)
	}
}

func interruptWhenMarked(t *testing.T, mark string) (context.Context, func()) {
	t.Helper()
	ctx, cancel := context.WithCancelCause(context.Background())
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-done:
				return
			case <-time.After(5 * time.Millisecond):
			}
			if _, err := os.Stat(mark); err == nil {
				cancel(&SignalError{Signal: syscall.SIGINT})
				return
			}
		}
	}()
	return ctx, func() { close(done); cancel(nil) }
}

func TestAnInterruptDuringStrictSaysStrictDidNotFinishInsteadOfAPlainFailure(t *testing.T) {
	e, _, _, mark := strictEngine(t, "echo ran >>MARK; exec sleep 30", "")
	ctx, stop := interruptWhenMarked(t, mark)
	defer stop()
	res, err := e.Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != StrictNotRun || res.Strict == nil || res.Strict.Status != "not_run" || res.Strict.Command != e.Config.StrictCommand {
		t.Fatalf("got %+v %+v", res, res.Strict)
	}
	const want = "strict did not finish: interrupted by SIGINT"
	if res.Strict.Reason != want || !strings.Contains(res.Reason, "change approved by the gate, but "+want) || !strings.Contains(res.Reason, "strict still has to pass") {
		t.Fatalf("%q %q", res.Strict.Reason, res.Reason)
	}
}

func TestAnInterruptDuringTheProbeSaysTheProbeDidNotFinishAndStrictDidNotRun(t *testing.T) {
	e, _, _, mark := strictEngine(t, "echo strict >>MARK.strict", "echo ran >>MARK; exec sleep 30")
	e.Config.StrictReadyCommand = strings.ReplaceAll(e.Config.StrictReadyCommand, "MARK", mark)
	ctx, stop := interruptWhenMarked(t, mark)
	defer stop()
	res, err := e.Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != StrictNotRun || res.Strict == nil || res.Strict.Status != "not_run" {
		t.Fatalf("got %+v %+v", res, res.Strict)
	}
	if want := "the strict_ready probe did not finish: interrupted by SIGINT"; res.Strict.Reason != want || !strings.Contains(res.Reason, want) {
		t.Fatalf("%q %q", res.Strict.Reason, res.Reason)
	}
	if _, err := os.Stat(mark + ".strict"); err == nil {
		t.Fatal("strict ran after its probe was interrupted")
	}
}

func TestStrictOutputThatCannotBeSavedKeepsItsRealStatusAndSaysSo(t *testing.T) {
	e, _, _, _ := strictEngine(t, "echo slow suite FAIL; exit 7", "")
	if err := os.MkdirAll(filepath.Join(e.Config.Worktree, RunDirRel(e.Config.RunID), "strict.txt"), 0o755); err != nil {
		t.Fatal(err)
	}
	res := run(t, e)
	if res.Outcome != StrictFailed || res.Strict == nil || res.Strict.Status != "failed" || res.Strict.ExitCode != 7 || res.Strict.Reply != "" {
		t.Fatalf("got %+v %+v", res, res.Strict)
	}
	if !strings.HasPrefix(res.Strict.Reason, "strict's output could not be saved: ") || !strings.Contains(res.Reason, "strict's output could not be saved") {
		t.Fatalf("%q %q", res.Strict.Reason, res.Reason)
	}
}

func TestStrictIsNamedAsNotReachedWhenTheGateApprovesAndTheTesterDoesNotFinish(t *testing.T) {
	for name, fail := range map[string]func(cancel context.CancelCauseFunc, ctx context.Context) error{
		"interrupted": func(cancel context.CancelCauseFunc, ctx context.Context) error {
			cancel(&SignalError{Signal: syscall.SIGINT})
			return ctx.Err()
		},
		"timed out": func(context.CancelCauseFunc, context.Context) error { return context.DeadlineExceeded },
	} {
		t.Run(name, func(t *testing.T) {
			e, f, _, mark := strictEngine(t, "echo ran >>MARK", "")
			e.Config.Chairs.Tester = &Chair{Provider: "p", Profile: "tester"}
			ctx, cancel := context.WithCancelCause(context.Background())
			defer cancel(nil)
			f.contextActions = map[string]func(context.Context, Step) error{"test": func(ctx context.Context, s Step) error { return fail(cancel, ctx) }}
			res, err := e.Run(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if res.Outcome != Untested || res.Strict == nil || res.Strict.Status != "not_reached" || res.Strict.Command != e.Config.StrictCommand || strictRuns(t, mark) != 0 {
				t.Fatalf("got %+v %+v", res, res.Strict)
			}
			if res.Strict.Reason != "strict not reached: the tester did not finish" {
				t.Fatal(res.Strict.Reason)
			}
		})
	}
}

func TestAFailedRunSaysNothingAboutStrict(t *testing.T) {
	e, f, _, mark := strictEngine(t, "echo ran >>MARK", "")
	f.errFor = func(s Step) error {
		if s.Name == "plan" {
			return errors.New("planner broke")
		}
		return nil
	}
	if res := run(t, e); res.Outcome != Failed || res.Strict != nil || strictRuns(t, mark) != 0 {
		t.Fatalf("got %+v %+v", res, res.Strict)
	}
}

func strictScript(t *testing.T, dir, name, body string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestAStrictScriptTheRunChangedIsNamedInTheResult(t *testing.T) {
	e, f, progress, _ := strictEngine(t, "./scripts/strict --all", "scripts/strict-ready")
	strictScript(t, e.Config.Worktree, "scripts/strict", "#!/bin/sh\nexit 7\n")
	strictScript(t, e.Config.Worktree, "scripts/strict-ready", "#!/bin/sh\nexit 0\n")
	f.actions["implement"] = func(s Step) {
		strictScript(t, e.Config.Worktree, "scripts/strict", "#!/bin/sh\nexit 0\n")
	}
	res := run(t, e)
	if res.Outcome != Approved || res.Strict == nil || res.Strict.Status != "passed" {
		t.Fatalf("got %+v %+v", res, res.Strict)
	}
	if strings.Join(res.Strict.Changed, ",") != "scripts/strict" {
		t.Fatal(res.Strict.Changed)
	}
	if !strings.Contains(progress.String(), "  strict: this run changed scripts/strict") {
		t.Fatal(progress.String())
	}
}

func TestAStrictScriptTheRunLeftAloneIsNotNamedAsChanged(t *testing.T) {
	e, _, progress, _ := strictEngine(t, "scripts/strict", "")
	strictScript(t, e.Config.Worktree, "scripts/strict", "#!/bin/sh\nexit 0\n")
	res := run(t, e)
	if res.Strict == nil || res.Strict.Status != "passed" || len(res.Strict.Changed) != 0 || strings.Contains(progress.String(), "changed") {
		t.Fatalf("got %+v %s", res.Strict, progress)
	}
}

func TestAMissingProbeScriptIsABrokenProbeNotAMissingEnvironment(t *testing.T) {
	e, _, progress, mark := strictEngine(t, "echo ran >>MARK", "scripts/strict-ready --device")
	res := run(t, e)
	if res.Outcome != StrictNotRun || res.Strict == nil || res.Strict.Status != "not_run" || strictRuns(t, mark) != 0 {
		t.Fatalf("got %+v %+v", res, res.Strict)
	}
	const want = "the strict_ready probe is broken: scripts/strict-ready does not exist"
	if res.Strict.Reason != want || !strings.Contains(res.Reason, want) || !strings.Contains(res.Reason, "fix [test] strict_ready") || !strings.Contains(progress.String(), want) {
		t.Fatalf("%q %q %s", res.Strict.Reason, res.Reason, progress)
	}
	strictScript(t, e.Config.Worktree, "scripts/strict-ready", "#!/bin/sh\nexit 0\n")
	if err := os.Chmod(filepath.Join(e.Config.Worktree, "scripts/strict-ready"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := ProbeFault(e.Config.Worktree, e.Config.StrictReadyCommand); got != "scripts/strict-ready is not executable" {
		t.Fatal(got)
	}
	if got := ProbeFault(e.Config.Worktree, "test -f answer.sh"); got != "" {
		t.Fatal(got)
	}
}

func TestACommandThatIsOnlySpacesOrACommentIsRefusedWhenTheConfigLoads(t *testing.T) {
	for key, value := range map[string]string{"strict": "' '", "strict_ready": "'# later'", "check": "'\t'"} {
		_, err := strictConfig(t, "strict = 'scripts/strict'\n"+key+" = "+value+"\n")
		if key == "strict" {
			_, err = strictConfig(t, key+" = "+value+"\n")
		}
		if err == nil || !strings.Contains(err.Error(), "test."+key+": the command is empty: write a command or remove the key") {
			t.Fatal(key, err)
		}
	}
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, filepath.Dir(ConfigFile)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ConfigFile), []byte("[test]\nverify = \"\"\"\n# nothing yet\n\n\"\"\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveTest(dir); err == nil || !strings.Contains(err.Error(), "test.verify: the command is empty") {
		t.Fatal(err)
	}
	if test, err := strictConfig(t, "strict = 'scripts/strict # the slow suites'\n"); err != nil || test.Strict == "" {
		t.Fatal(test, err)
	}
}
