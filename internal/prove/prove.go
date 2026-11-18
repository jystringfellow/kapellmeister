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

type Options struct {
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
	Command    string        `json:"command"`
	Status     string        `json:"status"`
	ExitStatus int           `json:"exit_status"`
	Reason     string        `json:"reason"`
	Runs       []RunResult   `json:"runs"`
	Breaks     []BreakResult `json:"breaks"`

	Flaky    []FlakyStep `json:"flaky,omitempty"`
	Unbroken []string    `json:"unbroken,omitempty"`

	KnownFailures        []string `json:"known_failures,omitempty"`
	KnownFailuresPassing []string `json:"known_failures_passing,omitempty"`

	TimeoutMS      int64 `json:"timeout_ms,omitempty"`
	BreakTimeoutMS int64 `json:"break_timeout_ms,omitempty"`
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
	command, _, err := loop.ResolveHooks(root, "")
	if err != nil {
		return refuse(err.Error())
	}
	result.Command = command
	if command == "" {
		return refuse("verify is not configured")
	}
	setup, err := readSetup(root)
	if err != nil {
		return refuse(err.Error())
	}
	if strings.TrimPrefix(command, "./") == initrepo.ScriptFile && initrepo.IsStub(string(setup[initrepo.ScriptFile].body)) {
		return refuse("verify is unfinished: scripts/verify is a stub; write the command, then run km prove")
	}
	test, err := loop.ResolveTest(root)
	if err != nil {
		return refuse(err.Error())
	}
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
	runVerify := func(dir string, limit time.Duration) (initrepo.VerifyResult, []Step, string, error) {
		tmp := filepath.Join(scratch, filepath.Base(dir)+"-tmp")
		if err := os.Mkdir(tmp, 0700); err != nil {
			return initrepo.VerifyResult{}, nil, "", err
		}
		record := filepath.Join(scratch, filepath.Base(dir)+"-steps")
		prefix := "unset GIT_DIR GIT_WORK_TREE GIT_INDEX_FILE GIT_ALTERNATE_OBJECT_DIRECTORIES GIT_COMMON_DIR GIT_OBJECT_DIRECTORY\nTMPDIR=" + quote(tmp) + "\n" + initrepo.StepsEnv + "=" + quote(record) + "\nexport TMPDIR " + initrepo.StepsEnv + "\n"
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
		before, err := setupSnapshot(dir)
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
			after, err := setupSnapshot(dir)
			if err != nil {
				return fail(err)
			}
			if !reflect.DeepEqual(before, after) {
				reason = fmt.Sprintf("break %s: patch touches setup files (.kapellmeister/ or scripts/verify)", patch)
			}
		}
		if reason != "" {
			result.Breaks[i].Status, result.Breaks[i].Reason = "refused", reason
			return refuse(reason)
		}
		breakDirs[i] = dir
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
			return finish("timeout", 3, name+": verify timed out")
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
		reason := "verify is flaky, not a failed proof: only one clean run passed"
		if failed.FailedStep != "" {
			result.Flaky = []FlakyStep{{Step: failed.FailedStep, FailedIn: failed.Name, PassedIn: passed.Name, Output: failed.Output}}
			reason = fmt.Sprintf("verify is flaky, not a failed proof: step %q failed in %s and passed in %s", failed.FailedStep, failed.Name, passed.Name)
		}
		return finish("flaky", 12, reason)
	}
	if failures == 2 {
		reason := "verify failed on both clean copies"
		if first, second := result.Runs[0].FailedStep, result.Runs[1].FailedStep; first != "" && first == second {
			reason += fmt.Sprintf(" at step %q", first)
		} else if first != "" && second != "" {
			reason += fmt.Sprintf(" at steps %q and %q", first, second)
		}
		return finish("failed", 12, reason)
	}
	for _, suite := range test.VerifyRequired {
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
			if !slices.Contains(test.VerifyKnownFailures, known.Label) {
				return finish("baseline_mismatch", 16, fmt.Sprintf("scripts/verify has a known_failure step %q that [test] verify_known_failures does not list; list it there so the baseline is named, or make it a run step", known.Label))
			}
		}
	}
	for _, label := range test.VerifyKnownFailures {
		passes := func(run RunResult) bool {
			return !slices.ContainsFunc(run.KnownFailures, func(known loop.KnownFailure) bool { return known.Label == label && !known.Passed })
		}
		if passes(result.Runs[0]) && passes(result.Runs[1]) {
			result.KnownFailuresPassing = append(result.KnownFailuresPassing, label)
		} else {
			result.KnownFailures = append(result.KnownFailures, label)
		}
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
			return finish("timeout", 3, "break "+patch+": verify timed out")
		}
		if run.ExitCode == 0 {
			b.Status = "survives"
			b.Reason = "patch did not make verify fail"
			survived = append(survived, patch)
		}
	}
	if len(survived) > 0 {
		return finish("survived", 13, "break patch did not make verify fail: "+strings.Join(survived, ", "))
	}
	if len(options.Breaks) == 0 {
		return finish("not_proved", 11, "passes, not proved; a break patch is needed: -break PATCH")
	}
	var unbroken []string
	for _, suite := range test.VerifyRequired {
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
	return finish("proved", 0, "passed twice and every break patch made verify fail")
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
