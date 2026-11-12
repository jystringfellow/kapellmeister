package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/jystringfellow/kapellmeister/internal/ensemble"
	"github.com/jystringfellow/kapellmeister/internal/loop"
)

func TestRehearseNeedsAWorktree(t *testing.T) {
	if err := rehearseCommand([]string{"-cao", unreachableCAO}); err == nil || err.Error() != "-worktree is required" {
		t.Errorf("error = %v", err)
	}
}

func TestRehearseNeedsExactlyOneTaskSource(t *testing.T) {
	for _, tt := range []struct {
		args []string
		want string
	}{
		{nil, "-task or -task-file is required"},
		{[]string{"-task", "text", "-task-file", "missing.md"}, "use -task or -task-file, not both"},
	} {
		args := append([]string{"-worktree", t.TempDir(), "-cao", unreachableCAO}, tt.args...)
		if err := rehearseCommand(args); err == nil || err.Error() != tt.want {
			t.Errorf("error = %v, want %s", err, tt.want)
		}
	}
}

func TestRehearseStopsOnAFailingBaselineBeforeAnyCAOCall(t *testing.T) {
	for _, source := range []string{"flag", "config", "hook"} {
		t.Run(source, func(t *testing.T) {
			t.Setenv(loop.HomeEnv, t.TempDir())
			writeFile(t, loop.UserConfigPath(loop.Home()), builtInAgentConfig)
			t.Setenv(loop.StateEnv, t.TempDir())
			wt := t.TempDir()
			args := []string{"-worktree", wt, "-task", "task", "-cao", unreachableCAO}
			const command = "echo baseline-failed; exit 3"
			if source == "flag" {
				args = append(args, "-verify", command)
			} else {
				if err := os.MkdirAll(filepath.Join(wt, ".kapellmeister"), 0755); err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(wt, loop.ConfigFile)
				data := "[test]\nverify = '" + command + "'\n"
				if source == "hook" {
					path = filepath.Join(wt, loop.VerifyHook)
					data = "#!/bin/sh\n" + command + "\n"
				}
				if err := os.WriteFile(path, []byte(data), 0755); err != nil {
					t.Fatal(err)
				}
			}
			err := rehearseCommand(args)
			if err == nil || !strings.Contains(err.Error(), "exit code 3") || !strings.Contains(err.Error(), "baseline-failed") || strings.Contains(err.Error(), "connect") {
				t.Fatalf("baseline error = %v", err)
			}
			entries, err := os.ReadDir(loop.StateDir())
			if err != nil || len(entries) != 0 {
				t.Errorf("baseline failure created state: %v, %v", entries, err)
			}
		})
	}
}

func TestRehearseRetriesTheBaselineUsingTheConfiguredRetryCount(t *testing.T) {
	t.Setenv(loop.HomeEnv, t.TempDir())
	writeFile(t, loop.UserConfigPath(loop.Home()), builtInAgentConfig)
	wt := t.TempDir()
	if err := os.MkdirAll(filepath.Join(wt, ".kapellmeister"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt, loop.ConfigFile), []byte("[run]\nverify_retries = 2\n"), 0600); err != nil {
		t.Fatal(err)
	}
	err := rehearseCommand([]string{"-worktree", wt, "-task", "task", "-cao", unreachableCAO, "-verify", "echo attempt >> attempts; exit 3"})
	if err == nil || !strings.Contains(err.Error(), "3 attempt(s)") {
		t.Fatalf("error = %v", err)
	}
	data, readErr := os.ReadFile(filepath.Join(wt, "attempts"))
	if readErr != nil || string(data) != "attempt\nattempt\nattempt\n" {
		t.Fatalf("attempts = %q, %v", data, readErr)
	}
}

func TestRehearseProceedsToCAOWhenTheBaselinePassesOrIsSkipped(t *testing.T) {
	for _, verify := range []string{"true", ""} {
		t.Setenv(loop.HomeEnv, t.TempDir())
		writeFile(t, loop.UserConfigPath(loop.Home()), builtInAgentConfig)
		err := rehearseCommand([]string{"-worktree", t.TempDir(), "-task", "task", "-cao", unreachableCAO, "-verify", verify})
		if err == nil || strings.Contains(err.Error(), "baseline verify failed") || !strings.Contains(err.Error(), "connect") {
			t.Errorf("verify=%q: error = %v", verify, err)
		}
	}
}

