package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/jystringfellow/kapellmeister/internal/ensemble"
	"github.com/jystringfellow/kapellmeister/internal/loop"
)

func TestRunAndAuditionRefuseAReservedSectionBeforeSeating(t *testing.T) {
	for _, command := range []string{"run", "audition"} {
		t.Run(command, func(t *testing.T) {
			t.Setenv(loop.HomeEnv, t.TempDir())
			t.Setenv(loop.StateEnv, t.TempDir())
			wt := t.TempDir()
			path := filepath.Join(wt, ensemble.RepoDir, "duo.toml")
			writeFile(t, path, "[sections.podium.chairs.lead]\npart = 'planner'\nagents = ['codex']\n")
			created := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "POST" {
					created++
				}
				http.Error(w, "unexpected", 500)
			}))
			defer srv.Close()
			args := []string{"-worktree", wt, "-ensemble", "duo", "-cao", srv.URL}
			var err error
			if command == "run" {
				err = runCommand(append(args, "-task", "test"))
			} else {
				err = auditionCommand(args)
			}
			want := fmt.Sprintf("%s: section \"podium\": podium is km's word for the idle terminal that keeps a run's CAO session alive; choose another name", path)
			if err == nil || err.Error() != want {
				t.Errorf("error = %v, want %s", err, want)
			}
			if created != 0 {
				t.Errorf("created %d terminals", created)
			}
		})
	}
}

func TestConfigRefusesAReservedEnsembleName(t *testing.T) {
	t.Setenv(loop.HomeEnv, t.TempDir())
	path := loop.UserConfigPath(loop.Home())
	writeFile(t, path, "[ensembles.podium]\n")
	err := configCommand([]string{"-worktree", t.TempDir()})
	want := path + `: ensembles.podium: podium is km's word for the idle terminal that keeps a run's CAO session alive; choose another name`
	if err == nil || err.Error() != want {
		t.Errorf("error = %v, want %s", err, want)
	}
}
