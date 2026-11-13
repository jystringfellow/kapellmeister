package initrepo

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jystringfellow/kapellmeister/internal/loop"
)

const ScriptFile = "scripts/verify"

type Options struct {
	Worktree string
	Verify   string
	Check    string
	DryRun   bool
	NoRun    bool
	Timeout  time.Duration
}

type FileResult struct {
	Path    string `json:"path"`
	Status  string `json:"status"`
	Reason  string `json:"reason,omitempty"`
	Content string `json:"content,omitempty"`
}

type Result struct {
	Worktree string       `json:"worktree"`
	DryRun   bool         `json:"dry_run"`
	Changed  bool         `json:"changed"`
	Hints    []Hint       `json:"hints"`
	Files    []FileResult `json:"files"`
	Verify   VerifyResult `json:"verify"`
	Next     []string     `json:"next"`
	Error    string       `json:"error,omitempty"`
}

func Init(ctx context.Context, options Options) (result Result, err error) {
	result = Result{Worktree: options.Worktree, DryRun: options.DryRun, Hints: []Hint{}, Files: []FileResult{{Path: ScriptFile, Status: "skipped", Reason: "not planned"}, {Path: loop.ConfigFile, Status: "skipped", Reason: "not planned"}}, Verify: VerifyResult{Status: "skipped", Output: "setup incomplete"}, Next: []string{}}
	defer func() {
		if err != nil {
			result.Error = err.Error()
			for i := range result.Files {
				file := &result.Files[i]
				if file.Status == "would_write" {
					file.Status = "skipped"
					file.Reason = "initialization stopped before writing"
					file.Content = ""
				}
			}
		}
	}()
	root, err := filepath.Abs(options.Worktree)
	if err != nil {
		return result, err
	}
	result.Worktree = root
	info, err := os.Stat(root)
	if err != nil {
		return result, err
	}
	if !info.IsDir() {
		return result, fmt.Errorf("worktree is not a directory: %s", root)
	}
	hints, err := Hints(root)
	if err != nil {
		return result, err
	}
	result.Hints = hints
	exists := func(name string) (os.FileInfo, error) {
		info, err := os.Lstat(filepath.Join(root, name))
		if os.IsNotExist(err) {
			return nil, nil
		}
		return info, err
	}
	scriptInfo, err := exists(ScriptFile)
	if err != nil {
		return result, err
	}
	configInfo, err := exists(loop.ConfigFile)
	if err != nil {
		return result, err
	}
	if scriptInfo != nil {
		result.Files[0].Status = "kept"
		result.Files[0].Reason = "it already exists"
	}
	if configInfo != nil {
		result.Files[1].Status = "kept"
		result.Files[1].Reason = "it already exists"
	}
	if scriptInfo != nil {
		executable, err := os.Stat(filepath.Join(root, ScriptFile))
		if err != nil || !executable.Mode().IsRegular() || executable.Mode().Perm()&0111 == 0 {
			return result, fmt.Errorf("%s is not executable; chmod +x %s or remove it", ScriptFile, ScriptFile)
		}
	}
	existing, check, err := loop.ResolveHooks(root, "")
	if err != nil {
		return result, err
	}
	if options.Check != "" {
		check = options.Check
	}
	verify := existing
	bodies := map[string]string{}
	useScript := existing == "" || strings.TrimPrefix(existing, "./") == ScriptFile
	if useScript {
		verify = ScriptFile
		if scriptInfo == nil {
			body := RenderStub()
			if options.Verify != "" {
				body, err = RenderVerify(options.Verify)
				if err != nil {
					return result, err
				}
			}
			bodies[ScriptFile] = body
		}
	} else if scriptInfo == nil {
		if existing == "./"+loop.VerifyHook {
			result.Files[0].Reason = "./" + loop.VerifyHook + " already exists and is used"
		} else {
			result.Files[0].Reason = loop.ConfigFile + " already sets verify"
		}
	}
	result.Verify.Command = verify
	if configInfo == nil {
		bodies[loop.ConfigFile] = RenderConfig(verify, check)
	}
	for i := range result.Files {
		file := &result.Files[i]
		body, write := bodies[file.Path]
		if !write {
			continue
		}
		parent := filepath.Dir(filepath.Join(root, file.Path))
		if info, err := os.Lstat(parent); err == nil {
			if !info.IsDir() {
				return result, fmt.Errorf("%s must be a directory", filepath.Dir(file.Path))
			}
		} else if !os.IsNotExist(err) {
			return result, err
		}
		file.Status = "would_write"
		file.Reason = ""
		if options.DryRun {
			file.Content = body
		}
	}
	if !options.DryRun {
		for i := range result.Files {
			file := &result.Files[i]
			body, write := bodies[file.Path]
			if !write {
				continue
			}
			mode := os.FileMode(0644)
			if file.Path == ScriptFile {
				mode = 0755
			}
			if err := writeNew(filepath.Join(root, file.Path), body, mode); err != nil {
				file.Status = "skipped"
				file.Reason = err.Error()
				return result, err
			}
			file.Status = "written"
			result.Changed = true
		}
	}
	if len(bodies) > 0 {
		var paths []string
		for _, file := range result.Files {
			if _, drafted := bodies[file.Path]; drafted {
				paths = append(paths, file.Path)
			}
		}
		result.Next = append(result.Next, "review and commit "+strings.Join(paths, " and "))
	}
	if configInfo != nil && existing == "" {
		result.Next = append(result.Next, fmt.Sprintf("add verify = %q to %s", verify, loop.ConfigFile))
	}
	unfinished := false
	if useScript {
		body := bodies[ScriptFile]
		if scriptInfo != nil {
			data, err := os.ReadFile(filepath.Join(root, ScriptFile))
			if err != nil {
				return result, err
			}
			body = string(data)
		}
		unfinished = IsStub(body)
	}
	if options.Verify != "" && scriptInfo != nil || !useScript && options.Verify != "" {
		verify = options.Verify
		result.Verify.Command = verify
		unfinished = false
	}
	if unfinished {
		result.Verify.Status = "unfinished"
		result.Verify.Output = "scripts/verify is a stub; write the command, then run km prove"
		result.Next = append(result.Next, "write the command in scripts/verify and remove the unfinished marker", "km prove -worktree "+shellQuote(root)+" -break PATCH")
		return result, nil
	}
	result.Next = append(result.Next, "km preflight -worktree "+shellQuote(root), "km run -repo "+shellQuote(root)+" -task-file task.md")
	if options.DryRun {
		result.Verify.Output = "-dry-run"
		return result, nil
	}
	if options.NoRun {
		result.Verify.Output = "-no-run"
		return result, nil
	}
	result.Verify = Run(ctx, root, verify, options.Timeout)
	if result.Verify.Status != "passed" {
		return result, fmt.Errorf("verify %s", result.Verify.Status)
	}
	return result, nil
}

func writeNew(path, body string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	_, writeErr := f.WriteString(body)
	if writeErr == nil {
		writeErr = f.Chmod(mode)
	}
	closeErr := f.Close()
	if writeErr != nil {
		return writeErr
	}
	return closeErr
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}
