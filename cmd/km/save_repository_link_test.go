package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestARepositorySaveRefusesALinkedEnsembleDirectoryAndWritesNothingThroughIt(t *testing.T) {
	for _, linked := range []string{".kapellmeister", filepath.Join(".kapellmeister", "ensembles")} {
		t.Run(linked, func(t *testing.T) {
			home, wt, outside := t.TempDir(), t.TempDir(), t.TempDir()
			safetyGit(t, wt, "init", "-q")
			t.Setenv("KAPELLMEISTER_HOME", home)
			writeFile(t, filepath.Join(home, "config.toml"), "[agents]\navailable = ['claude', 'codex', 'copilot', 'agy']\n")
			if err := os.MkdirAll(filepath.Dir(filepath.Join(wt, linked)), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, filepath.Join(wt, linked)); err != nil {
				t.Fatal(err)
			}
			if linked == ".kapellmeister" {
				if err := os.Mkdir(filepath.Join(outside, "ensembles"), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			var out, diag bytes.Buffer
			err := saveCommand([]string{"-name=sample", "-worktree=" + wt, "-repository", "-replace", "-"}, strings.NewReader(sectionedEnsemble), &out, &diag)
			if err == nil || !strings.Contains(out.String(), filepath.Join(wt, linked)) || !strings.Contains(out.String(), "link") {
				t.Fatalf("err=%v out=%q", err, out.String())
			}
			for _, p := range []string{filepath.Join(outside, "sample.toml"), filepath.Join(outside, "ensembles", "sample.toml")} {
				if _, e := os.Lstat(p); !os.IsNotExist(e) {
					t.Fatalf("%s was written: %v", p, e)
				}
			}
		})
	}
}
