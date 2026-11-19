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

func excludedRounds(t *testing.T, rounds string) {
	t.Helper()
	config, err := os.ReadFile(filepath.Join(loop.Home(), "config.toml"))
	if err != nil || !strings.Contains(string(config), "rounds = 1") {
		t.Fatal(string(config), err)
	}
	proofWrite(t, loop.Home(), "config.toml", strings.Replace(string(config), "rounds = 1", "rounds = "+rounds, 1), 0644)
}

func excludedWriter(rounds ...[]string) func(*testing.T, cao.StepRequest, string) string {
	round := 0
	return func(t *testing.T, req cao.StepRequest, patchDir string) string {
		t.Helper()
		suites := rounds[min(round, len(rounds)-1)]
		proofWrite(t, req.WorkingDirectory, initrepo.ScriptFile, suiteScript(suites...), 0755)
		if round == 0 {
			proofWrite(t, req.WorkingDirectory, loop.ConfigFile, suiteConfig(""), 0644)
		}
		round++
		return suiteReply(t, patchDir, "sh suites/"+strings.Join(suites, ".sh && sh suites/")+".sh")
	}
}

func TestTuneRecordsASuiteThatFailsWithoutNetworkAsExcludedAndPublishesAVerifyWithoutIt(t *testing.T) {
	root := suiteFixture(t)
	excludedRounds(t, "2")
	f := &tuneCAO{writer: excludedWriter([]string{"fast", "network"}, []string{"fast"})}
	code, out := tuneCall(t, "verify", "-worktree", root, "-cao", f.serve(t).URL, "-json")
	var sum struct {
		RunID string `json:"run_id"`
		Tune  struct {
			Status   string
			Excluded []loop.Exclusion
		}
	}
	if code != 0 || json.Unmarshal([]byte(out), &sum) != nil {
		t.Fatal(code, out)
	}
	method := prove.NoNetworkMethod(context.Background())
	if sum.Tune.Status != "proved" || len(sum.Tune.Excluded) != 1 {
		t.Fatal(out)
	}
	entry := sum.Tune.Excluded[0]
	if entry.Suite != "network" || entry.Reason != "network" || entry.Method != method || !strings.HasPrefix(entry.Evidence, "exit ") || entry.Run != "sh suites/network.sh" {
		t.Fatal(entry)
	}
	if script := initRead(t, root, initrepo.ScriptFile); strings.Contains(script, "network") || !strings.Contains(script, "run 'fast' 'sh suites/fast.sh'") {
		t.Fatal(script)
	}
	published, err := loop.ResolveTest(root)
	if err != nil || len(published.Excluded) != 1 || published.Excluded[0] != entry {
		t.Fatal(published, err)
	}
	var waited, diag bytes.Buffer
	if err := waitCommand([]string{sum.RunID, loop.StateDir()}, &waited, &diag); err != nil {
		t.Fatal(err, diag.String())
	}
	if want := "  excluded: " + entry.String() + "\n"; !strings.Contains(waited.String(), want) {
		t.Fatal(want, waited.String())
	}
}

func TestTheRoundThatHasASuiteNeedingNetworkIsNotProvedAndTheWriterIsToldToRemoveItsStep(t *testing.T) {
	root := suiteFixture(t)
	f := &tuneCAO{writer: excludedWriter([]string{"fast", "network"})}
	code, out := tuneCall(t, "verify", "-worktree", root, "-cao", f.serve(t).URL)
	if code == 0 || !initrepo.IsStub(initRead(t, root, initrepo.ScriptFile)) {
		t.Fatal(code, out)
	}
	method := prove.NoNetworkMethod(context.Background())
	if want := `suite "network" passes with network and fails on a clean copy without it (` + method + `): exit `; !strings.Contains(out, want) {
		t.Fatal(want, out)
	}
	if !strings.Contains(out, "km recorded it in [[test.excluded]]: remove its step from scripts/verify") {
		t.Fatal(out)
	}
}

