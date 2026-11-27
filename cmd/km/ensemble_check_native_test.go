package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jystringfellow/kapellmeister/internal/loop"
)

func TestEnsembleCheckResolvesEachNativeDefinitionAtTheBaseCommit(t *testing.T) {
	path, args := fallbackFixture(t, &fallbackCAO{}, false)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, strings.Replace(string(data), "part = 'classifier'\nagents = ['a', 'b']", "part = 'classifier'\nagents = ['claude']\nnative_agent = 'demo'", 1))
	writeFile(t, filepath.Join(loop.Home(), "config.toml"), "[agents]\navailable = ['a', 'b', 'claude']\n[agents.define]\na = 'pa:a'\nb = 'pb:b'\n")
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	wt := args[3]
	check := func() (int, string) {
		var diag bytes.Buffer
		code := 0
		out, err := captureStdout(t, func() error {
			code = execute([]string{"ensemble", "check", "-worktree", wt, path}, &diag)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		return code, out + diag.String()
	}
	if code, out := check(); code != 1 || !strings.Contains(out, ".claude/agents/demo.md") || !strings.Contains(out, "close.size: native agent demo") {
		t.Fatalf("code %d: %s", code, out)
	}
	writeFile(t, filepath.Join(wt, ".claude", "agents", "demo.md"), "---\nname: demo\n---\nbase agent\n")
	writeFile(t, filepath.Join(wt, ".claude", "settings.json"), "{}\n")
	writeFile(t, filepath.Join(wt, ".claude", "scheduled_tasks.lock"), "")
	for _, gitArgs := range [][]string{{"add", "-f", ".claude"}, {"-c", "commit.gpgsign=false", "commit", "-qm", "native agent"}} {
		if out, err := exec.Command("git", append([]string{"-C", wt}, gitArgs...)...).CombinedOutput(); err != nil {
			t.Fatalf("%s: %v", out, err)
		}
	}
	if code, out := check(); code != 0 {
		t.Fatalf("code %d: %s", code, out)
	}
}
