package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
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

func TestProveRefusesAListedKnownFailureThatPassesInBothCleanRunsAndNamesTheEntryToRemove(t *testing.T) {
	root := suiteFixture(t)
	proofWrite(t, root, initrepo.ScriptFile, suiteSteps("run 'fast' 'sh suites/fast.sh'\nknown_failure 'legacy' 'sh suites/unit.sh test_answer'\n"), 0755)
	proofWrite(t, root, loop.ConfigFile, suiteConfig(knownConfig), 0644)
	result, text := knownProof(t, root, knownBreaks(t)...)
	if result.Status != "baseline_mismatch" || result.ExitStatus != 16 || len(result.KnownFailures) != 0 || len(result.KnownFailuresPassing) != 1 || result.KnownFailuresPassing[0] != "legacy" {
		t.Fatal(result)
	}
	const want = `known failure "legacy" passed in both clean runs: remove "legacy" from [test] verify_known_failures and make its step a run step`
	if !strings.Contains(result.Reason, want) || !strings.Contains(text, want) {
		t.Fatal(result.Reason, text)
	}
}

func TestProveNamesAKnownFailureThatFailsInOneCleanRunAndPassesInTheOtherAsFlaky(t *testing.T) {
	root := suiteFixture(t)
	t.Setenv("SUITE_FLAKY_STATE", filepath.Join(t.TempDir(), "state"))
	proofWrite(t, root, initrepo.ScriptFile, suiteSteps("run 'fast' 'sh suites/fast.sh'\nrun 'unit' 'sh suites/unit.sh test_answer'\nknown_failure 'legacy' 'sh suites/flaky.sh'\n"), 0755)
	proofWrite(t, root, loop.ConfigFile, suiteConfig(knownConfig), 0644)
	_, text := proofCall(t, root, knownBreaks(t)...)
	if err := os.Remove(os.Getenv("SUITE_FLAKY_STATE")); err != nil {
		t.Fatal(err)
	}
	_, out := proofCall(t, root, append(knownBreaks(t), "-json")...)
	var result prove.Result
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatal(out, err)
	}
	if result.Status != "proved" || len(result.KnownFailures) != 1 || len(result.KnownFailuresPassing) != 0 || len(result.KnownFailuresFlaky) != 1 || result.KnownFailuresFlaky[0] != "legacy" {
		t.Fatal(result)
	}
	if !strings.Contains(text, "known failure \"legacy\" is flaky: it failed in one clean run and passed in the other\n") {
		t.Fatal(text)
	}
}

