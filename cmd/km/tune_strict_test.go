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
	suites []string
	probe  string
	config string
	verify string
}

func strictWriter(base string, w strictWrites) func(*testing.T, cao.StepRequest, string) string {
	return func(t *testing.T, req cao.StepRequest, patchDir string) string {
		t.Helper()
		proofWrite(t, req.WorkingDirectory, initrepo.StrictFile, suiteScript(w.suites...), 0755)
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
	if code == 0 || strictPublished(t, root) || !strings.Contains(out, `strict holds a suite excluded for network ("network"), so km requires a readiness probe: write scripts/strict-ready and set [test] strict_ready = "scripts/strict-ready"`) {
		t.Fatal(code, out)
	}
}

func TestAWriterDoesNotAddAProbeKmDidNotAskFor(t *testing.T) {
	root := strictTuneFixture(t, strictSlowExcluded)
	f := &tuneCAO{writer: strictWriter(strictSlowExcluded, strictWrites{suites: []string{"slow"}, probe: "exit 0"})}
	code, out := tuneCall(t, "strict", "-worktree", root, "-cao", f.serve(t).URL)
	if code == 0 || strictPublished(t, root) || !strings.Contains(out, "km decides when strict needs a readiness probe, and this strict holds no suite excluded for network: remove [test] strict_ready") {
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