func TestARequiredSuiteThatFailsWithoutNetworkIsAnErrorAndIsNotExcluded(t *testing.T) {
	root := suiteFixture(t)
	excludedRounds(t, "2")
	var worktree string
	write := suiteWriter("verify_required = ['network']\n", "fast", "network")
	f := &tuneCAO{writer: func(t *testing.T, req cao.StepRequest, patchDir string) string {
		worktree = req.WorkingDirectory
		proofWrite(t, patchDir, "network.patch", "diff --git a/suites/network.url b/suites/network.url\n--- a/suites/network.url\n+++ b/suites/network.url\n@@ -1 +1 @@\n-"+strings.TrimSpace(initRead(t, root, "suites/network.url"))+"\n+http://127.0.0.1:1/\n", 0644)
		reply := write(t, req, patchDir)
		return strings.Replace(reply, `"patches":[`, `"patches":[{"file":"`+filepath.Join(patchDir, "network.patch")+`","kind":"test"},`, 1)
	}}
	code, out := tuneCall(t, "verify", "-worktree", root, "-cao", f.serve(t).URL, "-require", "network")
	if code == 0 || !initrepo.IsStub(initRead(t, root, initrepo.ScriptFile)) {
		t.Fatal(code, out)
	}
	if !strings.Contains(out, `required suite "network" passes with network and fails on a clean copy without it`) || !strings.Contains(out, "a required suite cannot be excluded: make it pass without network, or stop requiring it") {
		t.Fatal(out)
	}
	if kept, err := loop.ResolveTest(worktree); err != nil || len(kept.Excluded) != 0 {
		t.Fatal(kept, err)
	}
}

const excludedEntry = "\n[[test.excluded]]\nsuite = 'network'\nreason = 'network'\nmethod = 'network namespace'\nevidence = 'exit 7'\nrun = 'sh suites/network.sh'\n"

func TestTuneRefusesARoundThatWritesOrChangesAnExcludedEntryItself(t *testing.T) {
	root := suiteFixture(t)
	f := &tuneCAO{writer: suiteWriter(excludedEntry, "fast")}
	code, out := tuneCall(t, "verify", "-worktree", root, "-cao", f.serve(t).URL)
	if code == 0 || !strings.Contains(out, "[[test.excluded]] is written by km from its own run without network, not by the tune: leave it as it was") || !initrepo.IsStub(initRead(t, root, initrepo.ScriptFile)) {
		t.Fatal(code, out)
	}
	proofWrite(t, root, loop.ConfigFile, suiteConfig(excludedEntry), 0644)
	f = &tuneCAO{writer: suiteWriter("", "fast")}
	code, out = tuneCall(t, "verify", "-worktree", root, "-cao", f.serve(t).URL)
	if code == 0 || !strings.Contains(out, "[[test.excluded]] is written by km from its own run without network, not by the tune: leave it as it was") {
		t.Fatal(code, out)
	}
}

func TestTuneRefusesAVerifyThatStillRunsAnExcludedSuiteAndKeepsTheEntryInItsSummary(t *testing.T) {
	root := suiteFixture(t)
	proofWrite(t, root, loop.ConfigFile, suiteConfig(excludedEntry), 0644)
	f := &tuneCAO{writer: suiteWriter(excludedEntry, "fast", "network")}
	code, out := tuneCall(t, "verify", "-worktree", root, "-cao", f.serve(t).URL)
	if code == 0 || !strings.Contains(out, `suite "network" is excluded from verify (network: exit 7): remove its step from scripts/verify; only the caller removes an entry from [[test.excluded]]`) {
		t.Fatal(code, out)
	}
	if !strings.Contains(out, "  excluded: network (network; network namespace): exit 7; run it by hand: sh suites/network.sh\n") {
		t.Fatal(out)
	}
}

func TestBothChairsAreToldThatKmDecidesWhichSuitesNeedNetwork(t *testing.T) {
	root := suiteFixture(t)
	f := &tuneCAO{writer: suiteWriter("", "fast")}
	if code, out := tuneCall(t, "verify", "-worktree", root, "-cao", f.serve(t).URL); code != 0 || len(f.prompts) < 2 {
		t.Fatal(code, out)
	}
	for _, prompt := range f.prompts[:2] {
		if !strings.Contains(prompt, "km runs the verify on a clean copy without network") || !strings.Contains(prompt, "[[test.excluded]]") {
			t.Fatal(prompt)
		}
	}
}

func TestRunSaysWhichSuitesVerifyExcludesBeforeItStarts(t *testing.T) {
	root := suiteFixture(t)
	proofWrite(t, root, loop.ConfigFile, suiteConfig(excludedEntry), 0644)
	var out bytes.Buffer
	printExcluded(&out, root)
	if got, want := out.String(), "verify excludes network (network; network namespace): exit 7; run it by hand: sh suites/network.sh\n"; got != want {
		t.Fatal(got)
	}
	out.Reset()
	proofWrite(t, root, loop.ConfigFile, suiteConfig(""), 0644)
	if printExcluded(&out, root); out.Len() != 0 {
		t.Fatal(out.String())
	}
}