func TestAFailingKnownFailureStepKeepsTheTailOfItsLog(t *testing.T) {
	root := suiteFixture(t)
	proofWrite(t, root, initrepo.ScriptFile, suiteSteps("known_failure 'legacy' 'echo legacy export is wrong; exit 3'\nknown_failure 'fixed' 'echo all good'\n"), 0755)
	result, err := loop.CommandVerifier("scripts/verify")(context.Background(), root)
	if err != nil || result.ExitCode != 0 {
		t.Fatal(result, err)
	}
	for _, want := range []string{"known failure failed: legacy (exit 3)\n", "--- last 40 lines of legacy ---\nlegacy export is wrong\n", "known failure passed: fixed\n"} {
		if !strings.Contains(result.Output, want) {
			t.Fatal(result.Output)
		}
	}
	if strings.Contains(result.Output, "all good") {
		t.Fatal(result.Output)
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

func knownWriter(steps, config string) func(*testing.T, cao.StepRequest, string) string {
	return func(t *testing.T, req cao.StepRequest, patchDir string) string {
		proofWrite(t, req.WorkingDirectory, initrepo.ScriptFile, suiteSteps(steps), 0755)
		proofWrite(t, req.WorkingDirectory, loop.ConfigFile, suiteConfig(config), 0644)
		return suiteReply(t, patchDir, "sh suites/fast.sh && sh suites/unit.sh test_answer")
	}
}

func TestATuneRefusesARoundWhoseWriterAddsAKnownFailureEntryItself(t *testing.T) {
	offlinePasses(t)
	root := suiteFixture(t)
	f := &tuneCAO{writer: knownWriter(knownSteps, knownConfig)}
	code, out := tuneCall(t, "verify", "-worktree", root, "-cao", f.serve(t).URL)
	if code == 0 || !strings.Contains(out, "[test] verify_known_failures must not be changed by the tune: only the caller sets the baseline, with -known-failure LABEL or an entry already in the config; the writer added \"legacy\"") {
		t.Fatal(code, out)
	}
	if strings.Contains(initRead(t, root, loop.ConfigFile), "verify_known_failures") {
		t.Fatal("a chair set the baseline")
	}
	if len(f.prompts) == 0 || !strings.Contains(f.prompts[0], "known_failure 'LABEL' 'COMMAND'") || !strings.Contains(f.prompts[0], "Never write or change [test] verify_known_failures yourself") {
		t.Fatal(f.prompts)
	}
}

func TestATuneWhoseWriterProposesAKnownFailureStopsForTheCallerAndPrintsTheFlagThatAcceptsIt(t *testing.T) {
	offlinePasses(t)
	root := suiteFixture(t)
	f := &tuneCAO{writer: knownWriter(knownSteps, "")}
	code, out := tuneCall(t, "verify", "-worktree", root, "-cao", f.serve(t).URL)
	if code == 0 || !strings.Contains(out, "baseline_needs_caller after 1 round(s)") {
		t.Fatal(code, out)
	}
	if !strings.Contains(out, "the writer proposes a known failure the caller has not accepted: legacy; a chair cannot set the baseline: run the tune again with -known-failure 'legacy' to accept it, or have the test fixed") {
		t.Fatal(out)
	}
}

func TestATuneAcceptsAKnownFailureTheCallerNamesByFlagOrThatIsAlreadyInTheConfig(t *testing.T) {
	offlinePasses(t)
	for name, flagged := range map[string]bool{"flag": true, "config": false} {
		root := suiteFixture(t)
		args := []string{"verify", "-worktree", root, "-cao", ""}
		if flagged {
			args = append(args, "-known-failure", "legacy")
		} else {
			proofWrite(t, root, loop.ConfigFile, suiteConfig(knownConfig), 0644)
			args = append(args, "-again")
		}
		f := &tuneCAO{writer: knownWriter(knownSteps, knownConfig)}
		args[4] = f.serve(t).URL
		code, out := tuneCall(t, args...)
		if code != 0 || strings.Count(out, "known failures (verify passes while these fail): legacy\n") < 2 {
			t.Fatal(name, code, out)
		}
		if !strings.Contains(initRead(t, root, loop.ConfigFile), knownConfig) || !strings.Contains(f.prompts[0], "The caller accepts these known failures: legacy.") {
			t.Fatal(name, out, f.prompts[0])
		}
	}
}

func TestATuneRefusesARoundWhoseWriterDropsAKnownFailureTheCallerAccepted(t *testing.T) {
	offlinePasses(t)
	root := suiteFixture(t)
	f := &tuneCAO{writer: knownWriter("run 'fast' 'sh suites/fast.sh'\nrun 'unit' 'sh suites/unit.sh test_answer'\n", "")}
	code, out := tuneCall(t, "verify", "-worktree", root, "-cao", f.serve(t).URL, "-known-failure", "legacy")
	if code == 0 || !strings.Contains(out, "the writer removed \"legacy\"") {
		t.Fatal(code, out)
	}
}

func TestATuneStopsForTheCallerWhenAnAcceptedKnownFailureNowPasses(t *testing.T) {
	offlinePasses(t)
	root := suiteFixture(t)
	f := &tuneCAO{writer: knownWriter("run 'fast' 'sh suites/fast.sh'\nrun 'unit' 'sh suites/unit.sh test_answer'\nknown_failure 'legacy' 'sh suites/unit.sh test_answer'\n", knownConfig)}
	code, out := tuneCall(t, "verify", "-worktree", root, "-cao", f.serve(t).URL, "-known-failure", "legacy")
	if code == 0 || !strings.Contains(out, "baseline_needs_caller after 1 round(s)") || !strings.Contains(out, `known failure "legacy" passed in both clean runs`) || !strings.Contains(out, "only the caller removes an entry: run the tune again without -known-failure 'legacy'") {
		t.Fatal(code, out)
	}
}

func knownRun(t *testing.T, verify, test string) (code int, out string, result map[string]any) {
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
	writeFile(t, filepath.Join(seed, loop.ConfigFile), "[test]\nverify = 'sh scripts/verify'\n"+test+"[run]\npreflight_timeout = '0'\nreply_grace = '1ms'\nrounds = 2\n[sizing]\nmode = 'off'\n")
	writeFile(t, filepath.Join(seed, initrepo.ScriptFile), verify)
	safetyGit(t, seed, "add", ".gitignore", initrepo.ScriptFile)
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

func TestARunRefusesAListedKnownFailureWhoseStepDidNotRunInsteadOfCallingItFailing(t *testing.T) {
	for name, verify := range map[string]string{
		"no step":                             suiteSteps("run 'fast' 'true'\n"),
		"a wrapper without known_failure":     "#!/bin/sh\nrun() { sh -c \"$2\" || exit 1; }\nrun 'fast' 'true'\n",
		"an ordinary step with the same name": suiteSteps("run 'legacy' 'true'\n"),
	} {
		t.Run(name, func(t *testing.T) {
			code, out, result := knownRun(t, verify, knownConfig)
			if code != 16 || result["outcome"] != "baseline_mismatch" {
				t.Fatal(code, result, out)
			}
			if !strings.Contains(out, `known failure "legacy" is listed in [test] verify_known_failures but its step did not run`) || strings.Contains(out, "verify passes while these fail") {
				t.Fatal(out)
			}
		})
	}
}

func TestARunRefusesAKnownFailureStepTheListDoesNotName(t *testing.T) {
	code, out, result := knownRun(t, suiteSteps("run 'fast' 'true'\nknown_failure 'hidden' 'false'\n"), "")
	if code != 16 || result["outcome"] != "baseline_mismatch" || !strings.Contains(out, `known_failure step "hidden" is not listed in [test] verify_known_failures`) {
		t.Fatal(code, result, out)
	}
}

func TestARunWithAListedKnownFailureThatFailsIsApprovedAndNamesIt(t *testing.T) {
	code, out, result := knownRun(t, suiteSteps("run 'fast' 'true'\nknown_failure 'legacy' 'false'\n"), knownConfig)
	if code != 0 || result["outcome"] != "approved" || !strings.Contains(out, "  known failures (verify passes while these fail): legacy\n") {
		t.Fatal(code, result, out)
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

func TestPlayReadsTheKnownFailureListSoAListedStepIsNamedAndNotRefused(t *testing.T) {
	s := playStanding(t)
	writeFile(t, filepath.Join(s.Worktree, initrepo.ScriptFile), suiteSteps("run 'fast' 'true'\nknown_failure 'legacy' 'false'\n"))
	writeFile(t, filepath.Join(s.Worktree, loop.ConfigFile), "[test]\nverify = 'sh scripts/verify'\n"+knownConfig+"[run]\nreply_grace = '1ms'\n")
	fake := &playCAO{}
	out, err := capturePlay(t, []string{s.Session, "-cao", fake.serve(t).URL})
	if err != nil || !strings.Contains(out, "approved after 1 round(s)") || !strings.Contains(out, "  known failures (verify passes while these fail): legacy\n") {
		t.Fatalf("play: %v\n%s", err, out)
	}
}
