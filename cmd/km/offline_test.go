package main

import (
	"strings"
	"testing"

	"github.com/jystringfellow/kapellmeister/internal/initrepo"
	"github.com/jystringfellow/kapellmeister/internal/loop"
	"github.com/jystringfellow/kapellmeister/internal/prove"
)

const offlineSteps = "run 'fast' 'sh suites/fast.sh'\nrun 'network' 'sh suites/network.sh'\nrun 'slow' 'sh suites/slow.sh'\nrun 'again' 'sh suites/network.sh'\n"

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

func TestARunWithoutNetworkNamesEverySuiteThatFailsWithoutItInOnePassWithEvidence(t *testing.T) {
	root := offlineFixture(t, offlineSteps)
	for _, method := range []string{"", prove.MethodProxy} {
		result := prove.Offline(t.Context(), prove.OfflineOptions{Worktree: root, Method: method})
		if result.Status != "failed" || result.Partial || offlineSuites(result) != "network,again" || result.Runs != 3 {
			t.Fatal(method, result)
		}
		if method == "" && result.Method != prove.NoNetworkMethod(t.Context()) || method != "" && result.Method != method {
			t.Fatal(method, result.Method)
		}
		for _, failed := range result.Failed {
			if !strings.HasPrefix(failed.Evidence, "exit ") || !strings.Contains(failed.Evidence, ": ") {
				t.Fatal(failed)
			}
		}
	}
}

func TestARunWithoutNetworkPassesWhenNoSuiteNeedsNetwork(t *testing.T) {
	root := offlineFixture(t, "run 'fast' 'sh suites/fast.sh'\nrun 'slow' 'sh suites/slow.sh'\n")
	if result := prove.Offline(t.Context(), prove.OfflineOptions{Worktree: root}); result.Status != "passed" || len(result.Failed) != 0 || result.Runs != 1 {
		t.Fatal(result)
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

func TestProveAndRunIgnoreASkipFileSoNoStepIsSkippedOutsideTheNetworkTest(t *testing.T) {
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
}
