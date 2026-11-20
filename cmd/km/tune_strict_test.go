package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jystringfellow/kapellmeister/internal/cao"
	"github.com/jystringfellow/kapellmeister/internal/initrepo"
	"github.com/jystringfellow/kapellmeister/internal/loop"
)

const strictSlowTestBreak = "diff --git a/suites/slow.sh b/suites/slow.sh\n--- a/suites/slow.sh\n+++ b/suites/slow.sh\n@@ -1,3 +1,3 @@\n sleep " + suiteSlowSleep + "\n . ./answer.sh\n-test \"$(answer)\" -eq 42 && test \"$(total)\" = 84\n+test \"$(answer)\" -eq 42 && test \"$(total)\" = 85\n"

const strictSlowExcluded = "\n[[test.excluded]]\nsuite = 'slow'\nreason = 'flaky'\nmethod = 'by hand'\nevidence = 'failed once in ten'\nrun = 'sh suites/slow.sh'\n"

func strictTuneFixture(t *testing.T, config string) string {
	t.Helper()
	root := suiteFixture(t)
	proofWrite(t, root, initrepo.ScriptFile, suiteScript("fast"), 0755)
	proofWrite(t, root, loop.ConfigFile, suiteConfig(config), 0644)
	return root
}

type strictWrites struct {
	script string
	suites []string
	probe  string
	config string
	verify string
}

func strictWriter(base string, w strictWrites) func(*testing.T, cao.StepRequest, string) string {
	return func(t *testing.T, req cao.StepRequest, patchDir string) string {
		t.Helper()
		script := suiteScript(w.suites...)
		if w.script != "" {
			script = w.script
		}
		proofWrite(t, req.WorkingDirectory, initrepo.StrictFile, script, 0755)
		config := "strict = 'scripts/strict'\n"
		if w.probe != "" {
			proofWrite(t, req.WorkingDirectory, initrepo.StrictReadyFile, "#!/bin/sh\n"+w.probe+"\n", 0755)
			config += "strict_ready = 'scripts/strict-ready'\n"
		}
		head, tables, _ := strings.Cut(suiteConfig(base), "\n[[")
		if tables != "" {
			tables = "\n[[" + tables
		}
		proofWrite(t, req.WorkingDirectory, loop.ConfigFile, head+"\n"+config+w.config+tables, 0644)
		if w.verify != "" {
			proofWrite(t, req.WorkingDirectory, initrepo.ScriptFile, w.verify, 0755)
		}
		proofWrite(t, patchDir, "source.patch", suiteSourceBreak, 0644)
		proofWrite(t, patchDir, "test.patch", strictSlowTestBreak, 0644)
		return fmt.Sprintf(`{"command":"sh suites/slow.sh","left_out":[],"patches":[{"file":%q,"kind":"source"},{"file":%q,"kind":"test"}]}`, filepath.Join(patchDir, "source.patch"), filepath.Join(patchDir, "test.patch"))
	}
}

func strictPublished(t *testing.T, root string) bool {
	t.Helper()
	_, err := os.Stat(filepath.Join(root, initrepo.StrictFile))
	return err == nil
}

func TestTuneStrictPublishesAProvedStrictThatRunsEveryExcludedSuiteWithTheProbeKmAskedFor(t *testing.T) {
	root := strictTuneFixture(t, excludedEntry)
	f := &tuneCAO{writer: strictWriter(excludedEntry, strictWrites{suites: []string{"slow", "network"}, probe: "exit 0"})}
	code, out := tuneCall(t, "strict", "-worktree", root, "-cao", f.serve(t).URL)
	if code != 0 || !strings.Contains(out, "  tune strict: sh suites/slow.sh\n") || !strings.Contains(out, "strict failed, as required") {
		t.Fatal(code, out)
	}
	test, err := loop.ResolveTest(root)
	if err != nil || test.Strict != "scripts/strict" || test.StrictReady != "scripts/strict-ready" || test.Verify != initrepo.ScriptFile || len(test.Excluded) != 1 {
		t.Fatal(test, err)
	}
	if script := initRead(t, root, initrepo.StrictFile); !strings.Contains(script, "run 'network' 'sh suites/network.sh'") || initrepo.IsStub(script) {
		t.Fatal(script)
	}
	if info, err := os.Stat(filepath.Join(root, initrepo.StrictReadyFile)); err != nil || info.Mode().Perm()&0o111 == 0 {
		t.Fatal(info, err)
	}
	if initRead(t, root, initrepo.ScriptFile) != suiteScript("fast") {
		t.Fatal("tune strict changed scripts/verify")
	}
	writing := f.prompts[0]
	for _, want := range []string{"Tune this repository's strict.", "network (network): sh suites/network.sh", "km has decided this strict needs a readiness probe", "scripts/strict-ready", "Break patch directory: "} {
		if !strings.Contains(writing, want) {
			t.Fatalf("writer was not told %q:\n%s", want, writing)
		}
	}
	if review := f.prompts[len(f.prompts)-1]; !strings.Contains(review, "Verify and strict together must run everything CI runs") {
		t.Fatal(review)
	}
}

