package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jystringfellow/kapellmeister/internal/ensemble"
	"github.com/jystringfellow/kapellmeister/internal/loop"
)

func nativeEnsembleFixture(t *testing.T) (path, wt, content string) {
	t.Helper()
	path, args := fallbackFixture(t, &fallbackCAO{}, false)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	content = strings.Replace(string(data), "part = 'classifier'\nagents = ['a', 'b']", "part = 'classifier'\nagents = ['claude']\nnative_agent = 'demo'", 1)
	writeFile(t, path, content)
	writeFile(t, filepath.Join(loop.Home(), "config.toml"), "[agents]\navailable = ['a', 'b', 'claude']\n[agents.define]\na = 'pa:a'\nb = 'pb:b'\n")
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	return path, args[3], content
}

func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	if out, err := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=T", "-c", "user.email=t@example.invalid", "-c", "commit.gpgsign=false"}, args...)...).CombinedOutput(); err != nil {
		t.Fatalf("git %v: %s: %v", args, out, err)
	}
}

func runCheck(t *testing.T, args ...string) (int, string) {
	t.Helper()
	var diag bytes.Buffer
	code := 0
	out, err := captureStdout(t, func() error {
		code = execute(args, &diag)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return code, out + diag.String()
}

func TestEnsembleCheckResolvesEachNativeDefinitionAtTheBaseCommit(t *testing.T) {
	path, wt, _ := nativeEnsembleFixture(t)
	if code, out := runCheck(t, "ensemble", "check", "-worktree", wt, path); code != 1 || !strings.Contains(out, ".claude/agents/demo.md") || !strings.Contains(out, "close.size: native agent demo") {
		t.Fatalf("code %d: %s", code, out)
	}
	writeFile(t, filepath.Join(wt, ".claude", "agents", "demo.md"), "---\nname: demo\n---\nbase agent\n")
	writeFile(t, filepath.Join(wt, ".claude", "settings.json"), "{}\n")
	writeFile(t, filepath.Join(wt, ".claude", "scheduled_tasks.lock"), "")
	gitIn(t, wt, "add", "-f", ".claude")
	gitIn(t, wt, "commit", "-qm", "native agent")
	if code, out := runCheck(t, "ensemble", "check", "-worktree", wt, path); code != 0 {
		t.Fatalf("code %d: %s", code, out)
	}
}

func TestTheRunAndSavePreCheckLeavesNativeDefinitionsToTheLaunch(t *testing.T) {
	_, wt, content := nativeEnsembleFixture(t)
	var diag bytes.Buffer
	if _, _, err := checkEnsemble("band", "band.toml", content, wt, &diag, true); err != nil {
		t.Fatalf("%v %s", err, diag.String())
	}
}

func TestSavingANativeEnsembleDoesNotDependOnTheCurrentRepository(t *testing.T) {
	path, _, _ := nativeEnsembleFixture(t)
	for _, dir := range []string{t.TempDir(), func() string {
		d := t.TempDir()
		gitIn(t, d, "init", "-q")
		gitIn(t, d, "commit", "--allow-empty", "-qm", "base")
		return d
	}()} {
		if code, out := runCheck(t, "ensemble", "save", "-name", "band", "-replace", "-worktree", dir, path); code != 0 {
			t.Fatalf("%s: code %d: %s", dir, code, out)
		}
	}
}

func TestEnsembleCheckNotesNativeDefinitionsItCannotCheckOutsideARepository(t *testing.T) {
	path, _, _ := nativeEnsembleFixture(t)
	plain := t.TempDir()
	empty := t.TempDir()
	gitIn(t, empty, "init", "-q")
	for dir, why := range map[string]string{plain: "is not a git repository", empty: "has no commits"} {
		code, out := runCheck(t, "ensemble", "check", "-worktree", dir, path)
		if code != 0 || !strings.Contains(out, "note: native agent definitions not checked: "+dir+" "+why) || strings.Contains(out, "exit status") || strings.Contains(out, `\n`) {
			t.Fatalf("%s: code %d: %q", why, code, out)
		}
	}
}

func TestEnsembleCheckResolvesARepositoryEnsembleAsPortableLikeItsLaunch(t *testing.T) {
	_, wt, content := nativeEnsembleFixture(t)
	writeFile(t, filepath.Join(os.Getenv("CLAUDE_CONFIG_DIR"), "agents", "demo.md"), "---\nname: demo\n---\nuser agent\n")
	repoPath := filepath.Join(wt, ensemble.RepoDir, "band.toml")
	writeFile(t, repoPath, content)
	userCopy := filepath.Join(t.TempDir(), "band.toml")
	writeFile(t, userCopy, content)
	if code, out := runCheck(t, "ensemble", "check", "-worktree", wt, userCopy); code != 0 {
		t.Fatalf("user copy: code %d: %s", code, out)
	}
	if code, out := runCheck(t, "ensemble", "check", "-worktree", wt, repoPath); code != 1 || !strings.Contains(out, "does not exist at base commit") {
		t.Fatalf("repository copy: code %d: %s", code, out)
	}
}

func TestAMissingDefinitionThatExistsUncommittedSaysSo(t *testing.T) {
	path, wt, _ := nativeEnsembleFixture(t)
	writeFile(t, filepath.Join(wt, ".claude", "agents", "demo.md"), "---\nname: demo\n---\n")
	if code, out := runCheck(t, "ensemble", "check", "-worktree", wt, path); code != 1 || !strings.Contains(out, ".claude/agents/demo.md exists in the worktree but is not committed") {
		t.Fatalf("code %d: %s", code, out)
	}
}

func TestEnsembleCheckResolvesFromADirectoryReachedThroughASymlink(t *testing.T) {
	path, wt, _ := nativeEnsembleFixture(t)
	writeFile(t, filepath.Join(wt, ".claude", "agents", "demo.md"), "---\nname: demo\n---\n")
	gitIn(t, wt, "add", "-f", ".claude")
	gitIn(t, wt, "commit", "-qm", "native agent")
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(wt, link); err != nil {
		t.Fatal(err)
	}
	t.Chdir(link)
	t.Setenv("PWD", link)
	if code, out := runCheck(t, "ensemble", "check", path); code != 0 || strings.Contains(out, "note:") {
		t.Fatalf("code %d: %s", code, out)
	}
}

func TestEnsembleCheckSaysWhenTheWorktreeDoesNotExist(t *testing.T) {
	path, _, _ := nativeEnsembleFixture(t)
	missing := filepath.Join(t.TempDir(), "missing")
	if code, out := runCheck(t, "ensemble", "check", "-worktree", missing, path); code != 0 || !strings.Contains(out, "note: native agent definitions not checked: "+missing+" does not exist") {
		t.Fatalf("code %d: %s", code, out)
	}
}

func TestTheUncommittedHintNamesOnlyARegularFile(t *testing.T) {
	for name, make := range map[string]func(string) error{
		"directory":        func(p string) error { return os.MkdirAll(p, 0755) },
		"dangling symlink": func(p string) error { return os.Symlink("nowhere.md", p) },
	} {
		t.Run(name, func(t *testing.T) {
			path, wt, _ := nativeEnsembleFixture(t)
			target := filepath.Join(wt, ".claude", "agents", "demo.md")
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				t.Fatal(err)
			}
			if err := make(target); err != nil {
				t.Fatal(err)
			}
			if code, out := runCheck(t, "ensemble", "check", "-worktree", wt, path); code != 1 || strings.Contains(out, "not committed") {
				t.Fatalf("code %d: %s", code, out)
			}
		})
	}
}
