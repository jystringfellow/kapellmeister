package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jystringfellow/kapellmeister/internal/initrepo"
	"github.com/jystringfellow/kapellmeister/internal/loop"
	"github.com/jystringfellow/kapellmeister/internal/prove"
)

func limitsTimeout(t *testing.T) *int {
	t.Helper()
	now := suiteClock(t)
	old := runTuneProof
	t.Cleanup(func() { runTuneProof = old })
	calls := 0
	runTuneProof = func(_ context.Context, opts prove.Options) prove.Result {
		calls++
		*now = now.Add(opts.Timeout)
		return prove.Result{Status: "timeout", ExitStatus: 3, Reason: "clean run 1: verify timed out", Runs: []prove.RunResult{{Name: "clean run 1", Status: "timed_out"}}}
	}
	return &calls
}

const limitsDerived = "budget 3h55m0s: a whole proof at the 45m0s verify timeout is two clean runs, 2 break runs and one run without network (3h45m0s), plus 10m0s for the chairs; pass -budget to set it yourself\n"

func TestTuneSizesItsDefaultBudgetFromTheVerifyTimeoutFlagAndSaysHowBeforeSeatingAnyChair(t *testing.T) {
	root := suiteFixture(t)
	f := &tuneCAO{writer: suiteWriter("", "fast", "slow")}
	calls := limitsTimeout(t)
	code, out := tuneCall(t, "verify", "-worktree", root, "-cao", f.serve(t).URL, "-verify-timeout", "45m")
	if code == 0 || !strings.HasPrefix(out, limitsDerived) {
		t.Fatal(code, out)
	}
	if *calls != 1 || !strings.Contains(out, "and 3h10m0s of the budget is left") {
		t.Fatal(*calls, out)
	}
}

func TestTuneSizesItsDefaultBudgetFromTheSavedVerifyTimeout(t *testing.T) {
	root := suiteFixture(t)
	proofWrite(t, root, loop.ConfigFile, suiteConfig("verify_timeout = '45m'\n"), 0644)
	f := &tuneCAO{writer: suiteWriter("verify_timeout = '45m'\n", "fast", "slow")}
	limitsTimeout(t)
	if _, out := tuneCall(t, "verify", "-worktree", root, "-cao", f.serve(t).URL); !strings.Contains(out, limitsDerived) {
		t.Fatal(out)
	}
}

func TestTheDefaultBudgetIsThirtyFiveMinutesAtTheDefaultVerifyTimeoutAndCountsOneBreakPerRequiredSuite(t *testing.T) {
	root := suiteFixture(t)
	f := &tuneCAO{writer: suiteWriter("", "fast", "slow")}
	limitsTimeout(t)
	if _, out := tuneCall(t, "verify", "-worktree", root, "-cao", f.serve(t).URL); !strings.Contains(out, "budget 35m0s: a whole proof at the 5m0s verify timeout is two clean runs, 2 break runs and one run without network (25m0s), plus 10m0s for the chairs; pass -budget to set it yourself\n") {
		t.Fatal(out)
	}
	if _, out := tuneCall(t, "verify", "-worktree", root, "-cao", f.serve(t).URL, "-require", "fast", "-require", "slow", "-require", "unit"); !strings.Contains(out, "budget 40m0s: a whole proof at the 5m0s verify timeout is two clean runs, 3 break runs and one run without network (30m0s), plus 10m0s for the chairs") {
		t.Fatal(out)
	}
}

func TestAnExplicitBudgetIsKeptAndTuneSaysUpFrontWhetherAWholeProofFitsItAndWhatWould(t *testing.T) {
	root := suiteFixture(t)
	f := &tuneCAO{writer: suiteWriter("", "fast", "slow")}
	calls := limitsTimeout(t)
	code, out := tuneCall(t, "verify", "-worktree", root, "-cao", f.serve(t).URL, "-verify-timeout", "45m", "-budget", "50m")
	short := strings.HasPrefix(out, "-budget 50m0s does not hold a whole proof at the 45m0s verify timeout: two clean runs, 2 break runs and one run without network can take 3h45m0s, plus 10m0s for the chairs; -budget 3h55m0s would\n")
	if code == 0 || !short || *calls != 1 || !strings.Contains(out, "and 5m0s of the budget is left") {
		t.Fatal(code, *calls, out)
	}
	if _, out := tuneCall(t, "verify", "-worktree", root, "-cao", f.serve(t).URL, "-verify-timeout", "45m", "-budget", "4h"); !strings.Contains(out, "-budget 4h0m0s holds a whole proof at the 45m0s verify timeout: two clean runs, 2 break runs and one run without network can take 3h45m0s, plus 10m0s for the chairs\n") {
		t.Fatal(out)
	}
}

