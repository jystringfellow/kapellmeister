package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/jystringfellow/kapellmeister/internal/cao"
	"github.com/jystringfellow/kapellmeister/internal/loop"
)

type runCAO struct {
	mu              sync.Mutex
	created         []cao.Launch
	deleted         []string
	terminalDeletes []string
	polled          int
	fake            playCAO
	interrupt       bool
	watch           bool
	deleteFails     bool
	poll            chan struct{}
	once            sync.Once
}

func (f *runCAO) serve(t *testing.T) *httptest.Server {
	inner := f.fake.serve(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST" && r.URL.Path == "/sessions":
			q := r.URL.Query()
			f.mu.Lock()
			f.created = append(f.created, cao.Launch{Provider: q.Get("provider"), Profile: q.Get("agent_profile"), Model: q.Get("model"), WorkingDirectory: q.Get("working_directory")})
			f.mu.Unlock()
			json.NewEncoder(w).Encode(cao.Terminal{ID: "podium", SessionName: "cao-run"})
			return
		case r.Method == "DELETE" && strings.HasPrefix(r.URL.Path, "/sessions/"):
			f.mu.Lock()
			f.deleted = append(f.deleted, r.URL.Path)
			f.mu.Unlock()
			if f.deleteFails {
				http.Error(w, "delete failed", 500)
			} else {
				w.Write([]byte(`{}`))
			}
			return
		case r.Method == "DELETE" && strings.HasPrefix(r.URL.Path, "/terminals/"):
			f.mu.Lock()
			f.terminalDeletes = append(f.terminalDeletes, r.URL.Path)
			f.mu.Unlock()
			w.Write([]byte(`{}`))
			return
		case r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/sessions/"):
			f.mu.Lock()
			f.polled++
			count := f.polled
			f.mu.Unlock()
			session := strings.TrimPrefix(r.URL.Path, "/sessions/")
			json.NewEncoder(w).Encode(map[string]any{"session": map[string]string{"name": session}, "terminals": []map[string]string{{"id": "blocked-chair", "tmux_session": session, "status": "waiting_user_answer", "description": "permission chair"}}})
			if f.poll != nil && count >= 2 {
				f.once.Do(func() { close(f.poll) })
			}
			return
		case strings.HasSuffix(r.URL.Path, "/output"):
			json.NewEncoder(w).Encode(map[string]string{"output": "Permission rule Bash(cp*) requires confirmation for this command.\n Do you want to proceed?\n ❯ 1. Yes\n   2. No\nEsc to cancel · Tab to amend"})
			return
		case r.Method == "POST" && r.URL.Path == "/terminals/run-step":
			if f.interrupt {
				io.Copy(io.Discard, r.Body)
				if err := syscall.Kill(os.Getpid(), syscall.SIGINT); err != nil {
					t.Error(err)
				}
				select {
				case <-r.Context().Done():
				case <-time.After(3 * time.Second):
					t.Error("step was not cancelled")
				}
				return
			}
			if f.watch {
				select {
				case <-f.poll:
				case <-time.After(time.Second):
					t.Error("standing session was not polled")
				}
			}
		}
		req, err := http.NewRequestWithContext(r.Context(), r.Method, inner.URL+r.URL.RequestURI(), r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Error(err)
			return
		}
		defer resp.Body.Close()
		w.WriteHeader(resp.StatusCode)
		io.Copy(w, resp.Body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func runSessionFixture(t *testing.T, f *runCAO) (string, error) {
	t.Helper()
	wt, args := runSessionArgs(t, f)
	return wt, runCommand(args)
}

func runSessionArgs(t *testing.T, f *runCAO) (string, []string) {
	t.Helper()
	s := playStanding(t)
	writeFile(t, filepath.Join(s.Worktree, loop.ConfigFile), "[test]\nverify = 'true'\n[run]\npreflight_timeout = '0'\nreply_grace = '1ms'\nrounds = 2\n[sizing]\nmode = 'off'\n")
	srv := f.serve(t)
	args := []string{"-task", "probe", "-worktree", s.Worktree, "-cao", srv.URL}
	for _, part := range loop.AllRoles() {
		spec := "p:" + part + ":m"
		if part == "reviewer" {
			spec = "p:reviewer:m,p:other:m"
		}
		args = append(args, "-"+part, spec)
	}
	wt, err := worktreeDir(s.Worktree)
	if err != nil {
		t.Fatal(err)
	}
	return wt, args
}

func TestRunCreatesOnePodiumSessionAndPassesItOnEveryNewRunStep(t *testing.T) {
	f := &runCAO{}
	wt, err := runSessionFixture(t, f)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.created) != 1 || f.created[0].Profile != "classifier" || f.created[0].WorkingDirectory != wt {
		t.Fatalf("created = %+v", f.created)
	}
	if len(f.fake.requests) < 5 {
		t.Fatalf("steps = %d", len(f.fake.requests))
	}
	for _, req := range f.fake.requests {
		if req.ReuseTerminalID == "" && req.SessionName != "cao-run" {
			t.Errorf("step %s session = %q", req.Description, req.SessionName)
		}
	}
}

func TestRunDeletesItsSessionOnSuccessFailureAndInterrupt(t *testing.T) {
	for _, outcome := range []string{"success", "failure", "interrupt", "delete failure"} {
		t.Run(outcome, func(t *testing.T) {
			f := &runCAO{interrupt: outcome == "interrupt", deleteFails: outcome == "delete failure"}
			if outcome == "failure" {
				f.fake.failPart = "planner"
			}
			var runErr error
			stderr := captureRunStderr(t, func() { _, runErr = runSessionFixture(t, f) })
			wantErr := error(nil)
			if outcome == "failure" || outcome == "interrupt" {
				wantErr = errNotApproved
			}
			if !errors.Is(runErr, wantErr) {
				t.Fatalf("run error = %v, want %v", runErr, wantErr)
			}
			if outcome == "delete failure" && !strings.Contains(stderr, "km: deleting session cao-run:") {
				t.Fatalf("stderr = %q", stderr)
			}
			if len(f.deleted) != 1 || f.deleted[0] != "/sessions/cao-run" {
				t.Fatalf("deleted = %v", f.deleted)
			}
			paths, err := filepath.Glob(filepath.Join(loop.StateDir(), "runs", "*", "result.json"))
			if err != nil || len(paths) != 1 {
				t.Fatalf("result files = %v: %v", paths, err)
			}
			data, _ := os.ReadFile(paths[0])
			var result loop.Result
			json.Unmarshal(data, &result)
			want := loop.Approved
			if outcome == "failure" || outcome == "interrupt" {
				want = loop.Failed
			}
			if result.Outcome != want {
				t.Fatalf("outcome = %s, want %s", result.Outcome, want)
			}
		})
	}
}

func TestPlayPollsTheStandingSessionAndNeverDeletesIt(t *testing.T) {
	s := playStanding(t)
	writeFile(t, filepath.Join(s.Worktree, loop.ConfigFile), "[test]\nverify = 'true'\n[run]\nblocker_poll = '1ms'\nreply_grace = '1ms'\n")
	f := &runCAO{watch: true, poll: make(chan struct{})}
	srv := f.serve(t)
	out, err := capturePlay(t, []string{s.Session, "-cao", srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	if f.polled == 0 || len(f.created) != 0 || len(f.deleted) != 0 || len(f.terminalDeletes) != 0 {
		t.Fatalf("polls=%d creates=%v deletes=%v", f.polled, f.created, f.deleted)
	}
	if !strings.Contains(out, "  blocked: permission chair — Permission rule Bash(cp*) requires confirmation for this command. — attach with tmux attach -t cao-standing") {
		t.Fatalf("progress = %s", out)
	}
	requirePlayStillStanding(t, s, &f.fake, out)
}

func captureRunStderr(t *testing.T, run func()) string {
	t.Helper()
	output, err := os.CreateTemp(t.TempDir(), "stderr-")
	if err != nil {
		t.Fatal(err)
	}
	original := os.Stderr
	os.Stderr = output
	defer func() { os.Stderr = original; output.Close() }()
	run()
	data, err := os.ReadFile(output.Name())
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
