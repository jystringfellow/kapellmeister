package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jystringfellow/kapellmeister/internal/cao"
	"github.com/jystringfellow/kapellmeister/internal/loop"
)

func TestAnInterruptedPreflightReportsTeardownAndPrintsNoAttachHints(t *testing.T) {
	for _, failDelete := range []bool{false, true} {
		t.Run(fmt.Sprint("deleteFails=", failDelete), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			release := make(chan struct{})
			var session string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == "POST" && r.URL.Path == "/sessions":
					session = r.URL.Query().Get("session_name")
					if !strings.HasPrefix(session, "cao-") {
						session = "cao-" + session
					}
					_ = json.NewEncoder(w).Encode(cao.Terminal{ID: "ours", SessionName: session})
				case r.Method == "PATCH":
					_, _ = w.Write([]byte(`{}`))
				case r.Method == "POST" && r.URL.Path == "/terminals/run-step":
					cancel()
					select {
					case <-release:
					case <-r.Context().Done():
					}
				case r.Method == "DELETE" && strings.HasPrefix(r.URL.Path, "/sessions/"):
					if failDelete {
						http.Error(w, "delete refused", http.StatusInternalServerError)
					} else {
						_, _ = w.Write([]byte(`{}`))
					}
				default:
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer srv.Close()
			defer close(release)
			stderrPath := filepath.Join(t.TempDir(), "stderr")
			stderr, err := os.Create(stderrPath)
			if err != nil {
				t.Fatal(err)
			}
			defer stderr.Close()
			original := os.Stderr
			os.Stderr = stderr
			defer func() { os.Stderr = original }()
			out, err := captureStdout(t, func() error {
				return preflight(ctx, cao.NewClient(srv.URL), t.TempDir(), time.Minute, loop.Chair{Provider: "a"})
			})
			if !errors.Is(err, context.Canceled) {
				t.Errorf("error = %v", err)
			}
			count := 1
			if failDelete {
				count = 0
			}
			if !strings.Contains(out, fmt.Sprintf("preflight interrupted; took down %d session(s)", count)) {
				t.Errorf("missing teardown report: %s", out)
			}
			if strings.Contains(out, "BLOCKED") || strings.Contains(out, "tmux attach") {
				t.Errorf("advertised removed chairs: %s", out)
			}
			problems, err := os.ReadFile(stderrPath)
			if err != nil {
				t.Fatal(err)
			}
			if failDelete && (!strings.Contains(string(problems), session) || !strings.Contains(string(problems), "delete refused")) {
				t.Errorf("missing named teardown error: %s", problems)
			}
		})
	}
}

func TestPreflightPrintsWhenASeatWasRetried(t *testing.T) {
	for _, blocked := range []bool{false, true} {
		t.Run(fmt.Sprint("blocked=", blocked), func(t *testing.T) {
			dir := t.TempDir()
			attempts := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == "POST" && r.URL.Path == "/sessions":
					attempts++
					name := r.URL.Query().Get("session_name")
					if attempts == 1 {
						http.Error(w, "Could not find object", 500)
						return
					}
					if attempts == 2 {
						http.Error(w, fmt.Sprintf(`{"detail":"Session '%s' not found"}`, name), 400)
						return
					}
					json.NewEncoder(w).Encode(cao.Terminal{ID: "ours", SessionName: name})
				case r.Method == "POST" && r.URL.Path == "/terminals/run-step":
					if blocked {
						http.Error(w, "check failed", 400)
						return
					}
					writeFile(t, filepath.Join(dir, loop.RunDirRel("preflight"), "0-a.txt"), "ready")
					json.NewEncoder(w).Encode(cao.StepResponse{TerminalID: "ours", Status: "completed"})
				case r.Method == "GET":
					json.NewEncoder(w).Encode(cao.Terminal{ID: "ours"})
				default:
					w.Write([]byte(`{}`))
				}
			}))
			defer srv.Close()
			output, err := captureStdout(t, func() error {
				return preflight(context.Background(), cao.NewClient(srv.URL), dir, time.Minute, loop.Chair{Provider: "a"})
			})
			if (err != nil) != blocked {
				t.Errorf("error %v", err)
			}
			label := "ok"
			if blocked {
				label = "BLOCKED"
			}
			if !strings.Contains(output, label) || !strings.Contains(output, "seat retried 2 time(s) after") || !strings.Contains(output, "Could not find object") {
				t.Errorf("output: %s", output)
			}
		})
	}
}

