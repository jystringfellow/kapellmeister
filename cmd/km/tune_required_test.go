package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jystringfellow/kapellmeister/internal/initrepo"
	"github.com/jystringfellow/kapellmeister/internal/loop"
	"github.com/jystringfellow/kapellmeister/internal/prove"
)

const suiteRequired = "required = ['fast', 'slow']\n"

func TestTuneRefusesAVerifyThatLeavesOutARequiredSuiteAndPublishesNothing(t *testing.T) {
	root := suiteFixture(t)
	proofWrite(t, root, loop.ConfigFile, suiteConfig(suiteRequired), 0644)
	f := &tuneCAO{writer: suiteWriter(suiteRequired, "fast")}
	code, out := tuneCall(t, "verify", "-worktree", root, "-cao", f.serve(t).URL, "-verify-timeout", "200ms")
	if code == 0 || !strings.Contains(out, `required suite "slow" is not a step in scripts/verify`) {
		t.Fatal(code, out)
	}
	if !initrepo.IsStub(initRead(t, root, initrepo.ScriptFile)) {
		t.Fatal("a verify without a required suite was published")
	}
}

func TestTuneRaisesTheVerifyTimeoutForARequiredSlowSuiteAndSaysSoInBothSummaries(t *testing.T) {
	root := suiteFixture(t)
	proofWrite(t, root, loop.ConfigFile, suiteConfig(suiteRequired), 0644)
	f := &tuneCAO{writer: suiteWriter(suiteRequired, "fast", "slow")}
	code, out := tuneCall(t, "verify", "-worktree", root, "-cao", f.serve(t).URL, "-verify-timeout", "200ms", "-json")
	var sum struct {
		RunID string `json:"run_id"`
		Tune  struct {
			Required      []string
			Status        string
			TimeoutRaised *struct {
				FromMS int64 `json:"from_ms"`
				ToMS   int64 `json:"to_ms"`
			} `json:"timeout_raised"`
		}
	}
	if code != 0 || json.Unmarshal([]byte(out), &sum) != nil {
		t.Fatal(code, out)
	}
	raised := sum.Tune.TimeoutRaised
	if sum.Tune.Status != "proved" || strings.Join(sum.Tune.Required, ",") != "fast,slow" || raised == nil || raised.FromMS != 200 || raised.ToMS < 400 {
		t.Fatal(out)
	}
	if script := initRead(t, root, initrepo.ScriptFile); !strings.Contains(script, "run 'slow' 'sh suites/slow.sh'") {
		t.Fatal(script)
	}
	var waited, diag bytes.Buffer
	if err := waitCommand([]string{sum.RunID, loop.StateDir()}, &waited, &diag); err != nil {
		t.Fatal(err, diag.String())
	}
	to := time.Duration(raised.ToMS) * time.Millisecond
	for _, want := range []string{"required suites: fast, slow", fmt.Sprintf("verify timeout raised from 200ms to %s; prove by hand with km prove -timeout %s", to, to)} {
		if !strings.Contains(waited.String(), want) {
			t.Fatal(want, waited.String())
		}
	}
}

func TestTuneStopsRaisingTheVerifyTimeoutAtItsBudgetAndSaysWhatToRaiseInsteadOfNarrowing(t *testing.T) {
	root := suiteFixture(t)
	proofWrite(t, root, loop.ConfigFile, suiteConfig(suiteRequired), 0644)
	f := &tuneCAO{writer: suiteWriter(suiteRequired, "fast", "slow")}
	old := runTuneProof
	defer func() { runTuneProof = old }()
	var limits []time.Duration
	runTuneProof = func(_ context.Context, opts prove.Options) prove.Result {
		limits = append(limits, opts.Timeout)
		return prove.Result{Status: "timeout", ExitStatus: 3, Reason: "clean run 1: verify timed out"}
	}
	code, out := tuneCall(t, "verify", "-worktree", root, "-cao", f.serve(t).URL)
	if code == 0 || !reflect.DeepEqual(limits, []time.Duration{5 * time.Minute, 10 * time.Minute, 20 * time.Minute}) {
		t.Fatal(code, limits, out)
	}
	if !strings.Contains(out, "verify timed out at 5m0s; required suites are never left out for time, so the timeout is raised to 10m0s") {
		t.Fatal(out)
	}
	if !strings.Contains(out, "the timeout is not raised past the 30m0s budget: raise -budget and -verify-timeout") || !initrepo.IsStub(initRead(t, root, initrepo.ScriptFile)) {
		t.Fatal(out)
	}
}

func TestTuneWithoutRequiredSuitesOrSlowKeepsItsTimeoutAndReportsTheTimeout(t *testing.T) {
	root := suiteFixture(t)
	f := &tuneCAO{writer: suiteWriter("", "fast", "slow")}
	code, out := tuneCall(t, "verify", "-worktree", root, "-cao", f.serve(t).URL, "-verify-timeout", "200ms")
	if code == 0 || strings.Contains(out, "raised") || !strings.Contains(out, "timeout: clean run 1: verify timed out") {
		t.Fatal(code, out)
	}
}

