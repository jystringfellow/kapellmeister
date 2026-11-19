package prove

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jystringfellow/kapellmeister/internal/initrepo"
	"github.com/jystringfellow/kapellmeister/internal/loop"
)

const (
	LevelVerify = "verify"
	LevelStrict = "strict"
)

type Options struct {
	Level    string
	Worktree string
	Breaks   []string
	Timeout  time.Duration
}

const DefaultTimeout = 10 * time.Minute

type RunResult struct {
	Name       string `json:"name"`
	Status     string `json:"status"`
	ExitCode   int    `json:"exit_code"`
	DurationMS int64  `json:"duration_ms"`
	Output     string `json:"output"`
	Steps      []Step `json:"steps,omitempty"`
	FailedStep string `json:"failed_step,omitempty"`

	KnownFailures []loop.KnownFailure `json:"known_failures,omitempty"`
}

type Step struct {
	Label      string `json:"label"`
	DurationMS int64  `json:"duration_ms"`
}

type FlakyStep struct {
	Step     string `json:"step"`
	FailedIn string `json:"failed_in"`
	PassedIn string `json:"passed_in"`
	Output   string `json:"output"`
}

type BreakResult struct {
	Patch      string `json:"patch"`
	Status     string `json:"status"`
	ExitCode   *int   `json:"exit_code,omitempty"`
	DurationMS *int64 `json:"duration_ms,omitempty"`
	Output     string `json:"output"`
	Reason     string `json:"reason"`
	FailedStep string `json:"failed_step,omitempty"`
}

type Result struct {
	Worktree   string        `json:"worktree"`
	Level      string        `json:"level,omitempty"`
	Command    string        `json:"command"`
	Status     string        `json:"status"`
	ExitStatus int           `json:"exit_status"`
	Reason     string        `json:"reason"`
	Runs       []RunResult   `json:"runs"`
	Breaks     []BreakResult `json:"breaks"`

	Excluded       []loop.Exclusion `json:"excluded,omitempty"`
	Flaky          []FlakyStep      `json:"flaky,omitempty"`
	Unbroken       []string         `json:"unbroken,omitempty"`
	TimeoutMS      int64            `json:"timeout_ms,omitempty"`
	BreakTimeoutMS int64            `json:"break_timeout_ms,omitempty"`

	KnownFailures         []string `json:"known_failures,omitempty"`
	KnownFailuresPassing  []string `json:"known_failures_passing,omitempty"`
	KnownFailuresFlaky    []string `json:"known_failures_flaky,omitempty"`
	KnownFailuresUnlisted []string `json:"known_failures_unlisted,omitempty"`
}

