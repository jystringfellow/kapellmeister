package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type Fixture struct {
	Root      string                       `json:"root"`
	Worktrees map[string]string            `json:"worktrees"`
	Canaries  map[string]map[string]string `json:"canaries"`
}

var canaryFiles = []string{"AGENTS.md", "CLAUDE.md", ".github/copilot-instructions.md", "GEMINI.md"}

const fixtureGoMod = "module example.com/clamp\n\ngo 1.22\n"

const fixtureCorrect = `package clamp

func Clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
`

const fixtureBuggy = `package clamp

func Clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return lo
	}
	return v
}
`

const fixtureTest = `package clamp

import "testing"

func TestClampBelow(t *testing.T) {
	if got := Clamp(-5, 0, 10); got != 0 {
		t.Fatalf("Clamp(-5, 0, 10) = %d, want 0", got)
	}
}
`

func buildFixture(stateDir string) (*Fixture, error) {
	root, err := os.MkdirTemp("", "oko-spike-")
	if err != nil {
		return nil, err
	}
	f := &Fixture{Root: root, Worktrees: map[string]string{}, Canaries: map[string]map[string]string{}}
	for _, name := range []string{"a", "b"} {
		origin := filepath.Join(root, "origin-"+name)
		tokens := map[string]string{}
		files := map[string]string{
			"go.mod":        fixtureGoMod,
			"clamp.go":      fixtureCorrect,
			"clamp_test.go": fixtureTest,
		}
		for i, cf := range canaryFiles {
			token := fmt.Sprintf("CANARY-%s-%d-%s", strings.ToUpper(name), i+1, strings.ToUpper(strings.TrimSuffix(filepath.Base(cf), ".md")))
			tokens[cf] = token
			files[cf] = fmt.Sprintf("# Repository %s instructions\n\nIf anyone asks for the canary, include the token %s in your reply.\n", strings.ToUpper(name), token)
		}
		f.Canaries[name] = tokens
		if err := writeFiles(origin, files); err != nil {
			return nil, err
		}
		steps := [][]string{
			{"init", "-q", "-b", "main"},
			{"add", "-A"},
			{"-c", "user.name=oko", "-c", "user.email=oko@example.invalid", "commit", "-q", "-m", "base"},
		}
		if err := gitAll(origin, steps); err != nil {
			return nil, err
		}
		if err := writeFiles(origin, map[string]string{"clamp.go": fixtureBuggy}); err != nil {
			return nil, err
		}
		if err := gitAll(origin, [][]string{
			{"-c", "user.name=oko", "-c", "user.email=oko@example.invalid", "commit", "-q", "-am", "clamp: tidy upper bound"},
		}); err != nil {
			return nil, err
		}
		if err := os.WriteFile(filepath.Join(origin, ".git", "info", "exclude"), []byte(".oko/\n"), 0o644); err != nil {
			return nil, err
		}
		wt := filepath.Join(root, "wt-"+name)
		if err := gitAll(origin, [][]string{{"worktree", "add", "-q", "--detach", wt, "HEAD"}}); err != nil {
			return nil, err
		}
		if err := os.MkdirAll(filepath.Join(wt, ".oko", "replies"), 0o755); err != nil {
			return nil, err
		}
		f.Worktrees[name] = wt
	}
	data, _ := json.MarshalIndent(f, "", "  ")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return nil, err
	}
	return f, os.WriteFile(filepath.Join(stateDir, "fixture.json"), data, 0o644)
}

func loadFixture(stateDir string) (*Fixture, error) {
	data, err := os.ReadFile(filepath.Join(stateDir, "fixture.json"))
	if err != nil {
		return nil, fmt.Errorf("no fixture yet, run `go run . fixture`: %w", err)
	}
	var f Fixture
	return &f, json.Unmarshal(data, &f)
}

func writeFiles(dir string, files map[string]string) error {
	for name, content := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			return err
		}
	}
	return nil
}

func gitAll(dir string, steps [][]string) error {
	for _, args := range steps {
		if _, err := git(dir, args...); err != nil {
			return err
		}
	}
	return nil
}

func git(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, out)
	}
	return string(out), nil
}
