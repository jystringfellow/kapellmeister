package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jystringfellow/kapellmeister/internal/loop"
	"github.com/jystringfellow/kapellmeister/internal/prove"
)

const excludedE2E = "[[test.excluded]]\nsuite = 'e2e'\nreason = 'network'\nmethod = 'network namespace'\nevidence = 'exit 7: could not resolve host'\nrun = 'make e2e'\n"

func TestPlayPrintsEachSuiteVerifyExcludesBeforeItStarts(t *testing.T) {
	s := playStanding(t)
	writeFile(t, filepath.Join(s.Worktree, loop.ConfigFile), "[test]\nverify = 'true'\n"+excludedE2E+"[run]\nreply_grace = '1ms'\n")
	fake := &playCAO{}
	out, err := capturePlay(t, []string{s.Session, "-cao", fake.serve(t).URL})
	if err != nil {
		t.Fatalf("play: %v\n%s", err, out)
	}
	excluded, started := strings.Index(out, "verify excludes e2e (network; network namespace)"), strings.Index(out, "km play")
	if excluded < 0 || started < 0 || excluded > started {
		t.Fatal(out)
	}
}

func TestRehearsePrintsEachSuiteVerifyExcludesBeforeItStarts(t *testing.T) {
	t.Setenv(loop.HomeEnv, t.TempDir())
	writeFile(t, loop.UserConfigPath(loop.Home()), builtInAgentConfig)
	t.Setenv(loop.StateEnv, t.TempDir())
	wt := t.TempDir()
	writeFile(t, filepath.Join(wt, loop.ConfigFile), "[test]\nverify = 'echo baseline-failed; exit 3'\n"+excludedE2E)
	out, err := captureStdout(t, func() error {
		return rehearseCommand([]string{"-worktree", wt, "-task", "task", "-cao", unreachableCAO})
	})
	if err == nil || !strings.Contains(err.Error(), "baseline-failed") {
		t.Fatal(err)
	}
	if !strings.Contains(out, "verify excludes e2e (network; network namespace): exit 7: could not resolve host; run it by hand: make e2e\n") {
		t.Fatal(out)
	}
}

func TestAPreflightRetryThatFailsTheSameWayTellsTheUserToRunAgainInAFewMinutes(t *testing.T) {
	const loading = "policy is still loading"
	same := loop.PreflightResult{Retried: true, RetryWaitMS: 20000, RetryWaitError: loading, Error: loading}
	if got, want := timeoutRetryNote(same, time.Minute), "; the retry after waiting 20s failed the same way: run the command again in a few minutes"; got != want {
		t.Fatalf("note = %q", got)
	}
	other := loop.PreflightResult{Retried: true, RetryWaitMS: 20000, RetryWaitError: loading, Error: "profile not found"}
	if got, want := timeoutRetryNote(other, time.Minute), "; first attempt failed (policy is still loading), retried after waiting 20s"; got != want {
		t.Fatalf("note = %q", got)
	}
}

func TestReportJSONCarriesVerifyRetriedInTheShapeResultJSONUses(t *testing.T) {
	root := recordRoot(t)
	retried := []loop.VerifyRetry{{Round: 1, Attempts: 2}, {Round: 2, Attempts: 3}}
	data, err := json.Marshal(ensembleSummary{Result: loop.Result{RunID: "flaky-run", Outcome: loop.Approved, Rounds: 2, VerifyRetried: retried}})
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "flaky-run", "result.json"), string(data))
	var recorded struct {
		VerifyRetried json.RawMessage `json:"verify_retried"`
	}
	if err := json.Unmarshal(data, &recorded); err != nil {
		t.Fatal(err)
	}
	doc := reportJSON(t, root)
	got, err := json.Marshal(doc["runs"].([]any)[0].(map[string]any)["verify_retried"])
	if err != nil || string(got) != string(recorded.VerifyRetried) {
		t.Fatalf("report %s, result.json %s", got, recorded.VerifyRetried)
	}
	totals := doc["totals"].(map[string]any)
	if totals["verify_retried_runs"] != float64(1) || totals["verify_retried"] != nil {
		t.Fatalf("%v", totals)
	}
}

func TestProveChecksBreakPatchesWhenItsScriptsLiveOutsideScriptsAndThereIsNoSuchDirectory(t *testing.T) {
	root := suiteFixture(t)
	if err := os.RemoveAll(filepath.Join(root, "scripts")); err != nil {
		t.Fatal(err)
	}
	proofWrite(t, root, "ci/verify", suiteScript("fast"), 0755)
	proofWrite(t, root, "ci/slow", suiteScript("slow"), 0755)
	proofWrite(t, root, loop.ConfigFile, "[test]\nverify = 'ci/verify'\nstrict = 'ci/slow'\n", 0644)
	if report := proofJSON(t, root, "-break", suitePatch(t, "source.patch", suiteSourceBreak)); report.Status != "proved" {
		t.Fatal(report)
	}
	if report := proofJSON(t, root, "-level", "strict", "-break", suitePatch(t, "slow.patch", suiteSlowOnlyBreak)); report.Status != "proved" {
		t.Fatal(report)
	}
	touching := suitePatch(t, "setup.patch", "diff --git a/ci/slow b/ci/slow\nnew file mode 100644\n--- /dev/null\n+++ b/ci/slow\n@@ -0,0 +1 @@\n+exit 0\n")
	if report := proofJSON(t, root, "-level", "strict", "-break", touching); report.ExitStatus != 10 {
		t.Fatal(report)
	}
}

func TestATuneWhoseBudgetRanOutSaysWhenTheRunsWithoutNetworkSpentItAndWhatWouldHoldThem(t *testing.T) {
	now := suiteClock(t)
	old := runTuneOffline
	t.Cleanup(func() { runTuneOffline = old })
	runTuneOffline = func(_ context.Context, options prove.OfflineOptions) prove.OfflineResult {
		*now = now.Add(time.Hour)
		return prove.OfflineResult{Method: prove.MethodProxy, Status: "failed", Runs: 4, Failed: []prove.OfflineFailure{{Suite: "network", Evidence: "exit 7: no route"}}}
	}
	root := suiteFixture(t)
	f := &tuneCAO{writer: suiteWriter("", "fast", "network")}
	code, out := tuneCall(t, "verify", "-worktree", root, "-cao", f.serve(t).URL, "-verify-timeout", "1m")
	if code == 0 || !strings.Contains(out, "budget_exhausted after 1 round(s)") {
		t.Fatal(code, out)
	}
	if want := "the run without network took 3 more verify runs than the one the budget counts, to confirm each step that failed there: pass -budget with 3m0s more to hold them"; !strings.Contains(out, want) {
		t.Fatal(out)
	}
}
