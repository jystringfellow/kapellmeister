package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestARepositorySaveRefusesAWorktreeThatIsNotTheTopOfAGitCheckout(t *testing.T) {
	plain := t.TempDir()
	checkout := t.TempDir()
	safetyGit(t, checkout, "init", "-q")
	inside := filepath.Join(checkout, "sub")
	if err := os.Mkdir(inside, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, wt := range map[string]string{"plain": plain, "inside": inside} {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("KAPELLMEISTER_HOME", home)
			writeFile(t, filepath.Join(home, "config.toml"), "[agents]\navailable = ['claude', 'codex', 'copilot', 'agy']\n")
			var out, diag bytes.Buffer
			err := saveCommand([]string{"-name=sample", "-worktree=" + wt, "-repository", "-"}, strings.NewReader(sectionedEnsemble), &out, &diag)
			if err == nil || !strings.Contains(out.String(), wt) || !strings.Contains(out.String(), "git checkout") {
				t.Fatalf("err=%v out=%q", err, out.String())
			}
			if _, e := os.Lstat(filepath.Join(wt, ".kapellmeister")); !os.IsNotExist(e) {
				t.Fatalf(".kapellmeister was created under %s: %v", wt, e)
			}
		})
	}
}