func TestAnInterruptedPreflightCommandReturnsTheSignalNotContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(&loop.SignalError{Signal: os.Interrupt})
	output, err := captureStdout(t, func() error {
		return preflight(ctx, cao.NewClient("http://127.0.0.1:1"), t.TempDir(), time.Minute, loop.Chair{Provider: "a"})
	})
	if err == nil || err.Error() != "interrupted by SIGINT" || !errors.Is(err, context.Canceled) {
		t.Errorf("error %v; output %s", err, output)
	}
}

const preflightTimeoutBody = `{"detail":{"message":"step on terminal t did not complete within 240.0s","kind":"timeout","terminal_id":"writer"}}`

func assertPreflightTimeoutAdvice(t *testing.T, out, session string) {
	t.Helper()
	start := strings.Index(out, "started but did not answer within 1s")
	if start < 0 {
		t.Fatal(out)
	}
	line, _, _ := strings.Cut(out[start:], "\n")
	if !strings.Contains(line, "this can happen when the agent or the server has only just started") || strings.Contains(line, "HTTP 504") || strings.Contains(line, "run-step") {
		t.Fatal(out)
	}
	retry := strings.Index(out, "1. run the command again")
	attach := strings.Index(out, "2. if it happens again, attach with `tmux attach -t "+session+"`")
	detail := strings.Index(out, "server detail: POST /terminals/run-step: HTTP 504:")
	prompts := strings.Index(out, "trust or approval prompt")
	setting := strings.Index(out, "[run] trust_folder = true in "+loop.UserConfigPath(loop.Home()))
	if prompts <= attach || setting <= prompts || detail <= setting {
		t.Fatal(out)
	}

	if retry <= start || attach <= retry || detail <= attach || !strings.Contains(out, "to see what the agent is waiting for, then detach with Ctrl-b d") || !strings.Contains(out, preflightTimeoutBody) {
		t.Fatal(out)
	}
	if strings.Count(out, "tmux attach -t "+session) != 1 {
		t.Fatal(out)
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "BLOCKED") && (!strings.Contains(line, "did not answer within 1s") || strings.Contains(line, "HTTP 504") || strings.Contains(line, "run-step")) {
			t.Fatal(out)
		}
	}
}

func TestPreflightExplainsATimeoutInPlainWordsAndNamesTheSession(t *testing.T) {
	for _, command := range []bool{false, true} {
		t.Run(fmt.Sprint(command), func(t *testing.T) {
			root := tuneFixture(t)
			srv := (&tuneCAO{mode: "timeout"}).serve(t)
			out, err := captureStdout(t, func() error {
				if command {
					return preflightCommand([]string{"-worktree", root, "-cao", srv.URL, "-timeout", "1s"})
				}
				return preflight(context.Background(), cao.NewClient(srv.URL), root, time.Second, loop.Chair{Provider: "test", Profile: "mock"})
			})
			if err == nil {
				t.Fatal(out)
			}
			assertPreflightTimeoutAdvice(t, out+err.Error(), "tune-fake")
		})
	}
}

func TestPreflightKeepsTheMessageOfANonTimeoutFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST" && r.URL.Path == "/sessions":
			json.NewEncoder(w).Encode(cao.Terminal{ID: "ours", SessionName: "preflight-fake"})
		case r.URL.Path == "/terminals/run-step":
			http.Error(w, "check failed", 400)
		default:
			w.Write([]byte(`{}`))
		}
	}))
	defer srv.Close()
	out, err := captureStdout(t, func() error {
		return preflight(context.Background(), cao.NewClient(srv.URL), t.TempDir(), time.Second, loop.Chair{Provider: "a"})
	})
	want := "preflight failed for 1 chair(s):\n  a:: resolve it with `tmux attach -t preflight-fake`, then detach with Ctrl-b d"
	if err == nil || err.Error() != want || !strings.Contains(out, "BLOCKED a: (") || !strings.Contains(out, "): POST /terminals/run-step: HTTP 400: check failed\n") {
		t.Fatal(out, err)
	}
}

