package cao

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestAFailedSendNamesTheMethodAndPathButNotTheMessage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, "boom")
	}))
	defer srv.Close()
	err := NewClient(srv.URL).SendMessage(requestWatchdog(t), "t", "s", strings.Repeat("cue-marker: play this part! ", 40))
	want := "POST /terminals/t/inbox/messages: HTTP 500: boom"
	if err == nil || err.Error() != want {
		t.Fatalf("error = %v, want %q", err, want)
	}
	var herr *HTTPError
	if !errors.As(err, &herr) || herr.Method != "POST" || herr.Path != "/terminals/t/inbox/messages" || herr.Status != 500 || herr.Body != "boom" {
		t.Fatalf("HTTP error = %+v", err)
	}
}

func TestATimedOutSendNamesTheMethodPathAndLimitButNotTheMessage(t *testing.T) {
	c := NewClient(hangingServer(t, "").URL)
	c.Timeout = 40 * time.Millisecond
	err := c.SendMessage(requestWatchdog(t), "t", "s", strings.Repeat("cue-marker: play this part! ", 40))
	want := "POST /terminals/t/inbox/messages: no response within 40ms"
	if err == nil || err.Error() != want {
		t.Fatalf("error = %v, want %q", err, want)
	}
	assertTimeout(t, err, "POST", "/terminals/t/inbox/messages", c.Timeout)
}

func TestATransportFailureOnASendDoesNotEchoTheMessage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		_ = conn.Close()
	}))
	defer srv.Close()
	err := NewClient(srv.URL).SendMessage(requestWatchdog(t), "t", "s", strings.Repeat("cue-marker: play this part! ", 40))
	want := "POST /terminals/t/inbox/messages: EOF"
	if err == nil || err.Error() != want {
		t.Fatalf("error = %v, want %q", err, want)
	}
	if !errors.Is(err, io.EOF) {
		t.Fatalf("error = %v, want wrapped EOF", err)
	}
}

func TestLaunchErrorsDoNotEchoProfileOrWorkingDirectory(t *testing.T) {
	for _, add := range []bool{false, true} {
		path := "/sessions"
		if add {
			path = "/sessions/s/terminals"
		}
		t.Run(path, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = io.WriteString(w, "launch failed")
			}))
			defer srv.Close()
			c := NewClient(srv.URL)
			launch := Launch{Profile: "profile-marker", WorkingDirectory: "/work/" + strings.Repeat("directory marker/", 50)}
			var err error
			if add {
				_, err = c.AddTerminal(requestWatchdog(t), "s", launch)
			} else {
				_, err = c.CreateSession(requestWatchdog(t), "s", launch)
			}
			want := "POST " + path + ": HTTP 500: launch failed"
			if err == nil || err.Error() != want {
				t.Fatalf("error = %v, want %q", err, want)
			}
			var herr *HTTPError
			if !errors.As(err, &herr) || herr.Path != path {
				t.Fatalf("HTTP error = %+v, want path %q", err, path)
			}
		})
	}
}

