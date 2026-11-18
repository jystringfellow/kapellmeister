package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jystringfellow/kapellmeister/internal/cao"
	"github.com/jystringfellow/kapellmeister/internal/initrepo"
	"github.com/jystringfellow/kapellmeister/internal/loop"
	"github.com/jystringfellow/kapellmeister/internal/prove"
)

const suiteRequired = "verify_required = ['fast', 'slow']\n"

func TestTuneRefusesAVerifyThatLeavesOutARequiredSuiteAndPublishesNothing(t *testing.T) {
	root := suiteFixture(t)
	proofWrite(t, root, loop.ConfigFile, suiteConfig(suiteRequired), 0644)
	f := &tuneCAO{writer: suiteWriter(suiteRequired, "fast")}
	code, out := tuneCall(t, "verify", "-worktree", root, "-cao", f.serve(t).URL, "-verify-timeout", "5s")
	if code == 0 || !strings.Contains(out, `required suite "slow" did not run in clean run 1`) {
		t.Fatal(code, out)
	}
	if !initrepo.IsStub(initRead(t, root, initrepo.ScriptFile)) {
		t.Fatal("a verify without a required suite was published")
	}
}

func TestWithoutAnyFlagTuneRaisesTheVerifyTimeoutInsteadOfNarrowingAndSavesItForLaterProofs(t *testing.T) {
	root := suiteFixture(t)
	f := &tuneCAO{writer: suiteWriter("", "fast", "slow")}
	code, out := tuneCall(t, "verify", "-worktree", root, "-cao", f.serve(t).URL, "-verify-timeout", "200ms", "-json")
	var sum struct {
		RunID string `json:"run_id"`
		Tune  struct {
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
	if sum.Tune.Status != "proved" || raised == nil || raised.FromMS != 200 || raised.ToMS < 400 {
		t.Fatal(out)
	}
	to := time.Duration(raised.ToMS) * time.Millisecond
	if script := initRead(t, root, initrepo.ScriptFile); !strings.Contains(script, "run 'slow' 'sh suites/slow.sh'") {
		t.Fatal(script)
	}
	if config := initRead(t, root, loop.ConfigFile); !strings.Contains(config, fmt.Sprintf("verify_timeout = %q\n", to.String())) {
		t.Fatal(config)
	}
	var waited, diag bytes.Buffer
	if err := waitCommand([]string{sum.RunID, loop.StateDir()}, &waited, &diag); err != nil {
		t.Fatal(err, diag.String())
	}
	if want := fmt.Sprintf("verify timeout raised from 200ms to %s and saved as [test] verify_timeout", to); !strings.Contains(waited.String(), want) {
		t.Fatal(want, waited.String())
	}
}

func TestProveAndALaterTuneReadTheSavedVerifyTimeoutAndAFlagOverridesIt(t *testing.T) {
	root := suiteFixture(t)
	proofWrite(t, root, initrepo.ScriptFile, suiteScript("fast", "slow"), 0755)
	proofWrite(t, root, loop.ConfigFile, suiteConfig("verify_timeout = '100ms'\n"), 0644)
	if report := proofJSON(t, root); report.ExitStatus != 3 {
		t.Fatal(report)
	}
	if report := proofJSON(t, root, "-timeout", "10s"); report.ExitStatus != 11 {
		t.Fatal(report)
	}
	old := runTuneProof
	defer func() { runTuneProof = old }()
	var limits []time.Duration
	runTuneProof = func(_ context.Context, opts prove.Options) prove.Result {
		limits = append(limits, opts.Timeout)
		return prove.Result{Status: "failed", ExitStatus: 12, Reason: "stopped by the test"}
	}
	f := &tuneCAO{writer: suiteWriter("verify_timeout = '100ms'\n", "fast", "slow")}
	tuneCall(t, "verify", "-again", "-worktree", root, "-cao", f.serve(t).URL)
	if len(limits) != 1 || limits[0] != 100*time.Millisecond {
		t.Fatal(limits)
	}
}

func TestAVerifyTimeoutMustBeADurationAboveZero(t *testing.T) {
	for _, bad := range []string{"verify_timeout = 'soon'\n", "verify_timeout = '0s'\n", "verify_timeout = '-1m'\n"} {
		root := suiteFixture(t)
		proofWrite(t, root, loop.ConfigFile, suiteConfig(bad), 0644)
		if code, out := proofCall(t, root); code != 10 || !strings.Contains(out, "test.verify_timeout") {
			t.Fatal(bad, code, out)
		}
	}
}

func suiteClock(t *testing.T) *time.Time {
	t.Helper()
	now := time.Now()
	old := tuneClock
	tuneClock = func() time.Time { return now }
	t.Cleanup(func() { tuneClock = old })
	return &now
}

func TestABreakThatTimesOutDoesNotRaiseTheVerifyTimeout(t *testing.T) {
	root := suiteFixture(t)
	f := &tuneCAO{writer: suiteWriter("", "fast", "slow")}
	old := runTuneProof
	defer func() { runTuneProof = old }()
	calls := 0
	runTuneProof = func(_ context.Context, opts prove.Options) prove.Result {
		calls++
		passed := prove.RunResult{Status: "passed"}
		return prove.Result{Status: "timeout", ExitStatus: 3, Reason: "break source.patch: verify timed out", Runs: []prove.RunResult{passed, passed}}
	}
	code, out := tuneCall(t, "verify", "-worktree", root, "-cao", f.serve(t).URL)
	if code == 0 || calls != 1 || strings.Contains(out, "raised") || !strings.Contains(out, "break source.patch: verify timed out") {
		t.Fatal(code, calls, out)
	}
}

func TestATimeBoundedTuneKeepsItsTimeoutCarriesTheTimeTargetToBothChairsAndMayLeaveASlowSuiteOut(t *testing.T) {
	root := suiteFixture(t)
	f := &tuneCAO{writer: suiteWriter("verify_time_bounded = true\n", "fast", "slow")}
	code, out := tuneCall(t, "verify", "-worktree", root, "-cao", f.serve(t).URL, "-time-bounded", "-verify-timeout", "200ms")
	if code == 0 || strings.Contains(out, "raised") || !strings.Contains(out, "timeout: clean run 1: verify timed out") {
		t.Fatal(code, out)
	}
	if len(f.prompts) != 1 || !strings.Contains(f.prompts[0], "about two minutes") || strings.Contains(f.prompts[0], "may take as long as CI") {
		t.Fatal(f.prompts)
	}
	f = &tuneCAO{writer: suiteWriter("verify_time_bounded = true\n", "fast")}
	code, out = tuneCall(t, "verify", "-again", "-worktree", root, "-cao", f.serve(t).URL, "-time-bounded", "-verify-timeout", "10s")
	if code != 0 || len(f.prompts) != 2 || !strings.Contains(f.prompts[1], "This verify is time-bounded") || !strings.Contains(out, "time-bounded: tests that do not fit the time target may be left out") {
		t.Fatal(code, out, f.prompts)
	}
}

func TestOnlyTheCallerMakesAVerifyTimeBoundedAndTheConfigMustRecordIt(t *testing.T) {
	for _, c := range []struct {
		flag    bool
		written string
		want    string
	}{
		{true, "", "[test] verify_time_bounded must be true"},
		{false, "verify_time_bounded = true\n", "[test] verify_time_bounded must not be set"},
	} {
		root := suiteFixture(t)
		f := &tuneCAO{writer: suiteWriter(c.written, "fast")}
		args := []string{"verify", "-worktree", root, "-cao", f.serve(t).URL, "-verify-timeout", "5s"}
		if c.flag {
			args = append(args, "-time-bounded")
		}
		if code, out := tuneCall(t, args...); code == 0 || !strings.Contains(out, c.want) || !initrepo.IsStub(initRead(t, root, initrepo.ScriptFile)) {
			t.Fatal(c, code, out)
		}
	}
}

func TestByDefaultBothChairsAreToldVerifyMirrorsCIAndThatTimeIsNoReasonToLeaveATestOut(t *testing.T) {
	root := suiteFixture(t)
	f := &tuneCAO{writer: suiteWriter("", "fast", "slow")}
	if code, out := tuneCall(t, "verify", "-worktree", root, "-cao", f.serve(t).URL, "-verify-timeout", "10s"); code != 0 {
		t.Fatal(code, out)
	}
	if len(f.prompts) != 2 {
		t.Fatal(len(f.prompts))
	}
	for i, prompt := range f.prompts {
		for _, unwanted := range []string{"about two minutes", "never more than five", "time target"} {
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
}

func TestSuitesRequiredByFlagMustBeRecordedInTheConfigTuneWrites(t *testing.T) {
	for _, recorded := range []bool{false, true} {
		root := suiteFixture(t)
		config := ""
		if recorded {
			config = suiteRequired
		}
		f := &tuneCAO{writer: suiteWriter(config, "fast", "slow")}
		code, out := tuneCall(t, "verify", "-worktree", root, "-cao", f.serve(t).URL, "-require", "fast", "-require", "slow", "-verify-timeout", "10s")
		published := initRead(t, root, loop.ConfigFile)
		if recorded && (code != 0 || !strings.Contains(published, suiteRequired) || !strings.Contains(out, "required suites: fast, slow")) {
			t.Fatal(code, out, published)
		}
		if !recorded && (code == 0 || !strings.Contains(out, `[test] verify_required must list "fast"`) || strings.Contains(published, "verify_required")) {
			t.Fatal(code, out, published)
		}
		if len(f.prompts) == 0 || !strings.Contains(f.prompts[0], "Required suites: fast, slow.") {
			t.Fatal(f.prompts)
		}
	}
}

func TestProveCountsARequiredSuiteOnlyWhenItsStepRanInBothCleanRuns(t *testing.T) {
	for name, c := range map[string]struct {
		steps string
		exit  int
	}{
		"no step":                 {"run 'fast' 'sh suites/fast.sh'\n", 14},
		"step in a dead branch":   {"run 'fast' 'sh suites/fast.sh'\nif false; then\nrun 'slow' 'sh suites/slow.sh'\nfi\n", 14},
		"step in a here-document": {"run 'fast' 'sh suites/fast.sh'\ncat >/dev/null <<END\nrun 'slow' 'sh suites/slow.sh'\nEND\n", 14},
		"step in a comment":       {"run 'fast' 'sh suites/fast.sh'\n# run 'slow' 'sh suites/slow.sh'\n", 14},
		"plain step":              {"run 'fast' 'sh suites/fast.sh'\nrun 'slow' 'sh suites/slow.sh'\n", 11},
		"two spaces after run":    {"run 'fast' 'sh suites/fast.sh'\nrun  'slow' 'sh suites/slow.sh'\n", 11},
		"a tab after run":         {"run 'fast' 'sh suites/fast.sh'\nrun\t'slow' 'sh suites/slow.sh'\n", 11},
		"step after and":          {"run 'fast' 'sh suites/fast.sh'\ntrue && run 'slow' 'sh suites/slow.sh'\n", 11},
	} {
		t.Run(name, func(t *testing.T) {
			root := suiteFixture(t)
			proofWrite(t, root, loop.ConfigFile, suiteConfig(suiteRequired), 0644)
			proofWrite(t, root, initrepo.ScriptFile, suiteSteps(c.steps), 0755)
			report := proofJSON(t, root)
			if report.ExitStatus != c.exit {
				t.Fatal(report)
			}
			if c.exit == 14 && (report.Status != "suite_not_run" || !strings.Contains(report.Reason, `required suite "slow" did not run in clean run 1; give it its own step in scripts/verify: run 'slow' 'COMMAND'`)) {
				t.Fatal(report)
			}
		})
	}
}

func TestProveSaysAWrapperThatRecordsNoStepsCannotShowARequiredSuiteRan(t *testing.T) {
	root := suiteFixture(t)
	proofWrite(t, root, loop.ConfigFile, suiteConfig(suiteRequired), 0644)
	silent := strings.Replace(suiteScript("fast", "slow"), initrepo.StepsEnv, "UNRECORDED", -1)
	proofWrite(t, root, initrepo.ScriptFile, silent, 0755)
	report := proofJSON(t, root)
	if report.ExitStatus != 14 || !strings.Contains(report.Reason, "verify recorded no steps; its run wrapper must append each label it runs to the file named by $"+initrepo.StepsEnv) {
		t.Fatal(report)
	}
	proofWrite(t, root, loop.ConfigFile, suiteConfig(""), 0644)
	if report := proofJSON(t, root); report.ExitStatus != 11 {
		t.Fatal(report)
	}
}

func TestABreakOnlyTheSlowSuiteCatchesSurvivesWhenTheSlowStepRunsSomethingElse(t *testing.T) {
	root := suiteFixture(t)
	proofWrite(t, root, loop.ConfigFile, suiteConfig(suiteRequired), 0644)
	patch := filepath.Join(t.TempDir(), "slow-only.patch")
	if err := os.WriteFile(patch, []byte(suiteSlowOnlyBreak), 0644); err != nil {
		t.Fatal(err)
	}
	fastOnly := suitePatch(t, "fast-only.patch", suiteTestBreak)
	for steps, exit := range map[string]int{
		"run 'fast' 'sh suites/fast.sh'\nrun 'slow' 'true'\n":                         13,
		"run 'fast' 'sh suites/fast.sh'\nrun 'slow' 'sh suites/slow.sh || true'\n":    13,
		"run 'fast' 'sh suites/fast.sh'\nrun 'slow' 'sh suites/slow.sh'\n":            0,
		"run 'fast' 'sh suites/fast.sh'\nrun 'slow' 'sh suites/slow.sh' || true\n":    0,
		"run 'fast' 'sh suites/fast.sh'\nrun 'slow' 'sh suites/fast.sh'\n":            13,
		"run 'slow' 'sh suites/slow.sh'\nrun 'fast' 'sh suites/fast.sh' || exit 99\n": 0,
	} {
		proofWrite(t, root, initrepo.ScriptFile, suiteSteps(steps), 0755)
		if report := proofJSON(t, root, "-break", patch, "-break", fastOnly); report.ExitStatus != exit {
			t.Fatal(steps, report)
		}
	}
}

func TestARoundThatRenamesARequiredStepIsProvedAgainAndNotPublished(t *testing.T) {
	root := suiteFixture(t)
	tuneRounds(t)
	proofWrite(t, root, loop.ConfigFile, suiteConfig(suiteRequired), 0644)
	round := 0
	f := &tuneCAO{mode: "label revision", writer: func(t *testing.T, req cao.StepRequest, patchDir string) string {
		round++
		script := suiteScript("fast", "slow")
		if round == 2 {
			script = strings.Replace(script, "run 'slow' ", "run 'integration' ", 1)
		}
		proofWrite(t, req.WorkingDirectory, initrepo.ScriptFile, script, 0755)
		proofWrite(t, req.WorkingDirectory, loop.ConfigFile, suiteConfig(suiteRequired), 0644)
		return suiteReply(t, patchDir, "sh suites/fast.sh && sh suites/slow.sh")
	}}
	code, out := tuneCall(t, "verify", "-worktree", root, "-cao", f.serve(t).URL, "-verify-timeout", "10s")
	if round != 2 || strings.Contains(out, "proof kept") || !strings.Contains(out, `required suite "slow" did not run in clean run 1`) {
		t.Fatal(code, round, out)
	}
	if published := initRead(t, root, initrepo.ScriptFile); strings.Contains(published, "run 'integration'") {
		t.Fatal(published)
	}
	if report := proofJSON(t, root); report.ExitStatus == 14 {
		t.Fatal(report)
	}
}

func TestTheGuideFollowsTheRequireAndTimeBoundedFlags(t *testing.T) {
	code, out := tuneCall(t, "verify", "-guide", "-require", "integration")
	if code != 0 || !strings.Contains(out, "may take as long as CI") || !strings.Contains(out, "Required suites: integration.") || strings.Contains(out, "about two minutes") {
		t.Fatal(code, out)
	}
	code, out = tuneCall(t, "verify", "-guide", "-time-bounded")
	if code != 0 || !strings.Contains(out, "about two minutes") || strings.Contains(out, "may take as long as CI") {
		t.Fatal(code, out)
	}
}

func TestRequiredSuiteNamesMustBeUsableAsStepLabels(t *testing.T) {
	for _, bad := range []string{"verify_required = ['']\n", "verify_required = ['a', 'a']\n", "verify_required = [\"it's\"]\n", "verify_required = [\"two\\nlines\"]\n"} {
		root := suiteFixture(t)
		proofWrite(t, root, loop.ConfigFile, suiteConfig(bad), 0644)
		if code, out := proofCall(t, root); code != 10 || !strings.Contains(out, "test.verify_required") {
			t.Fatal(bad, code, out)
		}
		if _, _, err := loop.ResolveHooks(root, ""); err == nil || !strings.Contains(err.Error(), "test.verify_required") {
			t.Fatal(bad, err)
		}
	}
}
