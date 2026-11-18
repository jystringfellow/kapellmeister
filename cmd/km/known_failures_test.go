package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/jystringfellow/kapellmeister/internal/cao"
	"github.com/jystringfellow/kapellmeister/internal/initrepo"
	"github.com/jystringfellow/kapellmeister/internal/loop"
	"github.com/jystringfellow/kapellmeister/internal/prove"
)

const knownConfig = "verify_known_failures = ['legacy']\n"
const knownSteps = "run 'fast' 'sh suites/fast.sh'\nrun 'unit' 'sh suites/unit.sh test_answer'\nknown_failure 'legacy' 'sh suites/unit.sh test_legacy'\n"
const knownLegacyBreak = "diff --git a/answer.sh b/answer.sh\n--- a/answer.sh\n+++ b/answer.sh\n@@ -1,3 +1,3 @@\n answer() { echo 42; }\n-legacy() { echo 0; }\n+legacy() { echo 5; }\n total() { echo 84; }\n"

func knownProof(t *testing.T, root string, args ...string) (prove.Result, string) {
	t.Helper()
	_, text := proofCall(t, root, args...)
	_, out := proofCall(t, root, append(args, "-json")...)
	var result prove.Result
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatal(out, err)
	}
	return result, text
}

func knownBreaks(t *testing.T) []string {
	t.Helper()
	return []string{"-break", suitePatch(t, "source.patch", suiteSourceBreak), "-break", suitePatch(t, "test.patch", suiteTestBreak)}
}

func TestVerifyPassesWhileANamedKnownFailureFailsAndProveReportsTheBaselineByName(t *testing.T) {
	root := suiteFixture(t)
	proofWrite(t, root, initrepo.ScriptFile, suiteSteps("run 'fast' 'sh suites/fast.sh'\nrun 'unit' 'sh suites/unit.sh'\n"), 0755)
	proofWrite(t, root, loop.ConfigFile, suiteConfig(""), 0644)
	if result, _ := knownProof(t, root, knownBreaks(t)...); result.Status != "failed" {
		t.Fatal(result)
	}
	proofWrite(t, root, initrepo.ScriptFile, suiteSteps(knownSteps), 0755)
	proofWrite(t, root, loop.ConfigFile, suiteConfig(knownConfig), 0644)
	result, text := knownProof(t, root, knownBreaks(t)...)
	if result.Status != "proved" || len(result.KnownFailures) != 1 || result.KnownFailures[0] != "legacy" || len(result.KnownFailuresPassing) != 0 {
		t.Fatal(result)
	}
	if !strings.Contains(text, "known failures (verify passes while these fail): legacy\n") {
		t.Fatal(text)
	}
}

func TestProveSaysWhenAKnownFailureNowPassesSoItsEntryIsRemoved(t *testing.T) {
	root := suiteFixture(t)
	proofWrite(t, root, initrepo.ScriptFile, suiteSteps("run 'fast' 'sh suites/fast.sh'\nknown_failure 'legacy' 'sh suites/unit.sh test_answer'\n"), 0755)
	proofWrite(t, root, loop.ConfigFile, suiteConfig(knownConfig), 0644)
	result, text := knownProof(t, root, knownBreaks(t)...)
	if result.Status != "proved" || len(result.KnownFailures) != 0 || len(result.KnownFailuresPassing) != 1 || result.KnownFailuresPassing[0] != "legacy" {
		t.Fatal(result)
	}
	if !strings.Contains(text, "known failure \"legacy\" now passes: remove it from [test] verify_known_failures and make it a run step\n") {
		t.Fatal(text)
	}
}

func TestProveRefusesAKnownFailureListThatDoesNotMatchTheKnownFailureStepsThatRan(t *testing.T) {
	for name, c := range map[string]struct{ steps, config, want string }{
		"listed, no step": {
			"run 'fast' 'sh suites/fast.sh'\n", knownConfig,
			`known failure "legacy" is listed in [test] verify_known_failures but no known_failure step with that label ran in clean run 1; give it its own step in scripts/verify: known_failure 'legacy' 'COMMAND', or remove it from the list`,
		},
		"listed, an ordinary step": {
			"run 'fast' 'sh suites/fast.sh'\nrun 'legacy' 'sh suites/unit.sh test_answer'\n", knownConfig,
			`known failure "legacy" is listed in [test] verify_known_failures but no known_failure step with that label ran in clean run 1`,
		},
		"step, not listed": {
			knownSteps, "",
			`scripts/verify has a known_failure step "legacy" that [test] verify_known_failures does not list; list it there so the baseline is named, or make it a run step`,
		},
	} {
		root := suiteFixture(t)
		proofWrite(t, root, initrepo.ScriptFile, suiteSteps(c.steps), 0755)
		proofWrite(t, root, loop.ConfigFile, suiteConfig(c.config), 0644)
		result, _ := knownProof(t, root, knownBreaks(t)...)
		if result.Status != "baseline_mismatch" || result.ExitStatus != 16 || !strings.Contains(result.Reason, c.want) {
			t.Fatal(name, result)
		}
	}
}