func TestSuitesRequiredByFlagMustBeRecordedInTheConfigTuneWrites(t *testing.T) {
	for _, recorded := range []bool{false, true} {
		root := suiteFixture(t)
		config := ""
		if recorded {
			config = suiteRequired
		}
		f := &tuneCAO{writer: suiteWriter(config, "fast", "slow")}
		code, out := tuneCall(t, "verify", "-worktree", root, "-cao", f.serve(t).URL, "-require", "fast", "-require", "slow", "-verify-timeout", "5s")
		published := initRead(t, root, loop.ConfigFile)
		if recorded && (code != 0 || !strings.Contains(published, suiteRequired)) {
			t.Fatal(code, out, published)
		}
		if !recorded && (code == 0 || !strings.Contains(out, `[test] required must list "fast"`) || strings.Contains(published, "required")) {
			t.Fatal(code, out, published)
		}
		if len(f.prompts) == 0 || !strings.Contains(f.prompts[0], "Required suites: fast, slow.") {
			t.Fatal(f.prompts)
		}
	}
}

func TestASlowTuneTellsBothChairsVerifyMirrorsCIAndRaisesItsTimeoutInsteadOfNarrowing(t *testing.T) {
	root := suiteFixture(t)
	f := &tuneCAO{writer: suiteWriter("slow = true\n", "fast", "slow")}
	code, out := tuneCall(t, "verify", "-worktree", root, "-cao", f.serve(t).URL, "-slow", "-verify-timeout", "200ms")
	if code != 0 || !strings.Contains(out, "verify timed out at 200ms; a slow verify is never narrowed for time, so the timeout is raised to 400ms") {
		t.Fatal(code, out)
	}
	if len(f.prompts) != 2 {
		t.Fatal(len(f.prompts))
	}
	for i, prompt := range f.prompts {
		for _, unwanted := range []string{"about two minutes", "never more than five"} {
			if strings.Contains(prompt, unwanted) {
				t.Errorf("prompt %d contains %q", i, unwanted)
			}
		}
		if !strings.Contains(prompt, "may take as long as CI") {
			t.Errorf("prompt %d does not say verify may take as long as CI:\n%s", i, prompt)
		}
	}
	if !strings.Contains(f.prompts[1], "ask for changes when anything is left out to save time") {
		t.Error(f.prompts[1])
	}
	if !strings.Contains(initRead(t, root, loop.ConfigFile), "slow = true") {
		t.Fatal("slow was not recorded")
	}
}

func TestASlowTuneGivenByFlagMustBeRecordedInTheConfig(t *testing.T) {
	root := suiteFixture(t)
	f := &tuneCAO{writer: suiteWriter("", "fast", "slow")}
	code, out := tuneCall(t, "verify", "-worktree", root, "-cao", f.serve(t).URL, "-slow", "-verify-timeout", "5s")
	if code == 0 || !strings.Contains(out, "[test] slow must be true") {
		t.Fatal(code, out)
	}
}

func TestProveRefusesAVerifyWithoutAStepForEachRequiredSuite(t *testing.T) {
	root := suiteFixture(t)
	proofWrite(t, root, loop.ConfigFile, suiteConfig(suiteRequired), 0644)
	proofWrite(t, root, initrepo.ScriptFile, suiteScript("fast"), 0755)
	report := proofJSON(t, root)
	if report.ExitStatus != 10 || report.Status != "refused" || !strings.Contains(report.Reason, `required suite "slow" is not a step in scripts/verify`) || len(report.Runs) != 0 {
		t.Fatal(report)
	}
	proofWrite(t, root, initrepo.ScriptFile, suiteScript("fast", "slow"), 0755)
	if report := proofJSON(t, root); report.ExitStatus != 11 {
		t.Fatal(report)
	}
	proofWrite(t, root, loop.ConfigFile, "[test]\nverify = 'sh suites/fast.sh'\n"+suiteRequired, 0644)
	if report := proofJSON(t, root); report.ExitStatus != 10 || !strings.Contains(report.Reason, "required suites need scripts/verify") {
		t.Fatal(report)
	}
}

func TestTheGuideFollowsTheRequireAndSlowFlags(t *testing.T) {
	code, out := tuneCall(t, "verify", "-guide", "-slow", "-require", "integration")
	if code != 0 || !strings.Contains(out, "may take as long as CI") || !strings.Contains(out, "Required suites: integration.") || strings.Contains(out, "about two minutes") {
		t.Fatal(code, out)
	}
}

func TestRequiredSuiteNamesMustBeUsableAsStepLabels(t *testing.T) {
	for _, bad := range []string{"required = ['']\n", "required = ['a', 'a']\n", "required = [\"it's\"]\n", "required = [\"two\\nlines\"]\n"} {
		root := suiteFixture(t)
		proofWrite(t, root, loop.ConfigFile, suiteConfig(bad), 0644)
		if code, out := proofCall(t, root); code != 10 || !strings.Contains(out, "test.required") {
			t.Fatal(bad, code, out)
		}
		if _, _, err := loop.ResolveHooks(root, ""); err == nil || !strings.Contains(err.Error(), "test.required") {
			t.Fatal(bad, err)
		}
	}
}
