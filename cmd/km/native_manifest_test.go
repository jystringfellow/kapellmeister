package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jystringfellow/kapellmeister/internal/loop"
)

func TestReseatRefusesANativeAgentAddedByAForgedTreeAfterRehearsal(t *testing.T) {
	s := playStanding(t)
	dir := s.Worktree
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %s: %v", args, out, err)
		}
		return strings.TrimSpace(string(out))
	}
	path := filepath.Join(dir, ".claude/agents/demo.md")
	writeFile(t, path, "---\nname: demo\n---\n")
	git("add", ".claude")
	git("commit", "-qm", "native")
	chair, err := loop.ResolveNativeDefinition(dir, loop.Chair{Provider: "claude_code", Profile: "native", NativeAgent: "demo"}, true)
	if err != nil {
		t.Fatal(err)
	}
	s.Chairs[0].Binding = chair
	savePlayStanding(t, s)
	oid := git("rev-parse", "HEAD:.claude/agents")
	writeFile(t, filepath.Join(dir, ".claude/agents/sub/helper.md"), "---\nname: demo\n---\n")
	git("add", ".claude")
	tree := git("write-tree")
	forged := git("rev-parse", tree+":.claude/agents")
	git("reset", "-q")
	object := func(id string) string { return filepath.Join(dir, ".git/objects", id[:2], id[2:]) }
	data, err := os.ReadFile(object(forged))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(object(oid), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(object(oid), data, 0444); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(git("ls-tree", "-r", "HEAD", "--", ".claude/agents"), "helper.md") {
		t.Fatal("tree forgery failed")
	}
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests++ }))
	defer server.Close()
	err = reseatCommand([]string{s.Session, s.Chairs[0].Address, "-cao", server.URL})
	if err == nil || !strings.Contains(err.Error(), "is not in the base commit") || !strings.Contains(err.Error(), "helper.md") {
		t.Fatalf("%v", err)
	}
	if requests != 0 {
		t.Fatalf("%d requests", requests)
	}
}