func TestTuneStrictRefusesAStrictThatLeavesAnExcludedSuiteUncoveredAndPublishesNothing(t *testing.T) {
	root := strictTuneFixture(t, excludedEntry)
	f := &tuneCAO{writer: strictWriter(excludedEntry, strictWrites{suites: []string{"slow"}, probe: "exit 0"})}
	code, out := tuneCall(t, "strict", "-worktree", root, "-cao", f.serve(t).URL)
	if code == 0 || strictPublished(t, root) {
		t.Fatal(code, out)
	}
	if want := `suite "network" is excluded from verify and scripts/strict has no run step labelled "network": strict must run every excluded suite (by hand: sh suites/network.sh)`; !strings.Contains(out, want) {
		t.Fatal(out)
	}
}

func TestKmDecidesThatAStrictHoldingASuiteExcludedForNetworkNeedsAProbe(t *testing.T) {
	root := strictTuneFixture(t, excludedEntry)
	f := &tuneCAO{writer: strictWriter(excludedEntry, strictWrites{suites: []string{"slow", "network"}})}
	code, out := tuneCall(t, "strict", "-worktree", root, "-cao", f.serve(t).URL)
	if code == 0 || strictPublished(t, root) || !strings.Contains(out, `strict holds a suite that needs something a machine may lack ("network": network), so km requires a readiness probe: write scripts/strict-ready and set [test] strict_ready = "scripts/strict-ready"`) {
		t.Fatal(code, out)
	}
}

func TestAWriterDoesNotAddAProbeKmDidNotAskFor(t *testing.T) {
	root := strictTuneFixture(t, strictSlowExcluded)
	f := &tuneCAO{writer: strictWriter(strictSlowExcluded, strictWrites{suites: []string{"slow"}, probe: "exit 0"})}
	code, out := tuneCall(t, "strict", "-worktree", root, "-cao", f.serve(t).URL)
	if code == 0 || strictPublished(t, root) || !strings.Contains(out, "km decides when strict needs a readiness probe, and this strict holds no suite excluded for network, a service, a credential or another machine: remove [test] strict_ready") {
		t.Fatal(code, out)
	}
	if !strings.Contains(f.prompts[0], "Do not write a strict_ready probe") {
		t.Fatal(f.prompts[0])
	}
	f = &tuneCAO{writer: strictWriter(strictSlowExcluded, strictWrites{suites: []string{"slow"}})}
	if code, out := tuneCall(t, "strict", "-worktree", root, "-cao", f.serve(t).URL); code != 0 || !strictPublished(t, root) {
		t.Fatal(code, out)
	}
}

func TestAProbeThatFailsHereMeansStrictIsNotProvedAndNothingIsPublished(t *testing.T) {
	root := strictTuneFixture(t, excludedEntry)
	f := &tuneCAO{writer: strictWriter(excludedEntry, strictWrites{suites: []string{"slow", "network"}, probe: "echo no route; exit 4"})}
	code, out := tuneCall(t, "strict", "-worktree", root, "-cao", f.serve(t).URL)
	if code == 0 || strictPublished(t, root) || !strings.Contains(out, "strict_not_run") || !strings.Contains(out, "no route") {
		t.Fatal(code, out)
	}
}

func TestTuneStrictRefusesARoundThatChangesVerifyOrItsKeys(t *testing.T) {
	root := strictTuneFixture(t, strictSlowExcluded)
	f := &tuneCAO{writer: strictWriter(strictSlowExcluded, strictWrites{suites: []string{"slow"}, verify: suiteScript("fast", "unit")})}
	code, out := tuneCall(t, "strict", "-worktree", root, "-cao", f.serve(t).URL)
	if code == 0 || strictPublished(t, root) || !strings.Contains(out, "tune changed files outside setup: scripts/verify") {
		t.Fatal(code, out)
	}
	f = &tuneCAO{writer: strictWriter(strictSlowExcluded, strictWrites{suites: []string{"slow"}, config: "verify_required = ['fast']\n"})}
	code, out = tuneCall(t, "strict", "-worktree", root, "-cao", f.serve(t).URL)
	if code == 0 || strictPublished(t, root) || !strings.Contains(out, "only [test] strict, strict_ready and strict_timeout may change in the config") {
		t.Fatal(code, out)
	}
}

