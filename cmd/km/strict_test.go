package main

import (
	"bytes"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jystringfellow/kapellmeister/internal/initrepo"
	"github.com/jystringfellow/kapellmeister/internal/loop"
)

func strictRun(t *testing.T, test string) (code int, out string, result map[string]any) {
	t.Helper()
	t.Setenv(loop.HomeEnv, t.TempDir())
	t.Setenv(loop.StateEnv, t.TempDir())
	t.Setenv(loop.CAOURLEnv, "")
	t.Setenv("TMPDIR", t.TempDir())
	seed := t.TempDir()
	safetyGit(t, seed, "init", "-q", "-b", "main")
	safetyGit(t, seed, "config", "user.name", "Test")
	safetyGit(t, seed, "config", "user.email", "test@example.invalid")
	writeFile(t, filepath.Join(seed, ".gitignore"), ".kapellmeister/\n")
	writeFile(t, filepath.Join(seed, loop.ConfigFile), "[test]\nverify = 'true'\n"+test+"[run]\npreflight_timeout = '0'\nreply_grace = '1ms'\nrounds = 2\n[sizing]\nmode = 'off'\n")
	safetyGit(t, seed, "add", ".gitignore")
	safetyGit(t, seed, "add", "--force", loop.ConfigFile)
	safetyGit(t, seed, "-c", "commit.gpgsign=false", "commit", "-qm", "fixture")
	origin := filepath.Join(t.TempDir(), "origin.git")
	safetyGit(t, seed, "clone", "-q", "--bare", seed, origin)
	repo := filepath.Join(t.TempDir(), "repo")
	safetyGit(t, seed, "clone", "-q", "-b", "main", origin, repo)
	f := &runCAO{}
	args := append([]string{"run", "-repo", repo, "-task", "probe", "-cao", f.serve(t).URL}, runRoleArgs()...)
	var diag bytes.Buffer
	out, _ = captureStdout(t, func() error {
		code = execute(args, &diag)
		return nil
	})
	_, result = onlyRunResult(t)
	return code, out + diag.String(), result
}

func strictWaitStatus(t *testing.T, result map[string]any) int {
	t.Helper()
	var out, diag bytes.Buffer
	err := waitCommand([]string{result["run_id"].(string), loop.StateDir()}, &out, &diag)
	var status *exitStatusError
	if errors.As(err, &status) {
		return status.code
	}
	if err != nil {
		t.Fatal(err)
	}
	return 0
}

func TestARunSaysPlainlyThatStrictPassedAfterTheGateApproved(t *testing.T) {
	code, out, result := strictRun(t, "strict = 'echo strict ok'\nstrict_ready = 'true'\n")
	strict, _ := result["strict"].(map[string]any)
	if code != 0 || result["outcome"] != "approved" || strict["status"] != "passed" || strict["command"] != "echo strict ok" {
		t.Fatal(code, result, out)
	}
	if !strings.Contains(out, "  strict    passed: echo strict ok\n") || strictWaitStatus(t, result) != 0 {
		t.Fatal(out)
	}
}

func TestARunWhoseStrictFailsEndsWithItsOwnOutcomeAndExitStatusAndKeepsTheApprovedChange(t *testing.T) {
	code, out, result := strictRun(t, "strict = 'echo slow suite FAIL; exit 7'\n")
	strict, _ := result["strict"].(map[string]any)
	if code != 5 || result["outcome"] != "strict_failed" || strict["status"] != "failed" || strict["exit_code"] != float64(7) {
		t.Fatal(code, result, out)
	}
	if !strings.Contains(out, "\nstrict_failed after 1 round(s) in ") || !strings.Contains(out, "  strict    failed (exit 7): echo slow suite FAIL; exit 7\n") || strings.Contains(out, "strict    passed") {
		t.Fatal(out)
	}
	if !strings.Contains(out, "  changes   "+result["worktree"].(string)+"\n") {
		t.Fatal(out)
	}
	if status := strictWaitStatus(t, result); status != 5 {
		t.Fatal(status)
	}
}

func TestARunWhoseStrictEnvironmentIsMissingSaysStrictNotRunWithTheProbesOutputAndNeverPasses(t *testing.T) {
	code, out, result := strictRun(t, "strict = 'echo strict ok'\nstrict_ready = 'echo no device attached; exit 3'\n")
	strict, _ := result["strict"].(map[string]any)
	if code != 6 || result["outcome"] != "strict_not_run" || strict["status"] != "not_run" {
		t.Fatal(code, result, out)
	}
	if !strings.Contains(out, "\nstrict_not_run after 1 round(s) in ") || !strings.Contains(out, "  strict    not run: echo no device attached; exit 3 exited 3\n") || !strings.Contains(out, "no device attached\n") {
		t.Fatal(out)
	}
	if strings.Contains(out, "strict    passed") || strings.Contains(out, "strict ok") || strings.Contains(out, "\napproved after") {
		t.Fatal(out)
	}
	if !strings.Contains(out, "  changes   "+result["worktree"].(string)+"\n") {
		t.Fatal(out)
	}
	if status := strictWaitStatus(t, result); status != 6 {
		t.Fatal(status)
	}
}