func TestRehearseRejectsAnUnknownEnsembleBeforeAnyCAOCall(t *testing.T) {
	t.Setenv(loop.HomeEnv, t.TempDir())
	writeFile(t, loop.UserConfigPath(loop.Home()), builtInAgentConfig)
	err := rehearseCommand([]string{"-worktree", t.TempDir(), "-task", "task", "-cao", unreachableCAO, "-ensemble", "nope"})
	if err == nil || !strings.Contains(err.Error(), `unknown ensemble "nope"`) {
		t.Errorf("error = %v", err)
	}
}

func TestRehearseWritesTheReportAndStandingTaskFileAndNamesFailedChairs(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			t.Setenv(loop.HomeEnv, t.TempDir())
			writeFile(t, loop.UserConfigPath(loop.Home()), builtInAgentConfig)
			t.Setenv(loop.StateEnv, t.TempDir())
			wt := t.TempDir()
			taskPath := filepath.Join(t.TempDir(), "task.md")
			const task = "Read the full task.\nKeep all its lines."
			if err := os.WriteFile(taskPath, []byte(task), 0600); err != nil {
				t.Fatal(err)
			}
			cwd, err := os.Getwd()
			if err != nil {
				t.Fatal(err)
			}
			taskArg, err := filepath.Rel(cwd, taskPath)
			if err != nil {
				t.Fatal(err)
			}
			var mu sync.Mutex
			next := 0
			cues := 0
			closed := regexp.MustCompile(`'([^']+)' cue done -queue '([^']+)' (\S+)`)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				switch {
				case r.Method == http.MethodPost && (r.URL.Path == "/sessions" || r.URL.Path == "/sessions/cao-standing/terminals"):
					next++
					fmt.Fprintf(w, `{"id":"%d","session_name":"cao-standing"}`, next)
				case r.Method == http.MethodPatch:
					fmt.Fprint(w, `{}`)
				case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/output"):
					fmt.Fprint(w, `{"output":""}`)
				case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/inbox/messages"):
					cues++
					text := r.URL.Query().Get("message")
					if !strings.Contains(text, task) {
						t.Errorf("task missing from briefing: %s", text)
					}
					match := closed.FindStringSubmatch(text)
					if match == nil {
						t.Errorf("briefing lacks closure command: %s", text)
					} else if !fail || match[3] != "rehearse-implementer-implementer" {
						if err := ensemble.Close(match[2], match[3], "terminal", ""); err != nil {
							t.Error(err)
						}
					}
					fmt.Fprint(w, `{}`)
				default:
					t.Errorf("unexpected CAO call: %s %s", r.Method, r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()
			args := []string{"-worktree", wt, "-task-file", taskArg, "-cao", srv.URL, "-timeout", "300ms"}
			for _, role := range loop.ChairRoles {
				args = append(args, "-"+role, "other:chair:explicit-model")
			}
			err = rehearseCommand(args)
			if fail {
				if err == nil || !strings.Contains(err.Error(), "rehearsal failed for 1 chair(s): implementer.implementer@default") || !strings.Contains(err.Error(), "km ensemble down cao-standing") {
					t.Fatalf("error = %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			records, err := ensemble.ListStanding(filepath.Join(loop.StateDir(), "ensembles"))
			if err != nil || len(records) != 1 {
				t.Fatalf("records = %+v, %v", records, err)
			}
			standing := records[0]
			resolvedWT, err := worktreeDir(wt)
			if err != nil {
				t.Fatal(err)
			}
			if standing.Session != "cao-standing" || standing.Ensemble != "default" || standing.Worktree != resolvedWT || standing.TaskFile != taskPath || standing.Task != "" || len(standing.Chairs) != 5 {
				t.Errorf("standing = %+v", standing)
			}
			if standing.GateWhen != loop.GateWhenAny {
				t.Errorf("recorded gate_when = %q", standing.GateWhen)
			}
			for _, c := range standing.Chairs {
				if c.Name == "" || c.Binding.Address != c.Address || c.Binding.String() != "other:chair:explicit-model" {
					t.Errorf("standing binding = %+v", c)
				}
			}
			data, err := os.ReadFile(filepath.Join(filepath.Dir(standing.Queue), "report.json"))
			if err != nil {
				t.Fatal(err)
			}
			var report ensemble.Report
			if err := json.Unmarshal(data, &report); err != nil {
				t.Fatal(err)
			}
			if report.Session != standing.Session || report.Queue != standing.Queue || report.OK == fail || len(report.Chairs) != 5 {
				t.Errorf("report = %+v", report)
			}
			mu.Lock()
			defer mu.Unlock()
			if next != 5 || cues != 5 {
				t.Errorf("booted %d and briefed %d chairs", next, cues)
			}
		})
	}
}