func limitsThreeRounds(t *testing.T) {
	t.Helper()
	path := filepath.Join(loop.Home(), "config.toml")
	config, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(config), "rounds = 1") {
		t.Fatal(string(config), err)
	}
	proofWrite(t, loop.Home(), "config.toml", strings.Replace(string(config), "rounds = 1", "rounds = 3", 1), 0644)
}

func TestATuneAtTheDefaultVerifyTimeoutEndsAfterOneCleanRunTimeoutWithoutAnotherWriterStepAndNamesTheFlag(t *testing.T) {
	root := suiteFixture(t)
	limitsThreeRounds(t)
	f := &tuneCAO{writer: suiteWriter("", "fast", "slow")}
	calls := limitsTimeout(t)
	code, out := tuneCall(t, "verify", "-worktree", root, "-cao", f.serve(t).URL, "-budget", "10h")
	if code == 0 || *calls != 1 || strings.Contains(out, "raised") || len(f.steps) != 1 || f.steps[0] != "tune-1.json" {
		t.Fatal(code, *calls, f.steps, out)
	}
	if !strings.Contains(out, "\nverify_limit after 1 round(s) in ") || strings.Contains(out, "rounds_exhausted") {
		t.Fatal(out)
	}
	if !strings.Contains(out, "clean run 1: verify timed out; a verify is never narrowed for time, and 5m0s is only the default limit: pass -verify-timeout with the time one verify run needs, and km sizes the budget from it and saves the limit") {
		t.Fatal(out)
	}
	if !initrepo.IsStub(initRead(t, root, initrepo.ScriptFile)) || strings.Contains(initRead(t, root, loop.ConfigFile), "verify_timeout") {
		t.Fatal(initRead(t, root, loop.ConfigFile))
	}
}

func TestARaiseThatDoesNotFitEndsTheRunWithoutAnotherWriterStepAndNamesTheLimitAndTheBudgetKmWouldDeriveFromIt(t *testing.T) {
	root := suiteFixture(t)
	limitsThreeRounds(t)
	f := &tuneCAO{writer: suiteWriter("", "fast", "slow")}
	calls := limitsTimeout(t)
	code, out := tuneCall(t, "verify", "-worktree", root, "-cao", f.serve(t).URL, "-verify-timeout", "45m")
	if code == 0 || *calls != 1 || len(f.steps) != 1 || !strings.Contains(out, "\nverify_limit after 1 round(s) in ") {
		t.Fatal(code, *calls, f.steps, out)
	}
	if !strings.Contains(out, "the timeout is not raised to 1h30m0s: a proof at that limit can take 9h0m0s (two clean runs, 3 break runs and one run without network) and 3h10m0s of the budget is left: pass -verify-timeout 1h30m0s, which sizes the budget at 7h40m0s, and pass -budget for more") {
		t.Fatal(out)
	}
	f = &tuneCAO{writer: suiteWriter("", "fast", "slow")}
	if _, out := tuneCall(t, "verify", "-worktree", root, "-cao", f.serve(t).URL, "-verify-timeout", "1h30m"); !strings.HasPrefix(out, "budget 7h40m0s: ") {
		t.Fatal(out)
	}
}

func TestTuneRefusesToDeriveABudgetAboveEightHoursAndAsksForAnExplicitOneBeforeSeatingAnyChair(t *testing.T) {
	root := suiteFixture(t)
	proofWrite(t, root, loop.ConfigFile, suiteConfig("verify_timeout = '100h'\n"), 0644)
	f := &tuneCAO{writer: suiteWriter("verify_timeout = '100h'\n", "fast", "slow")}
	calls := limitsTimeout(t)
	code, out := tuneCall(t, "verify", "-worktree", root, "-cao", f.serve(t).URL)
	if code == 0 || *calls != 0 || len(f.steps) != 0 || len(f.seated) != 0 {
		t.Fatal(code, *calls, f.steps, f.seated, out)
	}
	if !strings.Contains(out, "a whole proof at the 100h0m0s verify timeout needs a budget of 500h10m0s, more than the 8h0m0s km sets by itself: pass -budget to allow it, or -verify-timeout for a shorter limit") {
		t.Fatal(out)
	}
	if _, out := tuneCall(t, "verify", "-worktree", root, "-cao", f.serve(t).URL, "-budget", "600h"); !strings.HasPrefix(out, "-budget 600h0m0s holds a whole proof at the 100h0m0s verify timeout") || *calls != 1 {
		t.Fatal(*calls, out)
	}
}

func TestADetachedTunePrintsItsBudgetLineFromTheParent(t *testing.T) {
	root := suiteFixture(t)
	code, out := tuneCall(t, "verify", "-worktree", root, "-cao", unreachableCAO, "-detach", "-verify-timeout", "45m")
	if code == 0 || !strings.HasPrefix(out, limitsDerived) {
		t.Fatal(code, out)
	}
}

