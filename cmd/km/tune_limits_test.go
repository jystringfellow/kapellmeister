package main

import (
	"context"
	"strings"
	"testing"

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

const limitsDerived = "budget 3h10m0s: a whole proof at the 45m0s verify timeout is two clean runs and 2 break runs (3h0m0s), plus 10m0s for the chairs; pass -budget to set it yourself\n"

func TestTuneSizesItsDefaultBudgetFromTheVerifyTimeoutFlagAndSaysHowBeforeSeatingAnyChair(t *testing.T) {
	root := suiteFixture(t)
	f := &tuneCAO{writer: suiteWriter("", "fast", "slow")}
	calls := limitsTimeout(t)
	code, out := tuneCall(t, "verify", "-worktree", root, "-cao", f.serve(t).URL, "-verify-timeout", "45m")
	if code == 0 || !strings.HasPrefix(out, limitsDerived) {
		t.Fatal(code, out)
	}
	if *calls != 1 || !strings.Contains(out, "and 2h25m0s of the budget is left") {
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

func TestTheDefaultBudgetStaysThirtyMinutesAtTheDefaultVerifyTimeoutAndCountsOneBreakPerRequiredSuite(t *testing.T) {
	root := suiteFixture(t)
	f := &tuneCAO{writer: suiteWriter("", "fast", "slow")}
	limitsTimeout(t)
	if _, out := tuneCall(t, "verify", "-worktree", root, "-cao", f.serve(t).URL); !strings.Contains(out, "budget 30m0s: a whole proof at the 5m0s verify timeout is two clean runs and 2 break runs (20m0s), plus 10m0s for the chairs; pass -budget to set it yourself\n") {
		t.Fatal(out)
	}
	if _, out := tuneCall(t, "verify", "-worktree", root, "-cao", f.serve(t).URL, "-require", "fast", "-require", "slow", "-require", "unit"); !strings.Contains(out, "budget 35m0s: a whole proof at the 5m0s verify timeout is two clean runs and 3 break runs (25m0s), plus 10m0s for the chairs") {
		t.Fatal(out)
	}
}

func TestAnExplicitBudgetIsKeptAndTuneSaysUpFrontWhetherAWholeProofFitsItAndWhatWould(t *testing.T) {
	root := suiteFixture(t)
	f := &tuneCAO{writer: suiteWriter("", "fast", "slow")}
	calls := limitsTimeout(t)
	code, out := tuneCall(t, "verify", "-worktree", root, "-cao", f.serve(t).URL, "-verify-timeout", "45m", "-budget", "50m")
	short := strings.HasPrefix(out, "-budget 50m0s does not hold a whole proof at the 45m0s verify timeout: two clean runs and 2 break runs can take 3h0m0s, plus 10m0s for the chairs; -budget 3h10m0s would\n")
	if code == 0 || !short || *calls != 1 || !strings.Contains(out, "and 5m0s of the budget is left") {
		t.Fatal(code, *calls, out)
	}
	if _, out := tuneCall(t, "verify", "-worktree", root, "-cao", f.serve(t).URL, "-verify-timeout", "45m", "-budget", "4h"); !strings.Contains(out, "-budget 4h0m0s holds a whole proof at the 45m0s verify timeout: two clean runs and 2 break runs can take 3h0m0s, plus 10m0s for the chairs\n") {
		t.Fatal(out)
	}
}

func TestATuneAtTheDefaultVerifyTimeoutStopsAfterOneCleanRunTimeoutAndNamesTheFlag(t *testing.T) {
	root := suiteFixture(t)
	f := &tuneCAO{writer: suiteWriter("", "fast", "slow")}
	calls := limitsTimeout(t)
	code, out := tuneCall(t, "verify", "-worktree", root, "-cao", f.serve(t).URL, "-budget", "10h")
	if code == 0 || *calls != 1 || strings.Contains(out, "raised") {
		t.Fatal(code, *calls, out)
	}
	if !strings.Contains(out, "clean run 1: verify timed out; a verify is never narrowed for time, and 5m0s is only the default limit: pass -verify-timeout with the time one verify run needs, and km sizes the budget from it and saves the limit") {
		t.Fatal(out)
	}
	if !initrepo.IsStub(initRead(t, root, initrepo.ScriptFile)) || strings.Contains(initRead(t, root, loop.ConfigFile), "verify_timeout") {
		t.Fatal(initRead(t, root, loop.ConfigFile))
	}
}

func TestWhenARaiseDoesNotFitTuneNamesTheVerifyTimeoutAndBudgetThatWould(t *testing.T) {
	root := suiteFixture(t)
	f := &tuneCAO{writer: suiteWriter("", "fast", "slow")}
	limitsTimeout(t)
	_, out := tuneCall(t, "verify", "-worktree", root, "-cao", f.serve(t).URL, "-verify-timeout", "45m")
	if !strings.Contains(out, "the timeout is not raised to 1h30m0s: a proof at that limit can take 7h30m0s (two clean runs and 3 break runs) and 2h25m0s of the budget is left: pass -verify-timeout 1h30m0s and -budget 8h15m0s or more") {
		t.Fatal(out)
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