func Prove(ctx context.Context, options Options) (result Result) {
	result = Result{Worktree: options.Worktree, Runs: []RunResult{}, Breaks: []BreakResult{}}
	for _, patch := range options.Breaks {
		result.Breaks = append(result.Breaks, BreakResult{Patch: patch, Status: "skipped"})
	}
	finish := func(status string, code int, reason string) Result {
		result.Status, result.ExitStatus, result.Reason = status, code, reason
		if ctx.Err() != nil {
			result.Status, result.ExitStatus, result.Reason = "interrupted", 1, "interrupted: "+context.Cause(ctx).Error()
		}
		return result
	}
	fail := func(err error) Result { return finish("error", 1, bounded(err.Error())) }
	refuse := func(reason string) Result { return finish("refused", 10, reason) }
	root, err := filepath.Abs(options.Worktree)
	if err != nil {
		return fail(err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return refuse(err.Error())
	}
	result.Worktree = root
	top, err := git(ctx, root, "rev-parse", "--show-toplevel")
	if err != nil {
		return refuse("worktree is not a Git checkout with a HEAD commit: " + err.Error())
	}
	top, err = filepath.EvalSymlinks(top)
	if err != nil {
		return fail(err)
	}
	if top != root {
		return refuse("worktree must be the checkout's top level")
	}
	head, err := git(ctx, root, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return refuse("worktree has no HEAD commit: " + err.Error())
	}
	level := options.Level
	if level == "" {
		level = LevelVerify
	}
	if level != LevelVerify && level != LevelStrict {
		return refuse(fmt.Sprintf("level must be %s or %s, not %q", LevelVerify, LevelStrict, level))
	}
	command, _, err := loop.ResolveHooks(root, "")
	if err != nil {
		return refuse(err.Error())
	}
	levels, err := loop.ResolveTest(root)
	if err != nil {
		return refuse(err.Error())
	}
	if level == LevelStrict {
		command = levels.Strict
	}
	result.Command, result.Level = command, level
	scripts := loop.StrictScripts(levels)
	if command == "" && level == LevelStrict {
		return refuse("strict is not configured: set [test] strict")
	}
	if command == "" {
		return refuse("verify is not configured")
	}
	setup, err := readSetup(root, scripts...)
	if err != nil {
		return refuse(err.Error())
	}
	if level == LevelVerify && strings.TrimPrefix(command, "./") == initrepo.ScriptFile && initrepo.IsStub(string(setup[initrepo.ScriptFile].body)) {
		return refuse("verify is unfinished: scripts/verify is a stub; write the command, then run km prove")
	}
	if initrepo.SetsSkip(command) || initrepo.SetsSkip(string(setup[initrepo.ScriptFile].body)) {
		return refuse(fmt.Sprintf("verify must not set %s: only km sets it, for its own run without network", initrepo.SkipEnv))
	}
	test, err := loop.ResolveTest(root)
	if err != nil {
		return refuse(err.Error())
	}
	result.Excluded = test.Excluded
	timeout := options.Timeout
	if timeout == 0 {
		timeout = test.Timeout(DefaultTimeout)
	}
	result.TimeoutMS = timeout.Milliseconds()
	patches := make([][]byte, len(options.Breaks))
	for i, patch := range options.Breaks {
		data, err := os.ReadFile(patch)
		if err != nil || len(strings.TrimSpace(string(data))) == 0 {
			reason := fmt.Sprintf("break %s: empty patch", patch)
			if err != nil {
				reason = fmt.Sprintf("break %s: %s", patch, err)
			}
			result.Breaks[i].Status, result.Breaks[i].Reason = "refused", reason
			return refuse(reason)
		}
		patches[i] = data
	}
	scratch, err := os.MkdirTemp("", "km-prove-")
	if err != nil {
		return fail(err)
	}
	defer func() {
		if err := removeScratch(scratch); err != nil {
			result.Status, result.ExitStatus, result.Reason = "error", 1, "remove scratch copies: "+err.Error()
		}
	}()
	resolved, err := filepath.EvalSymlinks(scratch)
	if err != nil {
		return fail(err)
	}
	scratch = resolved
	rel, err := filepath.Rel(root, scratch)
	if err != nil {
		return fail(err)
	}
	if rel == "." || rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return refuse("scratch copies must live outside the checkout")
	}
	quote := func(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'" }
	knownIn := func(dir string) []loop.KnownFailure {
		return loop.ReadKnownFailures(filepath.Join(scratch, filepath.Base(dir)+"-steps"))
	}
	skip := filepath.Join(scratch, "skip")
	if err := os.WriteFile(skip, nil, 0600); err != nil {
		return fail(err)
	}
	runVerify := func(dir string, limit time.Duration) (initrepo.VerifyResult, []Step, string, error) {
		tmp := filepath.Join(scratch, filepath.Base(dir)+"-tmp")
		if err := os.Mkdir(tmp, 0700); err != nil {
			return initrepo.VerifyResult{}, nil, "", err
		}
		record := filepath.Join(scratch, filepath.Base(dir)+"-steps")
		prefix := "unset GIT_DIR GIT_WORK_TREE GIT_INDEX_FILE GIT_ALTERNATE_OBJECT_DIRECTORIES GIT_COMMON_DIR GIT_OBJECT_DIRECTORY\nTMPDIR=" + quote(tmp) + "\n" + initrepo.StepsEnv + "=" + quote(record) + "\n" + initrepo.SkipEnv + "=" + quote(skip) + "\nexport TMPDIR " + initrepo.StepsEnv + " " + initrepo.SkipEnv + "\n"
		stop, timed := make(chan struct{}), make(chan []Step)
		go func() { timed <- timeSteps(record, stop) }()
		run := initrepo.Run(ctx, dir, prefix+command, limit)
		close(stop)
		failed, _ := os.ReadFile(record + ".failed")
		return run, <-timed, strings.TrimSuffix(string(failed), "\n"), nil
	}
	breakDirs := make([]string, len(options.Breaks))
	for i, patch := range options.Breaks {
		dir, err := cleanCopy(ctx, root, head, scratch, fmt.Sprintf("break-%d", i+1), setup)
		if err != nil {
			return fail(err)
		}
		before, err := setupSnapshot(dir, scripts...)
		if err != nil {
			return fail(err)
		}
		patchFile := filepath.Join(scratch, "break.patch")
		if err := os.WriteFile(patchFile, patches[i], 0600); err != nil {
			return fail(err)
		}
		_, applyErr := git(ctx, dir, "apply", "--whitespace=nowarn", patchFile)
		reason := ""
		if applyErr != nil {
			reason = fmt.Sprintf("break %s: patch did not apply: %s", patch, applyErr)
		} else {
			after, err := setupSnapshot(dir, scripts...)
			if err != nil {
				return fail(err)
			}
			if !reflect.DeepEqual(before, after) {
				reason = fmt.Sprintf("break %s: patch touches setup files (%s)", patch, setupNames(scripts))
			}
		}
		if reason != "" {
			result.Breaks[i].Status, result.Breaks[i].Reason = "refused", reason
			return refuse(reason)
		}
		breakDirs[i] = dir
	}
	if level == LevelStrict && test.StrictReady != "" {
		if fault := loop.ProbeFault(root, test.StrictReady); fault != "" {
			return refuse("the strict_ready probe is broken: " + fault + "; fix [test] strict_ready")
		}
		dir, err := cleanCopy(ctx, root, head, scratch, "ready", setup)
		if err != nil {
			return fail(err)
		}
		probe := initrepo.Run(ctx, dir, test.StrictReady, timeout)
		if ctx.Err() != nil {
			return finish("interrupted", 1, "interrupted")
		}
		if probe.Status != "passed" {
			outcome := fmt.Sprintf("exited %d", probe.ExitCode)
			if probe.Status == "timed_out" {
				outcome = "timed out at " + timeout.String()
			}
			return finish("strict_not_run", 17, fmt.Sprintf("strict not run: %s %s: %s", test.StrictReady, outcome, bounded(probe.Output)))
		}
	}
	required := test.VerifyRequired
	if level != LevelVerify {
		required = nil
	}
	failures := 0
	for i := 1; i <= 2; i++ {
		name := fmt.Sprintf("clean run %d", i)
		dir, err := cleanCopy(ctx, root, head, scratch, fmt.Sprintf("clean-%d", i), setup)
		if err != nil {
			return fail(err)
		}
		run, steps, failedStep, err := runVerify(dir, timeout)
		if err != nil {
			return fail(err)
		}
		result.Runs = append(result.Runs, RunResult{Name: name, Status: run.Status, ExitCode: run.ExitCode, DurationMS: run.DurationMS, Output: run.Output, Steps: steps, FailedStep: failedStep, KnownFailures: knownIn(dir)})
		if ctx.Err() != nil {
			return finish("interrupted", 1, "interrupted")
		}
		if run.Status == "timed_out" {
			return finish("timeout", 3, name+": "+level+" timed out")
		}
		if run.ExitCode != 0 {
			failures++
		}
	}
	if failures == 1 {
		failed, passed := result.Runs[0], result.Runs[1]
		if failed.ExitCode == 0 {
			failed, passed = passed, failed
		}
		reason := level + " is flaky, not a failed proof: only one clean run passed"
		if failed.FailedStep != "" {
			result.Flaky = []FlakyStep{{Step: failed.FailedStep, FailedIn: failed.Name, PassedIn: passed.Name, Output: failed.Output}}
			reason = fmt.Sprintf("%s is flaky, not a failed proof: step %q failed in %s and passed in %s", level, failed.FailedStep, failed.Name, passed.Name)
		}
		return finish("flaky", 12, reason)
	}
	if failures == 2 {
		reason := level + " failed on both clean copies"
		if first, second := result.Runs[0].FailedStep, result.Runs[1].FailedStep; first != "" && first == second {
			reason += fmt.Sprintf(" at step %q", first)
		} else if first != "" && second != "" {
			reason += fmt.Sprintf(" at steps %q and %q", first, second)
		}
		return finish("failed", 12, reason)
	}
	for _, suite := range required {
		for _, run := range result.Runs {
			if slices.ContainsFunc(run.Steps, func(step Step) bool { return step.Label == suite }) {
				continue
			}
			reason := fmt.Sprintf("required suite %q did not run in %s; give it its own step in scripts/verify: run '%s' 'COMMAND'", suite, run.Name, suite)
			if len(run.Steps) == 0 {
				reason = fmt.Sprintf("required suite %q did not run in %s: verify recorded no steps; its run wrapper must append each label it runs to the file named by $%s, as the wrapper km init writes does", suite, run.Name, initrepo.StepsEnv)
			}
			return finish("suite_not_run", 14, reason)
		}
	}
	for _, run := range result.Runs {
		for _, label := range test.VerifyKnownFailures {
			if !slices.ContainsFunc(run.KnownFailures, func(known loop.KnownFailure) bool { return known.Label == label }) {
				return finish("baseline_mismatch", 16, fmt.Sprintf("known failure %q is listed in [test] verify_known_failures but no known_failure step with that label ran in %s; give it its own step in scripts/verify: known_failure '%s' 'COMMAND', or remove it from the list; a scripts/verify written before km had known_failure must add that function from the wrapper km init writes", label, run.Name, label))
			}
		}
		for _, known := range run.KnownFailures {
			if !slices.Contains(test.VerifyKnownFailures, known.Label) && !slices.Contains(result.KnownFailuresUnlisted, known.Label) {
				result.KnownFailuresUnlisted = append(result.KnownFailuresUnlisted, known.Label)
			}
		}
	}
	if len(result.KnownFailuresUnlisted) > 0 {
		return finish("baseline_mismatch", 16, fmt.Sprintf("scripts/verify has a known_failure step %q that [test] verify_known_failures does not list; list it there so the baseline is named, or make it a run step", result.KnownFailuresUnlisted[0]))
	}
	for _, label := range test.VerifyKnownFailures {
		passes := func(run RunResult) bool {
			return !slices.ContainsFunc(run.KnownFailures, func(known loop.KnownFailure) bool { return known.Label == label && !known.Passed })
		}
		first, second := passes(result.Runs[0]), passes(result.Runs[1])
		switch {
		case first && second:
			result.KnownFailuresPassing = append(result.KnownFailuresPassing, label)
		case first != second:
			result.KnownFailuresFlaky = append(result.KnownFailuresFlaky, label)
			result.KnownFailures = append(result.KnownFailures, label)
		default:
			result.KnownFailures = append(result.KnownFailures, label)
		}
	}
	if len(result.KnownFailuresPassing) > 0 {
		var reasons []string
		for _, label := range result.KnownFailuresPassing {
			reasons = append(reasons, fmt.Sprintf("known failure %q passed in both clean runs: remove %q from [test] verify_known_failures and make its step a run step", label, label))
		}
		return finish("baseline_mismatch", 16, strings.Join(reasons, "; "))
	}
	breakTimeout := timeout
	if len(options.Breaks) > 0 {
		breakTimeout = max(timeout, 2*time.Duration(max(result.Runs[0].DurationMS, result.Runs[1].DurationMS))*time.Millisecond)
		result.BreakTimeoutMS = breakTimeout.Milliseconds()
	}
	survived := []string{}
	for i, patch := range options.Breaks {
		dir := breakDirs[i]
		run, _, failedStep, err := runVerify(dir, breakTimeout)
		if err != nil {
			return fail(err)
		}
		b := &result.Breaks[i]
		b.Status, b.ExitCode, b.DurationMS, b.Output, b.FailedStep = "breaks", &run.ExitCode, &run.DurationMS, run.Output, failedStep
		if ctx.Err() != nil {
			b.Status = "interrupted"
			return finish("interrupted", 1, "interrupted")
		}
		if run.Status == "timed_out" {
			b.Status = "timeout"
			return finish("timeout", 3, "break "+patch+": "+level+" timed out")
		}
		if run.ExitCode == 0 {
			b.Status = "survives"
			b.Reason = "patch did not make " + level + " fail"
			survived = append(survived, patch)
		}
	}
	if len(survived) > 0 {
		return finish("survived", 13, "break patch did not make "+level+" fail: "+strings.Join(survived, ", "))
	}
	if len(options.Breaks) == 0 {
		return finish("not_proved", 11, "passes, not proved; a break patch is needed: -break PATCH")
	}
	var unbroken []string
	for _, suite := range required {
		if !slices.ContainsFunc(result.Breaks, func(b BreakResult) bool { return b.FailedStep == suite }) {
			result.Unbroken = append(result.Unbroken, suite)
			unbroken = append(unbroken, strconv.Quote(suite))
		}
	}
	if len(unbroken) > 0 {
		reason := fmt.Sprintf("required suites with no break patch that fails in their step: %s; each needs a break patch that its step is the first to catch", strings.Join(unbroken, ", "))
		if !slices.ContainsFunc(result.Breaks, func(b BreakResult) bool { return b.FailedStep != "" }) {
			reason += fmt.Sprintf("; verify did not record which step failed: its run wrapper must write the failing label to the file named by $%s with .failed added, as the wrapper km init writes does", initrepo.StepsEnv)
		}
		return finish("suite_unbroken", 15, reason)
	}
	return finish("proved", 0, "passed twice and every break patch made "+level+" fail")
}

func timeSteps(record string, stop <-chan struct{}) []Step {
	start := time.Now()
	var steps []Step
	var last time.Duration
	read := func() {
		data, err := os.ReadFile(record)
		if err != nil {
			return
		}
		labels := strings.Split(string(data), "\n")
		labels = labels[:len(labels)-1]
		for _, label := range labels[min(len(steps), len(labels)):] {
			now := time.Since(start)
			steps = append(steps, Step{Label: label, DurationMS: (now - last).Milliseconds()})
			last = now
		}
	}
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-tick.C:
			read()
		case <-stop:
			read()
			return steps
		}
	}
}

func removeScratch(root string) error {
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err == nil && entry.IsDir() {
			_ = os.Chmod(path, 0700)
		}
		return nil
	})
	return os.RemoveAll(root)
}

func setupNames(scripts []string) string {
	names := append([]string{".kapellmeister/", "scripts/verify"}, scripts...)
	if len(names) == 2 {
		return names[0] + " or " + names[1]
	}
	return strings.Join(names[:len(names)-1], ", ") + " or " + names[len(names)-1]
}
