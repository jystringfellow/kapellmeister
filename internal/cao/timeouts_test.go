package cao

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func hangingServer(t *testing.T, prefix string) *httptest.Server {
	t.Helper()
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if prefix != "" {
			_, _ = io.WriteString(w, prefix)
			w.(http.Flusher).Flush()
		}
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) })
	return srv
}

func requestWatchdog(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func assertTimeout(t *testing.T, err error, method, path string, limit time.Duration) {
	t.Helper()
	var timeout *TimeoutError
	if !errors.As(err, &timeout) {
		t.Fatalf("error = %v, want *TimeoutError", err)
	}
	if timeout.Method != method || timeout.Path != path || timeout.Limit != limit {
		t.Fatalf("timeout = %+v, want %s %s within %s", timeout, method, path, limit)
	}
	for _, detail := range []string{method, path, limit.String()} {
		if !strings.Contains(err.Error(), detail) {
			t.Errorf("error %q lacks %q", err, detail)
		}
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("error %v does not match DeadlineExceeded", err)
	}
}

func TestAShortCallAgainstAHungServerFailsWithAnErrorNamingMethodPathAndLimit(t *testing.T) {
	calls := []struct {
		name, method, path string
		call               func(context.Context, *Client) error
	}{
		{"terminal", "GET", "/terminals/t", func(ctx context.Context, c *Client) error { _, err := c.Terminal(ctx, "t"); return err }},
		{"output", "GET", "/terminals/t/output?mode=full", func(ctx context.Context, c *Client) error { _, err := c.Output(ctx, "t"); return err }},
		{"profiles", "GET", "/agents/profiles", func(ctx context.Context, c *Client) error { _, err := c.Profiles(ctx); return err }},
		{"permissions", "GET", "/agents/profiles/p", func(ctx context.Context, c *Client) error { _, err := c.ProfilePermissions(ctx, "p"); return err }},
		{"delete terminal", "DELETE", "/terminals/t", func(ctx context.Context, c *Client) error { return c.DeleteTerminal(ctx, "t") }},
		{"delete session", "DELETE", "/sessions/s", func(ctx context.Context, c *Client) error { return c.DeleteSession(ctx, "s") }},
		{"send", "POST", "/terminals/t/inbox/messages?message=hi&sender_id=s", func(ctx context.Context, c *Client) error { return c.SendMessage(ctx, "t", "s", "hi") }},
		{"session", "GET", "/sessions/s", func(ctx context.Context, c *Client) error { _, err := c.SessionTerminals(ctx, "s"); return err }},
		{"group", "PATCH", "/terminals/t/group", func(ctx context.Context, c *Client) error { return c.SetGroup(ctx, "t", []string{"s"}) }},
		{"metadata", "PATCH", "/terminals/t/metadata", func(ctx context.Context, c *Client) error {
			return c.SetMetadata(ctx, "t", map[string]any{"chair": "a"})
		}},
		{"description", "PATCH", "/terminals/t/description", func(ctx context.Context, c *Client) error { return c.SetDescription(ctx, "t", "chair") }},
	}
	for _, tc := range calls {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := NewClient(hangingServer(t, "").URL)
			c.Timeout = 40 * time.Millisecond
			start := time.Now()
			err := tc.call(requestWatchdog(t), c)
			assertTimeout(t, err, tc.method, tc.path, c.Timeout)
			if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
				t.Fatalf("call took %s", elapsed)
			}
		})
	}
}

func TestCreatingASessionOrTerminalIsBoundedByTheLaunchTimeoutNotTheShortOne(t *testing.T) {
	for _, add := range []bool{false, true} {
		for _, hang := range []bool{false, true} {
			name := "create"
			if add {
				name = "add"
			}
			if hang {
				name += " hangs"
			}
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				release := make(chan struct{})
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if hang {
						select {
						case <-release:
						case <-r.Context().Done():
						}
						return
					}
					time.Sleep(80 * time.Millisecond)
					_, _ = io.WriteString(w, `{"id":"t"}`)
				}))
				defer srv.Close()
				defer close(release)
				c := NewClient(srv.URL)
				c.Timeout, c.LaunchTimeout = 20*time.Millisecond, 200*time.Millisecond
				ctx := requestWatchdog(t)
				var terminal Terminal
				var err error
				path := "/sessions?agent_profile=p&session_name=s&working_directory="
				if add {
					path = "/sessions/s/terminals?agent_profile=p&working_directory="
					terminal, err = c.AddTerminal(ctx, "s", Launch{Profile: "p"})
				} else {
					terminal, err = c.CreateSession(ctx, "s", Launch{Profile: "p"})
				}
				if hang {
					assertTimeout(t, err, "POST", path, c.LaunchTimeout)
				} else if err != nil || terminal.ID != "t" {
					t.Fatalf("terminal = %+v, error = %v", terminal, err)
				}
			})
		}
	}
}