func TestInitRefusesASavedVerifyTimeoutAboveEightHoursUnlessTheTimeoutIsGivenByFlag(t *testing.T) {
	root := suiteFixture(t)
	proofWrite(t, root, initrepo.ScriptFile, suiteScript("fast"), 0755)
	proofWrite(t, root, loop.ConfigFile, suiteConfig("verify_timeout = '100h'\n"), 0644)
	var out, stderr bytes.Buffer
	err := initAt([]string{"-worktree", root}, &out, &stderr)
	if err == nil || !strings.Contains(err.Error(), "[test] verify_timeout is 100h0m0s, more than the 8h0m0s km init uses by itself: pass -timeout to allow it") || strings.Contains(out.String(), "passed") {
		t.Fatal(err, out.String())
	}
	if out, err := initCall(t, root, "-timeout", "10s"); err != nil || !strings.Contains(out, "passed") {
		t.Fatal(out, err)
	}
}

func TestProveNamesTheBreakTimeoutWhenABreakRunIsWhatTimedOut(t *testing.T) {
	root := suiteFixture(t)
	proofWrite(t, root, "suites/slow.sh", "sleep 1.8\n", 0644)
	proofGit(t, root, "add", ".")
	proofGit(t, root, "-c", "user.name=t", "-c", "user.email=t@example.invalid", "commit", "-qm", "slower")
	proofWrite(t, root, initrepo.ScriptFile, suiteScript("slow"), 0755)
	proofWrite(t, root, loop.ConfigFile, suiteConfig(""), 0644)
	hang := suitePatch(t, "hang.patch", "diff --git a/suites/slow.sh b/suites/slow.sh\n--- a/suites/slow.sh\n+++ b/suites/slow.sh\n@@ -1 +1 @@\n-sleep 1.8\n+sleep 30\n")
	report := proofJSON(t, root, "-timeout", "3s", "-break", hang)
	limit := time.Duration(report.BreakTimeoutMS) * time.Millisecond
	if report.ExitStatus != 3 || limit <= 3*time.Second || !strings.Contains(report.Reason, "verify timed out at "+limit.String()+", the break timeout; pass -timeout D") {
		t.Fatal(limit, report)
	}
}

func TestTheTuneWriterIsToldToRunVerifyOnceAndLeaveTheProofToKmWhileTheGuideStillSaysToProve(t *testing.T) {
	root := suiteFixture(t)
	f := &tuneCAO{writer: suiteWriter("", "fast", "slow")}
	limitsTimeout(t)
	tuneCall(t, "verify", "-worktree", root, "-cao", f.serve(t).URL)
	if len(f.prompts) == 0 {
		t.Fatal("no prompt")
	}
	writer := f.prompts[0]
	if !strings.Contains(writer, "4. Run scripts/verify once to see it pass. Do not run `km prove` yourself: km runs it with your patches after this step, where it has the time, and sends the result back when it is not proved.") || strings.Contains(writer, "until it reports proved") {
		t.Fatal(writer)
	}
	if _, guide := tuneCall(t, "verify", "-guide"); !strings.Contains(guide, "4. Run `km prove` with those patches until it reports proved.") {
		t.Fatal(guide)
	}
}

func TestInitUsesTheSavedVerifyTimeoutAndNamesTheFlagWhenVerifyTimesOut(t *testing.T) {
	root := suiteFixture(t)
	proofWrite(t, root, initrepo.ScriptFile, suiteScript("slow"), 0755)
	proofWrite(t, root, loop.ConfigFile, suiteConfig("verify_timeout = '100ms'\n"), 0644)
	out, err := initCall(t, root)
	if err == nil || !strings.Contains(out, "timed out after 100ms") || !strings.Contains(out, "pass -timeout D, or set [test] verify_timeout, to give verify longer") {
		t.Fatal(out, err)
	}
	if out, err := initCall(t, root, "-timeout", "10s"); err != nil {
		t.Fatal(out, err)
	}
}

func TestProveNamesTheLimitAndHowToRaiseItWhenAVerifyRunTimesOut(t *testing.T) {
	root := suiteFixture(t)
	proofWrite(t, root, initrepo.ScriptFile, suiteScript("slow"), 0755)
	proofWrite(t, root, loop.ConfigFile, suiteConfig(""), 0644)
	report := proofJSON(t, root, "-timeout", "100ms")
	if report.ExitStatus != 3 || !strings.Contains(report.Reason, "clean run 1: verify timed out at 100ms; pass -timeout D, or set [test] verify_timeout, to give verify longer") {
		t.Fatal(report)
	}
}
