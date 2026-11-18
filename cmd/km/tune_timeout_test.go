package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jystringfellow/kapellmeister/internal/initrepo"
	"github.com/jystringfellow/kapellmeister/internal/loop"
	"github.com/jystringfellow/kapellmeister/internal/prove"
)

func TestTuneStopsRaisingTheVerifyTimeoutWhenAWholeProofAtTheNextLimitDoesNotFitTheBudgetLeft(t *testing.T) {
	root := suiteFixture(t)
	f := &tuneCAO{writer: suiteWriter("", "fast", "slow")}
	now := suiteClock(t)
	old := runTuneProof
	defer func() { runTuneProof = old }()
	var limits []time.Duration
	runTuneProof = func(_ context.Context, opts prove.Options) prove.Result {
		limits = append(limits, opts.Timeout)
		*now = now.Add(opts.Timeout)
		return prove.Result{Status: "timeout", ExitStatus: 3, Reason: "clean run 1: verify timed out", Runs: []prove.RunResult{{Name: "clean run 1", Status: "timed_out"}}}
	}
	code, out := tuneCall(t, "verify", "-worktree", root, "-cao", f.serve(t).URL, "-verify-timeout", "5m", "-budget", "70m")
	if code == 0 || len(limits) != 2 || limits[0] != 5*time.Minute || limits[1] != 10*time.Minute {
		t.Fatal(code, limits, out)
	}
	if !strings.Contains(out, "verify timed out at 5m0s; a verify is never narrowed for time, so the timeout is raised to 10m0s and saved as [test] verify_timeout") {
		t.Fatal(out)
	}
	if !strings.Contains(out, "the timeout is not raised to 20m0s: a proof at that limit can take 1h40m0s (two clean runs and 3 break runs) and 55m0s of the budget is left: pass -verify-timeout 20m0s and -budget 1h55m0s or more") || !initrepo.IsStub(initRead(t, root, initrepo.ScriptFile)) {
		t.Fatal(out)
	}
}

func TestTuneDoesNotRaiseTheVerifyTimeoutWhenTheBudgetHoldsOneRunAtTheNextLimitButNotAWholeProof(t *testing.T) {
	root := suiteFixture(t)
	f := &tuneCAO{writer: suiteWriter("", "fast", "slow")}
	now := suiteClock(t)
	old := runTuneProof
	defer func() { runTuneProof = old }()
	calls := 0
	runTuneProof = func(_ context.Context, opts prove.Options) prove.Result {
		calls++
		*now = now.Add(opts.Timeout)
		return prove.Result{Status: "timeout", ExitStatus: 3, Reason: "clean run 1: verify timed out", Runs: []prove.RunResult{{Name: "clean run 1", Status: "timed_out"}}}
	}
	code, out := tuneCall(t, "verify", "-worktree", root, "-cao", f.serve(t).URL, "-verify-timeout", "5m")
	if code == 0 || calls != 1 || !strings.Contains(out, "the timeout is not raised to 10m0s: a proof at that limit can take 50m0s (two clean runs and 3 break runs) and 25m0s of the budget is left") {
		t.Fatal(code, calls, out)
	}
	if strings.Contains(initRead(t, root, loop.ConfigFile), "verify_timeout") {
		t.Fatal(initRead(t, root, loop.ConfigFile))
	}
}

func TestTuneRefusesAVerifyTimeoutTheWriterSetAndPublishesNothing(t *testing.T) {
	root := suiteFixture(t)
	f := &tuneCAO{writer: suiteWriter("verify_timeout = '300ms'\n", "fast", "slow")}
	code, out := tuneCall(t, "verify", "-worktree", root, "-cao", f.serve(t).URL)
	if code == 0 || !strings.Contains(out, "[test] verify_timeout must not be set by the tune: km saves the limit it proved at") {
		t.Fatal(code, out)
	}
	if !initrepo.IsStub(initRead(t, root, initrepo.ScriptFile)) || strings.Contains(initRead(t, root, loop.ConfigFile), "verify_timeout") {
		t.Fatal(initRead(t, root, loop.ConfigFile))
	}
}

func TestTuneRefusesARoundThatChangesTheVerifyTimeoutTheCallerSaved(t *testing.T) {
	root := suiteFixture(t)
	proofWrite(t, root, loop.ConfigFile, suiteConfig("verify_timeout = '30s'\n"), 0644)
	f := &tuneCAO{writer: suiteWriter("verify_timeout = '300ms'\n", "fast", "slow")}
	code, out := tuneCall(t, "verify", "-worktree", root, "-cao", f.serve(t).URL)
	if code == 0 || !strings.Contains(out, `[test] verify_timeout must stay "30s": only the caller and km change it`) {
		t.Fatal(code, out)
	}
	if !strings.Contains(initRead(t, root, loop.ConfigFile), "verify_timeout = '30s'") {
		t.Fatal(initRead(t, root, loop.ConfigFile))
	}
}

func TestTuneSavesAVerifyTimeoutGivenByFlagOnlyWhenALaterProveWouldUseAShorterOne(t *testing.T) {
	for limit, saved := range map[string]bool{"11m": true, "9m": false} {
		root := suiteFixture(t)
		f := &tuneCAO{writer: suiteWriter("", "fast", "slow")}
		code, out := tuneCall(t, "verify", "-worktree", root, "-cao", f.serve(t).URL, "-verify-timeout", limit)
		config := initRead(t, root, loop.ConfigFile)
		if code != 0 || strings.Contains(config, `verify_timeout = "11m0s"`) != saved || strings.Contains(config, "verify_timeout") != saved {
			t.Fatal(limit, code, config, out)
		}
		if saved != strings.Contains(out, "verify is proved at 11m0s, longer than the 10m0s a later km prove would use, so it is saved as [test] verify_timeout") {
			t.Fatal(limit, out)
		}
	}
}
