package loop

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

func TestClaudeCodeLoadsTheNameKmReadsForEveryFixture(t *testing.T) {
	if os.Getenv("KM_LIVE_CLAUDE") != "1" {
		t.Skip("set KM_LIVE_CLAUDE=1")
	}
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("claude is not on PATH")
	}
	run := func(path string) ([]string, error) {
		dir := t.TempDir()
		config := t.TempDir()
		c := exec.Command("git", "init", "-q", dir)
		if b, e := c.CombinedOutput(); e != nil {
			return nil, fmt.Errorf("git init: %s: %w", b, e)
		}
		if path != "" {
			data, e := os.ReadFile(path)
			if e != nil {
				return nil, e
			}
			dest := filepath.Join(dir, ".claude/agents", filepath.Base(path))
			if e = os.MkdirAll(filepath.Dir(dest), 0755); e != nil {
				return nil, e
			}
			if e = os.WriteFile(dest, data, 0644); e != nil {
				return nil, e
			}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "claude", "-p", "hi", "--output-format", "stream-json", "--verbose")
		cmd.Dir = dir
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		for _, e := range os.Environ() {
			k, _, _ := strings.Cut(e, "=")
			if strings.HasPrefix(k, "CLAUDE") || strings.HasPrefix(k, "ANTHROPIC") || strings.HasPrefix(k, "TMUX") || k == "CAO_TERMINAL_ID" {
				continue
			}
			cmd.Env = append(cmd.Env, e)
		}
		cmd.Env = append(cmd.Env, "CLAUDE_CONFIG_DIR="+config)
		out, e := cmd.StdoutPipe()
		if e != nil {
			return nil, e
		}
		if e = cmd.Start(); e != nil {
			return nil, e
		}
		scanner := bufio.NewScanner(out)
		scanner.Buffer(make([]byte, 4096), 1024*1024)
		var agents []string
		found := false
		for scanner.Scan() {
			var event struct {
				Type    string   `json:"type"`
				Subtype string   `json:"subtype"`
				Agents  []string `json:"agents"`
			}
			if json.Unmarshal(scanner.Bytes(), &event) == nil && event.Type == "system" && event.Subtype == "init" {
				agents = event.Agents
				found = true
				break
			}
		}
		scanErr := scanner.Err()
		if found {
			cancel()
			_ = cmd.Wait()
			return agents, nil
		}
		waitErr := cmd.Wait()
		ctxErr := ctx.Err()
		return nil, fmt.Errorf("no init event: scan: %v; wait: %v; context: %v; stderr: %s", scanErr, waitErr, ctxErr, stderr.String())
	}
	baseline, err := run("")
	if err != nil {
		t.Fatal(err)
	}
	known := map[string]bool{}
	for _, n := range baseline {
		known[n] = true
	}
	for _, kind := range []string{"accepted", "refused"} {
		paths, err := filepath.Glob(filepath.Join("testdata/native-front-matter", kind, "*.md"))
		if err != nil || len(paths) == 0 {
			t.Fatalf("%s: %v", kind, err)
		}
		for _, path := range paths {
			t.Run(kind+"/"+filepath.Base(path), func(t *testing.T) {
				agents, err := run(path)
				if err != nil {
					t.Fatal(err)
				}
				var added []string
				for _, n := range agents {
					if !known[n] {
						added = append(added, n)
					}
				}
				sort.Strings(added)
				t.Logf("Claude loaded %q", added)
				if kind == "refused" {
					return
				}
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				name, err := nativeName(path, data)
				if err != nil {
					t.Fatal(err)
				}
				if len(added) == 0 {
					t.Fatal("Claude Code lists no agent")
				}
				if len(added) != 1 || added[0] != name {
					t.Fatalf("km=%q Claude=%q: %v", name, added, err)
				}
			})
		}
	}
}