func TestRunStepIsBoundedByTheStepsOwnTimeoutPlusTheMargin(t *testing.T) {
	c := NewClient(hangingServer(t, "").URL)
	c.Timeout, c.StepMargin = 20*time.Millisecond, 200*time.Millisecond
	start := time.Now()
	_, err := c.RunStep(requestWatchdog(t), StepRequest{Timeout: 0.2})
	assertTimeout(t, err, "POST", "/terminals/run-step", 400*time.Millisecond)
	if elapsed := time.Since(start); elapsed < 400*time.Millisecond || elapsed > time.Second {
		t.Fatalf("step took %s, want at least 400ms and less than 1s", elapsed)
	}
}

func TestRunStepNeverCutsAStepShortOfItsOwnTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(180 * time.Millisecond)
		_, _ = io.WriteString(w, `{"terminal_id":"t","status":"completed","last_message":"ready"}`)
	}))
	defer srv.Close()
	c := NewClient(srv.URL)
	c.Timeout, c.StepMargin = 20*time.Millisecond, 100*time.Millisecond
	resp, err := c.RunStep(requestWatchdog(t), StepRequest{Timeout: 0.2})
	if err != nil || resp.TerminalID != "t" || resp.Status != "completed" || resp.LastMessage != "ready" {
		t.Fatalf("response = %+v, error = %v", resp, err)
	}
}

func TestRunStepRespectsACancelledContext(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		t.Run(map[bool]string{false: "cancel", true: "deadline"}[deadline], func(t *testing.T) {
			c := NewClient(hangingServer(t, "").URL)
			c.StepMargin = time.Second
			var ctx context.Context
			var cancel context.CancelFunc
			want := context.Canceled
			if deadline {
				ctx, cancel = context.WithTimeout(context.Background(), 50*time.Millisecond)
				want = context.DeadlineExceeded
			} else {
				ctx, cancel = context.WithCancel(requestWatchdog(t))
				timer := time.AfterFunc(50*time.Millisecond, cancel)
				defer timer.Stop()
			}
			defer cancel()
			start := time.Now()
			_, err := c.RunStep(ctx, StepRequest{Timeout: 1})
			if err != want {
				t.Fatalf("error = %v, want unchanged %v", err, want)
			}
			if time.Since(start) > 500*time.Millisecond {
				t.Fatal("cancellation was not prompt")
			}
		})
	}
}

func TestRunStepWithoutATimeoutIsBoundedOnlyByTheCallersContext(t *testing.T) {
	c := NewClient(hangingServer(t, "").URL)
	c.Timeout, c.StepMargin = time.Millisecond, time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := c.RunStep(ctx, StepRequest{})
	if err != context.DeadlineExceeded || time.Since(start) < 50*time.Millisecond {
		t.Fatalf("error = %v after %s, want caller deadline", err, time.Since(start))
	}
}

func TestAServerThatStallsMidBodyIsReportedNotSilentlyTruncated(t *testing.T) {
	for _, discard := range []bool{false, true} {
		t.Run(map[bool]string{false: "decode", true: "discard"}[discard], func(t *testing.T) {
			c := NewClient(hangingServer(t, `{"id":`).URL)
			c.Timeout = 40 * time.Millisecond
			ctx := requestWatchdog(t)
			if discard {
				assertTimeout(t, c.DeleteTerminal(ctx, "t"), "DELETE", "/terminals/t", c.Timeout)
			} else {
				_, err := c.Terminal(ctx, "t")
				assertTimeout(t, err, "GET", "/terminals/t", c.Timeout)
			}
		})
	}
}

func TestATruncatedResponseBodyReportsItsReadError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "100")
		_, _ = io.WriteString(w, `{}`)
	}))
	defer srv.Close()
	err := NewClient(srv.URL).DeleteTerminal(requestWatchdog(t), "t")
	if !errors.Is(err, io.ErrUnexpectedEOF) || !strings.Contains(err.Error(), "DELETE /terminals/t") {
		t.Fatalf("error = %v, want method, path and unexpected EOF", err)
	}
}

func TestTimeoutErrorMatchesDeadlineExceeded(t *testing.T) {
	if !errors.Is(&TimeoutError{Method: "GET", Path: "/terminals/t", Limit: time.Second}, context.DeadlineExceeded) {
		t.Fatal("TimeoutError does not unwrap to DeadlineExceeded")
	}
}

func TestRunStepPreservesHTTPErrorAndItsTerminalID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusGatewayTimeout)
		_, _ = io.WriteString(w, `{"detail":{"terminal_id":"t"}}`)
	}))
	defer srv.Close()
	_, err := NewClient(srv.URL).RunStep(requestWatchdog(t), StepRequest{Timeout: 1})
	var herr *HTTPError
	if !errors.As(err, &herr) || herr.TerminalID != "t" || herr.Status != 504 || herr.Method != "POST" || herr.Path != "/terminals/run-step" {
		t.Fatalf("error = %+v", err)
	}
}
