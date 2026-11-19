package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jystringfellow/kapellmeister/internal/initrepo"
	"github.com/jystringfellow/kapellmeister/internal/loop"
	"github.com/jystringfellow/kapellmeister/internal/prove"
)

const offlineSteps = "run 'fast' 'sh suites/fast.sh'\nrun 'network' 'sh suites/network.sh'\nrun 'loopback' 'sh suites/loopback.sh'\nrun 'slow' 'sh suites/slow.sh'\nrun 'again' 'sh suites/network.sh'\n"

func offlineFixture(t *testing.T, steps string) string {
	t.Helper()
	root := suiteFixture(t)
	proofWrite(t, root, initrepo.ScriptFile, suiteSteps(steps), 0755)
	proofWrite(t, root, loop.ConfigFile, suiteConfig(""), 0644)
	return root
}

func offlineSuites(result prove.OfflineResult) string {
	var suites []string
	for _, failed := range result.Failed {
		suites = append(suites, failed.Suite)
	}
	return strings.Join(suites, ",")
}

func TestTheFixturesNetworkSuitePassesWithNetworkSoOnlyTheMissingNetworkMakesItFail(t *testing.T) {
	root := offlineFixture(t, offlineSteps)
	if report := proofJSON(t, root); report.ExitStatus != 11 {
		t.Fatal(report)
	}
}

func TestARunWithoutNetworkNamesEverySuiteThatNeedsARouteOffTheMachineAndKeepsOneThatOpensItsOwnLoopbackListener(t *testing.T) {
	root := offlineFixture(t, offlineSteps)
	for _, method := range []string{"", prove.MethodProxy} {
		result := prove.Offline(t.Context(), prove.OfflineOptions{Worktree: root, Method: method})
		if result.Status != "failed" || result.Partial || offlineSuites(result) != "network,again" {
			t.Fatal(method, result)
		}
		if method == "" && result.Method != prove.NoNetworkMethod(t.Context()) || method != "" && result.Method != method {
			t.Fatal(method, result.Method)
		}
		for _, failed := range result.Failed {
			if !strings.HasPrefix(failed.Evidence, "exit 7: no route to a non-loopback address: ") {
				t.Fatal(failed)
			}
		}
	}
}

func TestARunWithoutNetworkPassesWhenNoSuiteNeedsNetwork(t *testing.T) {
	root := offlineFixture(t, "run 'fast' 'sh suites/fast.sh'\nrun 'loopback' 'sh suites/loopback.sh'\nrun 'slow' 'sh suites/slow.sh'\n")
	if result := prove.Offline(t.Context(), prove.OfflineOptions{Worktree: root}); result.Status != "passed" || len(result.Failed) != 0 || result.Runs != 1 {
		t.Fatal(result)
	}
}

func TestAStepThatFailsOnceWithoutNetworkAndPassesWhenRunAgainWithoutItIsFlakyAndNotExcluded(t *testing.T) {
	root := offlineFixture(t, "run 'fast' 'sh suites/fast.sh'\nrun 'once' 'sh suites/once.sh'\n")
	t.Setenv("SUITE_ONCE_STATE", filepath.Join(t.TempDir(), "ran"))
	result := prove.Offline(t.Context(), prove.OfflineOptions{Worktree: root})
	if result.Status != "flaky" || len(result.Failed) != 0 || result.Runs != 2 {
		t.Fatal(result)
	}
	if want := `step "once" failed once without network (exit 1: unrelated failure) and passed when run again without it: flaky, not a suite that needs network`; result.Reason != want {
		t.Fatal(result.Reason)
	}
}

func TestAStepThatFailsWithoutNetworkAndAgainWithItIsAFailureAndNotExcluded(t *testing.T) {
	root := offlineFixture(t, "run 'fast' 'sh suites/fast.sh'\nrun 'broken' 'sh suites/broken.sh'\n")
	result := prove.Offline(t.Context(), prove.OfflineOptions{Worktree: root})
	if result.Status != "failure" || len(result.Failed) != 0 || result.Runs != 3 {
		t.Fatal(result)
	}
	if want := `step "broken" failed twice without network (exit 3: always fails) and failed again with it: not a suite that needs network`; result.Reason != want {
		t.Fatal(result.Reason)
	}
}

func TestAWrapperThatCannotSkipAStepYieldsTheFirstFailingSuiteAndSaysTheResultIsPartial(t *testing.T) {
	root := offlineFixture(t, offlineSteps)
	script := initRead(t, root, initrepo.ScriptFile)
	var kept []string
	for _, line := range strings.Split(script, "\n") {
		if !strings.Contains(line, initrepo.SkipEnv) {
			kept = append(kept, line)
		}
	}
	if len(kept) == strings.Count(script, "\n")+1 {
		t.Fatal("the wrapper has no skip line to remove")
	}
	proofWrite(t, root, initrepo.ScriptFile, strings.Join(kept, "\n"), 0755)
	result := prove.Offline(t.Context(), prove.OfflineOptions{Worktree: root})
	if result.Status != "failed" || !result.Partial || offlineSuites(result) != "network" {
		t.Fatal(result)
	}
}