func TestRequestsStillCarryTheirQueryString(t *testing.T) {
	message := strings.Repeat("cue-marker: play this part! & ? + ", 40)
	launch := Launch{Profile: "profile marker", Provider: "codex", Model: "model marker", WorkingDirectory: "/work/" + strings.Repeat("directory marker/", 50)}
	for _, kind := range []string{"send", "create", "add"} {
		t.Run(kind, func(t *testing.T) {
			path := "/terminals/t/inbox/messages"
			query := url.Values{"message": {message}, "sender_id": {"s & ? +"}}
			if kind != "send" {
				query = url.Values{"agent_profile": {launch.Profile}, "provider": {launch.Provider}, "model": {launch.Model}, "working_directory": {launch.WorkingDirectory}}
				path = "/sessions/s/terminals"
				if kind == "create" {
					path = "/sessions"
					query.Set("session_name", "s & ? +")
				}
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "POST" || r.URL.Path != path || r.URL.RawQuery != query.Encode() || !reflect.DeepEqual(r.URL.Query(), query) {
					t.Errorf("request = %s %s, want POST %s?%s", r.Method, r.URL, path, query.Encode())
				}
				body, err := io.ReadAll(r.Body)
				if err != nil || len(body) != 0 {
					t.Errorf("body = %q, error = %v, want empty body", body, err)
				}
				_, _ = io.WriteString(w, `{"id":"t"}`)
			}))
			defer srv.Close()
			c := NewClient(srv.URL)
			ctx := requestWatchdog(t)
			var err error
			switch kind {
			case "send":
				err = c.SendMessage(ctx, "t", "s & ? +", message)
			case "create":
				_, err = c.CreateSession(ctx, "s & ? +", launch)
			case "add":
				_, err = c.AddTerminal(ctx, "s", launch)
			}
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestErrorPathsPreserveEscapedQuestionMarksAndOmitEmptyQueries(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, "boom")
	}))
	defer srv.Close()
	for _, path := range []string{"/sessions/s%3F/terminals", "/sessions/s%3F/terminals?", "/sessions/s%3F/terminals?message="} {
		t.Run(path, func(t *testing.T) {
			err := NewClient(srv.URL).call(context.Background(), time.Second, "POST", path, nil, nil)
			want := "POST /sessions/s%3F/terminals: HTTP 500: boom"
			if err == nil || err.Error() != want {
				t.Fatalf("error = %v, want %q", err, want)
			}
		})
	}
}

func TestOnlyAStructuredRunStepTimeoutMatchesDeadlineExceeded(t *testing.T) {
	for _, tc := range []struct {
		name, method, path, body string
		status                   int
		timeout                  bool
	}{
		{"step timeout", "POST", "/terminals/run-step", `{"detail":{"message":"step did not complete within 3s","kind":"timeout","terminal_id":"t7"}}`, 504, true},
		{"long timeout message", "POST", "/terminals/run-step", `{"detail":{"message":"` + strings.Repeat("x", 700) + `","kind":"timeout","terminal_id":"t7"}}`, 504, true},
		{"provider error", "POST", "/terminals/run-step", `{"detail":{"kind":"provider_error","terminal_id":"t7"}}`, 502, false},
		{"step error", "POST", "/terminals/run-step", `{"detail":{"kind":"error","terminal_id":"t7"}}`, 502, false},
		{"unknown gateway timeout", "POST", "/terminals/run-step", `{"detail":{"kind":"error","terminal_id":"t7"}}`, 504, false},
		{"unstructured gateway timeout", "POST", "/terminals/run-step", `{"detail":"timeout"}`, 504, false},
		{"malformed body", "POST", "/terminals/run-step", `{"detail":{"kind":"timeout"`, 504, false},
		{"other status", "POST", "/terminals/run-step", `{"detail":{"kind":"timeout","terminal_id":"t7"}}`, 500, false},
		{"other endpoint", "POST", "/sessions", `{"detail":{"kind":"timeout","terminal_id":"t7"}}`, 504, false},
		{"other method", "GET", "/terminals/run-step", `{"detail":{"kind":"timeout","terminal_id":"t7"}}`, 504, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer srv.Close()
			err := NewClient(srv.URL).call(context.Background(), time.Second, tc.method, tc.path, nil, nil)
			if got := errors.Is(fmt.Errorf("step failed: %w", err), context.DeadlineExceeded); got != tc.timeout {
				t.Errorf("matches deadline = %t, want %t: %v", got, tc.timeout, err)
			}
			var herr *HTTPError
			if !errors.As(err, &herr) || herr.Status != tc.status || herr.Method != tc.method || herr.Path != tc.path || herr.Body != truncate(tc.body, 600) {
				t.Fatalf("HTTP error was lost: %+v", err)
			}
			if strings.Contains(tc.body, `"terminal_id":"t7"`) && herr.TerminalID != "t7" {
				t.Errorf("terminal ID = %q, want t7", herr.TerminalID)
			}
		})
	}
}