func TestTuneStrictNeedsAWrittenVerifyAndLeavesAnExistingStrictAloneWithoutAgain(t *testing.T) {
	stub := suiteFixture(t)
	if code, out := tuneCall(t, "strict", "-worktree", stub); code == 0 || !strings.Contains(out, "verify is not written yet: run km tune verify first") {
		t.Fatal(code, out)
	}
	root := strictTuneFixture(t, "strict = 'make strict'\n")
	if code, out := tuneCall(t, "strict", "-worktree", root); code == 0 || !strings.Contains(out, `strict "make strict" is already written; pass -again to tune it anyway`) {
		t.Fatal(code, out)
	}
}

func TestTheStrictTimeoutTheCallerPassesIsTheProofLimitAndMustBeRecorded(t *testing.T) {
	root := strictTuneFixture(t, strictSlowExcluded)
	f := &tuneCAO{writer: strictWriter(strictSlowExcluded, strictWrites{suites: []string{"slow"}})}
	code, out := tuneCall(t, "strict", "-worktree", root, "-cao", f.serve(t).URL, "-strict-timeout", "7m")
	if code == 0 || strictPublished(t, root) || !strings.Contains(out, `[test] strict_timeout must be "7m0s": the caller passed -strict-timeout`) {
		t.Fatal(code, out)
	}
	if !strings.Contains(f.prompts[0], `Set strict_timeout = "7m0s" in [test]`) || !strings.Contains(out, "budget 38m0s: a whole proof at the 7m0s strict timeout is two clean runs and 2 break runs (28m0s), plus 10m0s for the chairs") {
		t.Fatal(out, f.prompts[0])
	}
	f = &tuneCAO{writer: strictWriter(strictSlowExcluded, strictWrites{suites: []string{"slow"}, config: "strict_timeout = '7m0s'\n"})}
	if code, out := tuneCall(t, "strict", "-worktree", root, "-cao", f.serve(t).URL, "-strict-timeout", "7m"); code != 0 || !strictPublished(t, root) {
		t.Fatal(code, out)
	}
}

func TestTuneNamesBothSubjects(t *testing.T) {
	code, out := tuneCall(t, "everything")
	if code != 2 || !strings.Contains(out, "  verify  ") || !strings.Contains(out, "  strict  ") {
		t.Fatal(code, out)
	}
}

func TestAStepThatCarriesAnExcludedSuitesLabelButNotItsCommandIsRefused(t *testing.T) {
	root := strictTuneFixture(t, excludedEntry)
	hollow := suiteSteps("run 'slow' 'sh suites/slow.sh'\nrun 'network' 'true'\n")
	f := &tuneCAO{writer: strictWriter(excludedEntry, strictWrites{script: hollow, probe: "exit 0"})}
	code, out := tuneCall(t, "strict", "-worktree", root, "-cao", f.serve(t).URL)
	if code == 0 || strictPublished(t, root) || !strings.Contains(out, `the step labelled "network" in scripts/strict runs "true", not the command recorded for that excluded suite, "sh suites/network.sh"`) {
		t.Fatal(code, out)
	}
}

func TestAStrictThatRecordsNoStepsCannotShowAnExcludedSuiteRan(t *testing.T) {
	root := strictTuneFixture(t, strictSlowExcluded)
	silent := "#!/bin/sh\ncd \"$(dirname \"$0\")/..\" || exit 1\nrun() { sh -c \"$2\"; }\nrun 'slow' 'sh suites/slow.sh'\n"
	f := &tuneCAO{writer: strictWriter(strictSlowExcluded, strictWrites{script: silent})}
	code, out := tuneCall(t, "strict", "-worktree", root, "-cao", f.serve(t).URL)
	if code == 0 || strictPublished(t, root) || !strings.Contains(out, `suite_not_run`) || !strings.Contains(out, `excluded suite "slow" did not run in clean run 1: strict recorded no steps`) {
		t.Fatal(code, out)
	}
}

