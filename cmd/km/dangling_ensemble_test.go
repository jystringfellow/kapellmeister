package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jystringfellow/kapellmeister/internal/ensemble"
	"github.com/jystringfellow/kapellmeister/internal/loop"
)

func TestARunRefusesAUserEnsembleThatIsABrokenLinkRatherThanPlayingTheRepositoryFileItShadows(t *testing.T) {
	home := t.TempDir()
	t.Setenv(loop.HomeEnv, home)
	t.Setenv(loop.StateEnv, t.TempDir())
	wt, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(wt, ensemble.RepoDir, "duo.toml"), "[sections.leads.chairs.lead]\npart = 'planner'\nagents = ['codex']\n")
	userDir := filepath.Join(home, ensemble.UserDir)
	if err := os.MkdirAll(userDir, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(userDir, "duo.toml")
	if err := os.Symlink(filepath.Join(home, "missing.toml"), link); err != nil {
		t.Fatal(err)
	}
	created := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			created++
		}
		http.Error(w, "unexpected", 500)
	}))
	defer srv.Close()
	err = runCommand([]string{"-worktree", wt, "-ensemble", "duo", "-cao", srv.URL, "-task", "test"})
	if err == nil || !strings.Contains(err.Error(), link) || strings.Contains(err.Error(), wt) {
		t.Fatalf("error = %v, want the broken user link %s named and the repository file not loaded", err, link)
	}
	if created != 0 {
		t.Errorf("created %d terminals", created)
	}
}