func TestARunWithoutAStrictCommandSaysNothingAboutStrict(t *testing.T) {
	code, out, result := strictRun(t, "")
	if _, has := result["strict"]; code != 0 || has || strings.Contains(out, "strict") {
		t.Fatal(code, result, out)
	}
}

func strictProofFixture(t *testing.T, test string, suites ...string) string {
	t.Helper()
	root := suiteFixture(t)
	proofWrite(t, root, initrepo.ScriptFile, suiteScript("fast"), 0755)
	proofWrite(t, root, "scripts/strict", suiteScript(suites...), 0755)
	proofWrite(t, root, loop.ConfigFile, suiteConfig(test), 0644)
	return root
}

func TestProveAtTheStrictLevelProvesTheStrictCommandOnCleanCopiesAndBrokenCode(t *testing.T) {
	root := strictProofFixture(t, "strict = 'scripts/strict'\nverify_required = ['fast']\n", "slow")
	slowOnly := suitePatch(t, "slow.patch", suiteSlowOnlyBreak)
	report := proofJSON(t, root, "-level", "strict", "-break", slowOnly)
	if report.ExitStatus != 0 || report.Status != "proved" || report.Command != "scripts/strict" || len(report.Runs) != 2 {
		t.Fatal(report)
	}
	if report := proofJSON(t, root, "-break", slowOnly); report.ExitStatus != 13 || report.Command != "scripts/verify" {
		t.Fatal(report)
	}
	if report := proofJSON(t, root, "-level", "strict"); report.ExitStatus != 11 {
		t.Fatal(report)
	}
}

func TestProveAtTheStrictLevelRefusesWhenNoStrictCommandIsConfiguredOrTheLevelIsUnknown(t *testing.T) {
	root := strictProofFixture(t, "", "slow")
	if report := proofJSON(t, root, "-level", "strict"); report.ExitStatus != 10 || !strings.Contains(report.Reason, "strict is not configured: set [test] strict") {
		t.Fatal(report)
	}
	if code, out := proofCall(t, root, "-level", "nightly"); code == 0 || !strings.Contains(out, `-level must be verify or strict, not "nightly"`) {
		t.Fatal(code, out)
	}
}

func TestProveSaysStrictNotRunWhenTheProbeFailsAndThatIsNotAProof(t *testing.T) {
	root := strictProofFixture(t, "strict = 'scripts/strict'\nstrict_ready = 'echo no device attached; exit 3'\n", "slow")
	report := proofJSON(t, root, "-level", "strict", "-break", suitePatch(t, "slow.patch", suiteSlowOnlyBreak))
	if report.ExitStatus != 17 || report.Status != "strict_not_run" || len(report.Runs) != 0 {
		t.Fatal(report)
	}
	if !strings.Contains(report.Reason, "strict not run: echo no device attached; exit 3 exited 3: no device attached") {
		t.Fatal(report.Reason)
	}
	if code, out := proofCall(t, root, "-level", "strict"); code != 17 || strings.Contains(out, "proved") || !strings.Contains(out, "strict_not_run: strict not run") {
		t.Fatal(code, out)
	}
}

func TestProveRunsTheStrictCommandWhenItsProbePasses(t *testing.T) {
	root := strictProofFixture(t, "strict = 'scripts/strict'\nstrict_ready = 'test -f answer.sh'\n", "slow")
	if report := proofJSON(t, root, "-level", "strict", "-break", suitePatch(t, "slow.patch", suiteSlowOnlyBreak)); report.Status != "proved" {
		t.Fatal(report)
	}
}

func TestABreakPatchThatTouchesTheStrictScriptIsRefused(t *testing.T) {
	root := strictProofFixture(t, "strict = 'scripts/strict'\n", "slow")
	proofGit(t, root, "add", "--force", "scripts/strict")
	proofGit(t, root, "-c", "user.name=t", "-c", "user.email=t@example.invalid", "commit", "-qm", "strict")
	patch := suitePatch(t, "setup.patch", "diff --git a/scripts/strict b/scripts/strict\nold mode 100755\nnew mode 100644\n")
	report := proofJSON(t, root, "-level", "strict", "-break", patch)
	if report.ExitStatus != 10 || !strings.Contains(report.Reason, "patch touches setup files (.kapellmeister/, scripts/verify or scripts/strict)") {
		t.Fatal(report)
	}
}

func TestAStrictTimeoutNamesTheStrictLevel(t *testing.T) {
	root := strictProofFixture(t, "strict = 'scripts/strict'\n", "slow")
	if report := proofJSON(t, root, "-level", "strict", "-timeout", "100ms"); report.ExitStatus != 3 || !strings.Contains(report.Reason, "clean run 1: strict timed out") {
		t.Fatal(report)
	}
}