func TestRunExplainsAPreflightTimeoutInPlainWordsAndNamesTheSession(t *testing.T) {
	f := &runCAO{preflightTimeout: true}
	_, args := runSessionArgs(t, f)
	args = append(args, "-preflight-timeout", "1s")
	out, err := captureStdout(t, func() error { return runCommand(args) })
	if err == nil {
		t.Fatal(out)
	}
	if !strings.Contains(err.Error(), "preflight failed for") {
		t.Fatal(out, err)
	}
	advice := strings.Split(err.Error(), "\n  chair ")[1:]
	if len(advice) == 0 {
		t.Fatal(out, err)
	}
	for i, message := range advice {
		if i == 0 {
			message = out + "chair " + message
		} else {
			message = "chair " + message
		}
		assertPreflightTimeoutAdvice(t, message, "cao-run")
	}
}

func TestATimeoutMessageNamesPromptsAndTheSetting(t *testing.T) {
	r := loop.PreflightResult{Chair: loop.Chair{Provider: "codex"}, Session: "kept", Error: "detail"}
	message := preflightTimeoutMessage(r, time.Second)
	for _, text := range []string{"trust or approval prompt", "answered automatically", "km's first message lost", "[run] trust_folder = true in " + loop.UserConfigPath(loop.Home()), "known Codex case"} {
		if !strings.Contains(message, text) {
			t.Errorf("missing %q: %s", text, message)
		}
	}
	if !(strings.Index(message, "2. if it happens again") < strings.Index(message, "trust or approval prompt") && strings.Index(message, "known Codex case") < strings.Index(message, "server detail: detail")) {
		t.Fatal(message)
	}
}
func TestPreflightReportsTheSecondAttemptAfterATimeout(t *testing.T) {
	for _, pass := range []bool{true, false} {
		t.Run(fmt.Sprint(pass), func(t *testing.T) {
			dir := t.TempDir()
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == "POST" && r.URL.Path == "/sessions":
					json.NewEncoder(w).Encode(cao.Terminal{ID: "t", SessionName: r.URL.Query().Get("session_name")})
				case r.URL.Path == "/terminals/run-step":
					calls++
					if calls == 2 && pass {
						writeFile(t, filepath.Join(dir, loop.RunDirRel("preflight"), "0-codex.txt"), "ready")
						json.NewEncoder(w).Encode(cao.StepResponse{TerminalID: "t", Status: "completed"})
						return
					}
					w.WriteHeader(504)
					w.Write([]byte(preflightTimeoutBody))
				default:
					w.Write([]byte(`{}`))
				}
			}))
			defer srv.Close()
			out, err := captureStdout(t, func() error {
				return preflight(context.Background(), cao.NewClient(srv.URL), dir, time.Second, loop.Chair{Provider: "codex"})
			})
			if calls != 2 || (err == nil) != pass {
				t.Fatal(calls, out, err)
			}
			want := "first attempt timed out after 1s, second attempt passed"
			if !pass {
				want = "first attempt also timed out and was retried once"
			}
			if !strings.Contains(out, want) {
				t.Fatal(out, err)
			}
		})
	}
}
func TestTuneVerifyReportsATimeoutRetry(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" && r.URL.Path == "/sessions" {
			fmt.Fprint(w, `{"id":"t","session_name":"s"}`)
		} else if r.URL.Path == "/terminals/run-step" {
			w.WriteHeader(504)
			fmt.Fprint(w, preflightTimeoutBody)
		} else {
			fmt.Fprint(w, `{}`)
		}
	}))
	defer srv.Close()
	var out strings.Builder
	err := tuneSeat(context.Background(), cao.NewClient(srv.URL), t.TempDir(), time.Second, &out, loop.Chair{Provider: "codex"})
	if err == nil || !strings.Contains(out.String(), "first attempt also timed out and was retried once") {
		t.Fatal(out.String(), err)
	}
}
