package prove

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
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

type RunResult struct {
	Name       string `json:"name"`
	Status     string `json:"status"`
	ExitCode   int    `json:"exit_code"`
	DurationMS int64  `json:"duration_ms"`
	Output     string `json:"output"`
}

type BreakResult struct {
	Patch      string `json:"patch"`
	Status     string `json:"status"`
	ExitCode   *int   `json:"exit_code,omitempty"`
	DurationMS *int64 `json:"duration_ms,omitempty"`
	Output     string `json:"output"`
	Reason     string `json:"reason"`
}

type Result struct {
	Worktree   string        `json:"worktree"`
	Command    string        `json:"command"`
	Status     string        `json:"status"`
	ExitStatus int           `json:"exit_status"`
	Reason     string        `json:"reason"`
	Runs       []RunResult   `json:"runs"`
	Breaks     []BreakResult `json:"breaks"`
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
	runVerify := func(dir string) (initrepo.VerifyResult, error) {
		tmp := filepath.Join(scratch, filepath.Base(dir)+"-tmp")
		if err := os.Mkdir(tmp, 0700); err != nil {
			return initrepo.VerifyResult{}, err
		}
		prefix := "unset GIT_DIR GIT_WORK_TREE GIT_INDEX_FILE GIT_ALTERNATE_OBJECT_DIRECTORIES GIT_COMMON_DIR GIT_OBJECT_DIRECTORY\nTMPDIR='" + strings.ReplaceAll(tmp, "'", "'\"'\"'") + "'\nexport TMPDIR\n"
		return initrepo.Run(ctx, dir, prefix+command, options.Timeout), nil
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
		run, err := runVerify(dir)
		if err != nil {
			return fail(err)
		}
		result.Runs = append(result.Runs, RunResult{Name: name, Status: run.Status, ExitCode: run.ExitCode, DurationMS: run.DurationMS, Output: run.Output})
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
		return finish("flaky", 12, "verify is flaky: only one clean run passed")
	}
	if failures == 2 {
		return finish("failed", 12, "verify failed on both clean copies")
	}
	survived := []string{}
	for i, patch := range options.Breaks {
		dir := breakDirs[i]
		run, err := runVerify(dir)
		if err != nil {
			return fail(err)
		}
		b := &result.Breaks[i]
		b.Status, b.ExitCode, b.DurationMS, b.Output = "breaks", &run.ExitCode, &run.DurationMS, run.Output
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
	return finish("proved", 0, "passed twice and every break patch made verify fail")
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