func TestAProvedStrictIsPublishedWhenALaterRoundIsEditedAndNotProved(t *testing.T) {
	root := strictTuneFixture(t, strictSlowExcluded)
	excludedRounds(t, "2")
	round := 0
	good := strictWriter(strictSlowExcluded, strictWrites{suites: []string{"slow"}})
	bad := strictWriter(strictSlowExcluded, strictWrites{suites: []string{"unit"}})
	f := &tuneCAO{mode: "revision", writer: func(t *testing.T, req cao.StepRequest, patchDir string) string {
		round++
		if round == 1 {
			return good(t, req, patchDir)
		}
		return bad(t, req, patchDir)
	}}
	code, out := tuneCall(t, "strict", "-worktree", root, "-cao", f.serve(t).URL)
	if code != 0 || !strings.Contains(out, "later revision was not adopted") || strings.Contains(out, "setup files changed after proof") {
		t.Fatal(code, out)
	}
	if script := initRead(t, root, initrepo.StrictFile); script != suiteScript("slow") {
		t.Fatal(script)
	}
}

func TestTuneStrictStopsBeforeSeatingAnyoneWhenVerifyLeftNothingOut(t *testing.T) {
	root := strictTuneFixture(t, "")
	code, out := tuneCall(t, "strict", "-worktree", root)
	if code == 0 || !strings.Contains(out, "there is nothing for strict to hold: [[test.excluded]] is empty and verify is not time-bounded") {
		t.Fatal(code, out)
	}
	bounded := strictTuneFixture(t, "verify_time_bounded = true\n")
	f := &tuneCAO{writer: strictWriter("verify_time_bounded = true\n", strictWrites{suites: []string{"slow"}})}
	if code, out := tuneCall(t, "strict", "-worktree", bounded, "-cao", f.serve(t).URL); code != 0 || !strictPublished(t, bounded) {
		t.Fatal(code, out)
	}
}

func TestASuiteExcludedForACredentialOrAServiceNeedsAProbeToo(t *testing.T) {
	credential := strings.Replace(strictSlowExcluded, "reason = 'flaky'", "reason = 'credential'", 1)
	root := strictTuneFixture(t, credential)
	f := &tuneCAO{writer: strictWriter(credential, strictWrites{suites: []string{"slow"}})}
	code, out := tuneCall(t, "strict", "-worktree", root, "-cao", f.serve(t).URL)
	if code == 0 || strictPublished(t, root) || !strings.Contains(out, `strict holds a suite that needs something a machine may lack ("slow": credential), so km requires a readiness probe`) {
		t.Fatal(code, out)
	}
	f = &tuneCAO{writer: strictWriter(credential, strictWrites{suites: []string{"slow"}, probe: "exit 0"})}
	if code, out := tuneCall(t, "strict", "-worktree", root, "-cao", f.serve(t).URL); code != 0 || !strictPublished(t, root) {
		t.Fatal(code, out)
	}
}

func TestTheUsageNamesTheBudgetFlagOfTuneStrict(t *testing.T) {
	var diag strings.Builder
	execute([]string{"help"}, &diag)
	if !strings.Contains(diag.String(), "km tune strict [-worktree DIR] [-strict-timeout D] [-budget D]") {
		t.Fatal(diag.String())
	}
}

func TestTuneVerifyWarnsWhenItExcludesASuiteTheWrittenStrictDoesNotRun(t *testing.T) {
	root := suiteFixture(t)
	excludedRounds(t, "2")
	const keepsStrict = "strict = 'scripts/strict'\n"
	proofWrite(t, root, loop.ConfigFile, suiteConfig(keepsStrict), 0644)
	proofWrite(t, root, initrepo.StrictFile, suiteScript("slow"), 0755)
	round := 0
	f := &tuneCAO{writer: func(t *testing.T, req cao.StepRequest, patchDir string) string {
		suites := []string{"fast", "loopback", "network"}
		if round++; round > 1 {
			suites = suites[:2]
		} else {
			proofWrite(t, req.WorkingDirectory, loop.ConfigFile, suiteConfig(keepsStrict), 0644)
		}
		proofWrite(t, req.WorkingDirectory, initrepo.ScriptFile, suiteScript(suites...), 0755)
		return suiteReply(t, patchDir, "sh suites/"+strings.Join(suites, ".sh && sh suites/")+".sh")
	}}
	code, out := tuneCall(t, "verify", "-worktree", root, "-cao", f.serve(t).URL)
	if code != 0 || !strings.Contains(out, "  strict does not run excluded suite network: run km tune strict -again so that verify and strict together still run what CI runs\n") {
		t.Fatal(code, out)
	}
}
