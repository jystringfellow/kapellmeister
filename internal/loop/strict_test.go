package loop

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
