package loop

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func nativeRepoWith(t *testing.T, files map[string]string, links map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	gitTestNative(t, dir, "init", "-q")
	for p, body := range files {
		full := filepath.Join(dir, p)
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	for p, target := range links {
		full := filepath.Join(dir, p)
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, full); err != nil {
			t.Fatal(err)
		}
	}
	gitTestNative(t, dir, "add", "-A")
	gitTestNative(t, dir, "commit", "-qm", "base")
	return dir
}

func TestOrdinaryFilesAndDirectoriesBesideClaudeAgentsDoNotBlockANativeChair(t *testing.T) {
	dir := nativeRepoWith(t, map[string]string{
		".claude/settings.json":          "{}\n",
		".claude/settings.local.json":    "{}\n",
		".claude/scheduled_tasks.lock":   "",
		".claude/CLAUDE.md":              "notes\n",
		".claude/skills/helper/SKILL.md": "skill\n",
		".claude/agents/demo.md":         "---\nname: demo\n---\nbody\n",
	}, nil)
	chair, err := ResolveNativeDefinition(dir, Chair{NativeAgent: "demo"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := chair.CheckDefinition(dir); err != nil {
		t.Fatal(err)
	}
}

func TestASymlinkedClaudeOrAgentsDirectoryStillRefusesANativeChair(t *testing.T) {
	for name, links := range map[string]map[string]string{
		".claude":        {".claude": "real"},
		".claude/agents": {".claude/agents": "../real"},
	} {
		t.Run(name, func(t *testing.T) {
			files := map[string]string{"real/agents/demo.md": "---\nname: demo\n---\n", "real/demo.md": "---\nname: demo\n---\n"}
			if name == ".claude/agents" {
				files[".claude/settings.json"] = "{}\n"
			}
			dir := nativeRepoWith(t, files, links)
			_, err := ResolveNativeDefinition(dir, Chair{NativeAgent: "demo"}, true)
			if err == nil || !strings.Contains(err.Error(), name+": not a regular directory") {
				t.Fatalf("error %v", err)
			}
		})
	}
}