func TestABreakThatOnlyAKnownFailureStepWouldCatchSurvives(t *testing.T) {
	root := suiteFixture(t)
	proofWrite(t, root, initrepo.ScriptFile, suiteSteps(knownSteps), 0755)
	proofWrite(t, root, loop.ConfigFile, suiteConfig(knownConfig), 0644)
	result, _ := knownProof(t, root, "-break", suitePatch(t, "legacy.patch", knownLegacyBreak))
	if result.Status != "survived" || result.ExitStatus != 13 || result.Breaks[0].FailedStep != "" {
		t.Fatal(result)
	}
}

func TestAKnownFailureCannotAlsoBeARequiredSuiteAndItsNameMustBeUsableAsAStepLabel(t *testing.T) {
	for config, want := range map[string]string{
		"verify_required = ['legacy']\n" + knownConfig:        `test.verify_known_failures: "legacy" is also in verify_required: a required suite cannot be expected to fail`,
		"verify_known_failures = ['a', 'a']\n":                `test.verify_known_failures: suite "a" is repeated`,
		"verify_known_failures = [\"it's\"]\n":                "test.verify_known_failures:",
		"verify_known_failures = ['']\n":                      "test.verify_known_failures:",
		"verify_known_failures = ['two words', 'passed x']\n": "",
	} {
		root := suiteFixture(t)
		proofWrite(t, root, initrepo.ScriptFile, suiteSteps(knownSteps), 0755)
		proofWrite(t, root, loop.ConfigFile, suiteConfig(config), 0644)
		code, out := proofCall(t, root)
		if want == "" {
			if code == 10 && strings.Contains(out, "test.verify_known_failures") {
				t.Fatal(config, out)
			}
			continue
		}
		if code != 10 || !strings.Contains(out, want) {
			t.Fatal(config, code, out)
		}
	}
}

func TestTuneReportsTheKnownFailureBaselineByNameInItsSummaryAndTellsTheWriterHowToRecordOne(t *testing.T) {
	root := suiteFixture(t)
	f := &tuneCAO{writer: func(t *testing.T, req cao.StepRequest, patchDir string) string {
		proofWrite(t, req.WorkingDirectory, initrepo.ScriptFile, suiteSteps(knownSteps), 0755)
		proofWrite(t, req.WorkingDirectory, loop.ConfigFile, suiteConfig(knownConfig), 0644)
		return suiteReply(t, patchDir, "sh suites/fast.sh && sh suites/unit.sh test_answer")
	}}
	code, out := tuneCall(t, "verify", "-worktree", root, "-cao", f.serve(t).URL)
	if code != 0 || strings.Count(out, "known failures (verify passes while these fail): legacy\n") < 2 {
		t.Fatal(code, out)
	}
	if !strings.Contains(initRead(t, root, loop.ConfigFile), knownConfig) || len(f.prompts) == 0 || !strings.Contains(f.prompts[0], "known_failure 'LABEL' 'COMMAND'") || !strings.Contains(f.prompts[0], "[test] verify_known_failures") {
		t.Fatal(out)
	}
}

func TestARunsVerifierReportsEachKnownFailureStepAndWhetherItPassed(t *testing.T) {
	root := suiteFixture(t)
	proofWrite(t, root, initrepo.ScriptFile, suiteSteps("known_failure 'legacy' 'sh suites/unit.sh test_legacy'\nknown_failure 'fixed' 'sh suites/unit.sh test_answer'\n"), 0755)
	result, err := loop.CommandVerifier("scripts/verify")(context.Background(), root)
	if err != nil || result.ExitCode != 0 || len(result.Known) != 2 {
		t.Fatal(result, err)
	}
	if result.Known[0] != (loop.KnownFailure{Label: "legacy"}) || result.Known[1] != (loop.KnownFailure{Label: "fixed", Passed: true}) {
		t.Fatal(result.Known)
	}
}

func TestTheFinalRunSummaryNamesTheKnownFailureBaselineAndAnyThatNowPass(t *testing.T) {
	var out bytes.Buffer
	sum := ensembleSummary{Result: loop.Result{Outcome: loop.Approved, Rounds: 1, KnownFailures: []string{"legacy", "old export"}, KnownFailuresPassing: []string{"fixed"}}}
	if err := finishRun(&out, sum); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"  known failures (verify passes while these fail): legacy, old export\n", "  known failure \"fixed\" now passes: remove it from [test] verify_known_failures and make it a run step\n"} {
		if !strings.Contains(out.String(), want) {
			t.Fatal(out.String())
		}
	}
	out.Reset()
	_ = finishRun(&out, ensembleSummary{Result: loop.Result{Outcome: loop.Approved, Rounds: 1}})
	if strings.Contains(out.String(), "known failure") {
		t.Fatal(out.String())
	}
}