func TestAStepThatHangsWithoutNetworkIsNamedWithTheTimeoutAsEvidenceWhenTheStepOrderIsKnown(t *testing.T) {
	root := offlineFixture(t, "run 'fast' 'sh suites/fast.sh'\nrun 'hang' 'sh suites/hang.sh'\nrun 'slow' 'sh suites/slow.sh'\n")
	result := prove.Offline(t.Context(), prove.OfflineOptions{Worktree: root, Timeout: 3 * time.Second, Labels: []string{"fast", "hang", "slow"}})
	if result.Status != "failed" || offlineSuites(result) != "hang" || result.Failed[0].Evidence != "did not finish within 3s" || result.Runs != 4 {
		t.Fatal(result)
	}
	result = prove.Offline(t.Context(), prove.OfflineOptions{Worktree: root, Timeout: 3 * time.Second})
	if result.Status != "timeout" || result.Reason != "verify timed out without network after 3s and km could not tell which step was running" || len(result.Failed) != 0 {
		t.Fatal(result)
	}
}

func TestTheNetworkIsCutByTheMethodThisMachineIsExpectedToOffer(t *testing.T) {
	want := os.Getenv("KAPELLMEISTER_TEST_NO_NETWORK_METHOD")
	got := prove.NoNetworkMethod(t.Context())
	if want == "" {
		t.Skipf("the method here is %q; set KAPELLMEISTER_TEST_NO_NETWORK_METHOD to require one", got)
	}
	if got != want {
		t.Fatalf("the network is cut by %q, want %q", got, want)
	}
	root := offlineFixture(t, offlineSteps)
	if result := prove.Offline(t.Context(), prove.OfflineOptions{Worktree: root}); result.Method != want || result.Status != "failed" || offlineSuites(result) != "network,again" {
		t.Fatal(result)
	}
}

func TestAMachineThatDoesNotPermitTheIsolationToolFallsBackToProxyVariablesAndSaysSo(t *testing.T) {
	bin := t.TempDir()
	for _, tool := range []string{"unshare", "sandbox-exec"} {
		proofWrite(t, bin, tool, "#!/bin/sh\necho 'operation not permitted' >&2\nexit 1\n", 0755)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	if method := prove.NoNetworkMethod(t.Context()); method != prove.MethodProxy {
		t.Fatal(method)
	}
	root := offlineFixture(t, offlineSteps)
	if result := prove.Offline(t.Context(), prove.OfflineOptions{Worktree: root}); result.Method != prove.MethodProxy || result.Status != "failed" || offlineSuites(result) != "network,again" {
		t.Fatal(result)
	}
}

func TestInitProveAndRunIgnoreASkipFileSoNoStepIsSkippedOutsideTheNetworkTest(t *testing.T) {
	root := offlineFixture(t, "run 'unit' 'sh suites/unit.sh'\n")
	skip := t.TempDir() + "/skip"
	writeFile(t, skip, "unit\n")
	t.Setenv(initrepo.SkipEnv, skip)
	if report := proofJSON(t, root); report.ExitStatus != 12 {
		t.Fatal(report)
	}
	if result, err := loop.CommandVerifier("scripts/verify")(t.Context(), root); err != nil || result.ExitCode == 0 {
		t.Fatal(result, err)
	}
	if out, err := initCall(t, root, "-timeout", "30s"); err == nil || strings.Contains(out, "verify skipped step") {
		t.Fatal(out, err)
	}
}

func TestProveRefusesAVerifyThatSetsTheSkipVariableItself(t *testing.T) {
	root := offlineFixture(t, "run 'unit' 'sh suites/unit.sh'\n")
	skip := t.TempDir() + "/skip"
	writeFile(t, skip, "unit\n")
	const want = "verify must not set KAPELLMEISTER_VERIFY_SKIP: only km sets it, for its own run without network"
	proofWrite(t, root, loop.ConfigFile, initrepo.RenderConfig(initrepo.SkipEnv+"="+skip+" scripts/verify", ""), 0644)
	if report := proofJSON(t, root); report.ExitStatus != 10 || report.Reason != want {
		t.Fatal(report)
	}
	proofWrite(t, root, loop.ConfigFile, suiteConfig(""), 0644)
	proofWrite(t, root, initrepo.ScriptFile, strings.Replace(suiteScript("unit"), "\nrun() {", "\n: \"${"+initrepo.SkipEnv+":="+skip+"}\"\nrun() {", 1), 0755)
	if report := proofJSON(t, root); report.ExitStatus != 10 || report.Reason != want {
		t.Fatal(report)
	}
}

func TestAStepSeesTheSkipVariableInAProofRunTooSoItCannotTellTheRunWithoutNetworkByIt(t *testing.T) {
	root := offlineFixture(t, "run 'probe' 'test -f \"$"+initrepo.SkipEnv+"\" && test ! -s \"$"+initrepo.SkipEnv+"\"'\n")
	if report := proofJSON(t, root); report.ExitStatus != 11 {
		t.Fatal(report)
	}
	if result := prove.Offline(t.Context(), prove.OfflineOptions{Worktree: root}); result.Status != "passed" {
		t.Fatal(result)
	}
}
