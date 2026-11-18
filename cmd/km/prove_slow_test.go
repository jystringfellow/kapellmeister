package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jystringfellow/kapellmeister/internal/initrepo"
	"github.com/jystringfellow/kapellmeister/internal/loop"
	"github.com/jystringfellow/kapellmeister/internal/prove"
)

func slowProof(t *testing.T, root string, args ...string) prove.Result {
	t.Helper()
	result := prove.Prove(t.Context(), prove.Options{Worktree: root, Breaks: args})
	return result
}

func suitePatch(t *testing.T, name, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestProveNamesTheStepThatFailedOnOneCleanCopyAndPassedOnTheOtherAsFlaky(t *testing.T) {
	root := suiteFixture(t)
	t.Setenv("SUITE_FLAKY_STATE", filepath.Join(t.TempDir(), "ran"))
	proofWrite(t, root, initrepo.ScriptFile, suiteScript("fast", "flaky", "slow"), 0755)
	proofWrite(t, root, loop.ConfigFile, suiteConfig(""), 0644)
	result := slowProof(t, root)
	if result.Status != "flaky" || result.ExitStatus != 12 || len(result.Flaky) != 1 {
		t.Fatal(result)
	}
	flaky := result.Flaky[0]
	if flaky.Step != "flaky" || flaky.FailedIn != "clean run 1" || flaky.PassedIn != "clean run 2" || !strings.Contains(flaky.Output, "verify failed at step: flaky") {
		t.Fatal(flaky)
	}
	if want := `flaky, not a failed proof: step "flaky" failed in clean run 1 and passed in clean run 2`; !strings.Contains(result.Reason, want) {
		t.Fatal(result.Reason)
	}
	if err := os.Remove(os.Getenv("SUITE_FLAKY_STATE")); err != nil {
		t.Fatal(err)
	}
	if code, out := proofCall(t, root); code != 12 || !strings.Contains(out, "flaky step: flaky (failed in clean run 1, passed in clean run 2)") {
		t.Fatal(code, out)
	}
}

func TestProveReportsAStepThatFailsOnBothCleanCopiesAsAFailedProofAndNotAsFlaky(t *testing.T) {
	root := suiteFixture(t)
	proofWrite(t, root, initrepo.ScriptFile, suiteScript("fast", "unit"), 0755)
	proofWrite(t, root, loop.ConfigFile, suiteConfig(""), 0644)
	result := slowProof(t, root)
	if result.Status != "failed" || result.ExitStatus != 12 || len(result.Flaky) != 0 || !strings.Contains(result.Reason, `verify failed on both clean copies at step "unit"`) {
		t.Fatal(result)
	}
}

const suiteUnbroken = `required suites with no break patch that fails in their step: %s; each needs a break patch that its step is the first to catch`

func TestProveRefusesAHollowedOutRequiredSuiteBecauseNoBreakFailsInItsStep(t *testing.T) {
	root := suiteFixture(t)
	proofWrite(t, root, initrepo.ScriptFile, suiteSteps("run 'fast' 'sh suites/fast.sh'\nrun 'slow' 'true'\n"), 0755)
	proofWrite(t, root, loop.ConfigFile, suiteConfig(suiteRequired), 0644)
	result := slowProof(t, root, suitePatch(t, "source.patch", suiteSourceBreak), suitePatch(t, "test.patch", suiteTestBreak))
	if result.Status != "suite_unbroken" || result.ExitStatus != 15 || !strings.Contains(result.Reason, fmt.Sprintf(suiteUnbroken, `"slow"`)) {
		t.Fatal(result)
	}
}

func TestProveNeedsABreakInEachOfTwoRequiredSuitesThatTakeTheSameTimeWhicheverIsListedFirst(t *testing.T) {
	for _, required := range []string{"['fast', 'again']", "['again', 'fast']"} {
		root := suiteFixture(t)
		proofWrite(t, root, initrepo.ScriptFile, suiteSteps("run 'fast' 'sh suites/fast.sh'\nrun 'again' 'sh suites/fast.sh'\n"), 0755)
		proofWrite(t, root, loop.ConfigFile, suiteConfig("verify_required = "+required+"\n"), 0644)
		result := slowProof(t, root, suitePatch(t, "source.patch", suiteSourceBreak), suitePatch(t, "test.patch", suiteTestBreak))
		if result.Status != "suite_unbroken" || !strings.Contains(result.Reason, fmt.Sprintf(suiteUnbroken, `"again"`)) {
			t.Fatal(required, result)
		}
	}
}

func TestProveMeasuresEachStepAndNeedsABreakThatFailsInEveryRequiredSuite(t *testing.T) {
	root := suiteFixture(t)
	proofWrite(t, root, initrepo.ScriptFile, suiteScript("fast", "slow"), 0755)
	proofWrite(t, root, loop.ConfigFile, suiteConfig(suiteRequired), 0644)
	source, test, slowOnly := suitePatch(t, "source.patch", suiteSourceBreak), suitePatch(t, "test.patch", suiteTestBreak), suitePatch(t, "slow.patch", suiteSlowOnlyBreak)
	result := slowProof(t, root, source, test)
	if result.Status != "suite_unbroken" || result.ExitStatus != 15 || !strings.Contains(result.Reason, fmt.Sprintf(suiteUnbroken, `"slow"`)) {
		t.Fatal(result)
	}
	if result := slowProof(t, root, slowOnly); result.Status != "suite_unbroken" || !strings.Contains(result.Reason, fmt.Sprintf(suiteUnbroken, `"fast"`)) {
		t.Fatal(result)
	}
	for _, b := range result.Breaks {
		if b.FailedStep != "fast" {
			t.Fatal(b)
		}
	}
	steps := result.Runs[0].Steps
	if len(steps) != 2 || steps[0].Label != "fast" || steps[1].Label != "slow" || steps[1].DurationMS < 400 || steps[0].DurationMS >= steps[1].DurationMS {
		t.Fatal(steps)
	}
	result = slowProof(t, root, source, slowOnly)
	if result.Status != "proved" || result.Breaks[1].FailedStep != "slow" {
		t.Fatal(result)
	}
	proofWrite(t, root, loop.ConfigFile, suiteConfig(""), 0644)
	if result := slowProof(t, root, source, test); result.Status != "proved" {
		t.Fatal(result)
	}
}

func TestProveGivesEachBreakAtLeastTwiceTheSlowestCleanRunAndSaysSo(t *testing.T) {
	root := suiteFixture(t)
	proofWrite(t, root, "suites/slow.sh", "sleep 1.5\n. ./answer.sh\ntest \"$(answer)\" -eq 42 && test \"$(total)\" = 84\n", 0644)
	proofGit(t, root, "-c", "user.name=t", "-c", "user.email=t@example.invalid", "commit", "-qam", "slower")
	proofWrite(t, root, initrepo.ScriptFile, suiteScript("slow"), 0755)
	proofWrite(t, root, loop.ConfigFile, suiteConfig("verify_timeout = '2500ms'\n"), 0644)
	report := proofJSON(t, root, "-break", suitePatch(t, "source.patch", suiteSourceBreak))
	if report.ExitStatus != 0 {
		t.Fatal(report)
	}
	slowest := max(report.Runs[0].DurationMS, report.Runs[1].DurationMS)
	if report.BreakTimeoutMS != 2*slowest || report.BreakTimeoutMS < 3000 {
		t.Fatal(report.BreakTimeoutMS, slowest)
	}
	_, out := proofCall(t, root, "-break", suitePatch(t, "source.patch", suiteSourceBreak))
	if !strings.Contains(out, "break timeout raised from 2.5s to ") || !strings.Contains(out, ": twice the slowest clean run") {
		t.Fatal(out)
	}
	proofWrite(t, root, loop.ConfigFile, suiteConfig("verify_timeout = '1m'\n"), 0644)
	if report := proofJSON(t, root, "-break", suitePatch(t, "source.patch", suiteSourceBreak)); report.BreakTimeoutMS != 60000 {
		t.Fatal(report.BreakTimeoutMS)
	}
}
