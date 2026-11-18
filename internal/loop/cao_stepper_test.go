package loop

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/jystringfellow/kapellmeister/internal/cao"
)

func hungCAOPreflight(t *testing.T) ([]PreflightResult, int64, time.Duration) {
	t.Helper()
	var calls atomic.Int64
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" && r.URL.Path == "/sessions" {
			name := r.URL.Query().Get("session_name")
			if !strings.HasPrefix(name, "cao-") {
				name = "cao-" + name
			}
			_ = json.NewEncoder(w).Encode(cao.Terminal{ID: name, SessionName: name})
			return
		}
		if r.Method == "PATCH" || r.Method == "GET" || r.Method == "DELETE" {
			_, _ = w.Write([]byte(`{}`))
			return
		}
		if r.Method != "POST" || r.URL.Path != "/terminals/run-step" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			return
		}
		var req cao.StepRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		if req.Timeout != 0.05 {
			t.Errorf("step timeout = %v, want 0.05", req.Timeout)
		}
		calls.Add(1)
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)
	client := cao.NewClient(srv.URL)
	client.StepMargin = 50 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	start := time.Now()
	results, err := Preflight(ctx, CAOStepper{Client: client}, t.TempDir(), []Chair{{Provider: "a"}, {Provider: "b"}, {Provider: "c"}}, 50*time.Millisecond)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	return results, calls.Load(), elapsed
}

func TestPreflightAgainstAHungCAOFailsEachChairWithinTwoTimeoutsPlusTheirMargins(t *testing.T) {
	results, _, elapsed := hungCAOPreflight(t)
	if len(results) != 3 {
		t.Fatalf("results = %+v", results)
	}
	for _, res := range results {
		if res.OK || !strings.Contains(res.Error, "POST /terminals/run-step: no response within 100ms") {
			t.Errorf("result = %+v", res)
		}
	}
	if elapsed > 500*time.Millisecond {
		t.Fatalf("preflight took %s, want less than 500ms", elapsed)
	}
}

func TestPreflightRetriesAChairWhoseCheckCallTimedOutOnce(t *testing.T) {
	results, calls, _ := hungCAOPreflight(t)
	if calls != 2*int64(len(results)) {
		t.Errorf("requests = %d, want %d", calls, 2*len(results))
	}
	for _, res := range results {
		if !res.Retried || !res.TimeoutRetried {
			t.Errorf("timed out check was not retried: %+v", res)
		}
	}
}

func TestACancelledPreflightAgainstCAODeletesExactlyTheSessionsItCreated(t *testing.T) {
	var mu sync.Mutex
	created := map[string]string{}
	live := map[string]bool{"ensemble-we-must-keep": true, "another-run": true}
	deleted := map[string]bool{}
	started := make(chan struct{}, 3)
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST" && r.URL.Path == "/sessions":
			name := r.URL.Query().Get("session_name")
			if !strings.HasPrefix(name, "cao-") {
				name = "cao-" + name
			}
			id := "terminal-" + name
			mu.Lock()
			created[name] = id
			live[name] = true
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(cao.Terminal{ID: id, SessionName: name})
		case r.Method == "PATCH" && strings.HasSuffix(r.URL.Path, "/description"):
			_, _ = w.Write([]byte(`{}`))
		case r.Method == "POST" && r.URL.Path == "/terminals/run-step":
			var req cao.StepRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			if req.ReuseTerminalID == "" || req.SessionName != "" {
				t.Errorf("run-step must reuse a seated terminal: %+v", req)
			}
			started <- struct{}{}
			select {
			case <-release:
			case <-r.Context().Done():
			}
		case r.Method == "DELETE" && strings.HasPrefix(r.URL.Path, "/sessions/"):
			name := strings.TrimPrefix(r.URL.Path, "/sessions/")
			mu.Lock()
			if created[name] == "" || deleted[name] {
				t.Errorf("deleted foreign or duplicate session %q", name)
			}
			deleted[name] = true
			delete(live, name)
			mu.Unlock()
			_, _ = w.Write([]byte(`{}`))
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	defer close(release)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := Preflight(ctx, CAOStepper{Session: "ensemble-we-must-keep", Client: cao.NewClient(srv.URL)}, t.TempDir(), []Chair{{Provider: "a"}, {Provider: "b"}, {Provider: "c"}}, time.Minute)
		done <- err
	}()
	for range 3 {
		<-started
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(created) != 3 || len(deleted) != 3 {
		t.Fatalf("created %v, deleted %v", created, deleted)
	}
	if len(live) != 2 || !live["ensemble-we-must-keep"] || !live["another-run"] {
		t.Errorf("foreign sessions changed: %v", live)
	}
	for session := range created {
		if !deleted[session] {
			t.Errorf("leaked %s", session)
		}
	}
}

func TestACancelDuringAFailedCAOSeatStillDeletesTheSessionNamedByItsTerminal(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var session string
	var deleted string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST" && r.URL.Path == "/sessions":
			if r.URL.Query().Get("provider") != "p" || r.URL.Query().Get("agent_profile") != "a" || r.URL.Query().Get("model") != "m" {
				t.Errorf("launch bindings lost: %s", r.URL.RawQuery)
			}
			session = r.URL.Query().Get("session_name")
			if !strings.HasPrefix(session, "cao-") {
				session = "cao-" + session
			}
			cancel()
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"detail":{"terminal_id":"owned-terminal"}}`))
		case r.Method == "GET" && r.URL.Path == "/terminals/owned-terminal":
			_ = json.NewEncoder(w).Encode(cao.Terminal{ID: "owned-terminal", SessionName: session})
		case r.Method == "DELETE" && r.URL.Path == "/sessions/"+session:
			deleted = session
			_, _ = w.Write([]byte(`{}`))
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	_, err := Preflight(ctx, CAOStepper{Client: cao.NewClient(srv.URL)}, t.TempDir(), []Chair{{Provider: "p", Profile: "a", Model: "m"}}, time.Minute)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v", err)
	}
	if session == "" || deleted != session {
		t.Errorf("created %q, deleted %q", session, deleted)
	}
}

func TestPreflightAgainstCAODeletesTheChosenNameWhenSeatingReturnsNoIdentity(t *testing.T) {
	originalBackoff := seatRetryBackoff
	seatRetryBackoff = []time.Duration{time.Millisecond, 2 * time.Millisecond, 4 * time.Millisecond}
	t.Cleanup(func() { seatRetryBackoff = originalBackoff })
	for _, tc := range []struct {
		name     string
		cancelAt int
		timeout  bool
		absent   bool
	}{
		{name: "every launch fails after creating a session"},
		{name: "launch times out after creating a session", timeout: true},
		{name: "cancel during first failed launch", cancelAt: 1},
		{name: "cancel during failed retry", cancelAt: 2},
		{name: "cancel before launch timeout", cancelAt: 1, timeout: true},
		{name: "failed launch created nothing", absent: true},
		{name: "cancelled launch created nothing", cancelAt: 1, absent: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var mu sync.Mutex
			var chosen, deleted []string
			live := map[string]bool{"cao-another-run": true, "cao-standing-ensemble": true}
			release := make(chan struct{})
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == "POST" && r.URL.Path == "/sessions":
					name := r.URL.Query().Get("session_name")
					canonical := name
					if !strings.HasPrefix(canonical, "cao-") {
						canonical = "cao-" + canonical
					}
					mu.Lock()
					chosen = append(chosen, name)
					attempt := len(chosen)
					if !tc.absent {
						live[canonical] = true
					}
					mu.Unlock()
					if attempt == tc.cancelAt {
						cancel()
					}
					if tc.timeout {
						select {
						case <-release:
						case <-r.Context().Done():
						}
						return
					}
					http.Error(w, "launch failed without an ID", http.StatusInternalServerError)
				case r.Method == "DELETE" && strings.HasPrefix(r.URL.Path, "/sessions/"):
					name := strings.TrimPrefix(r.URL.Path, "/sessions/")
					mu.Lock()
					deleted = append(deleted, name)
					owned := false
					for _, requested := range chosen {
						if name == requested {
							owned = true
						}
					}
					if !owned {
						t.Errorf("deleted a name km did not choose: %q", name)
					}
					exists := live[name]
					delete(live, name)
					mu.Unlock()
					if !exists {
						http.Error(w, "session not found", http.StatusNotFound)
						return
					}
					_, _ = w.Write([]byte(`{}`))
				default:
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer srv.Close()
			defer close(release)
			client := cao.NewClient(srv.URL)
			if tc.timeout {
				client.LaunchTimeout = 20 * time.Millisecond
			}
			results, err := Preflight(ctx, CAOStepper{Session: "cao-standing-ensemble", Client: client}, t.TempDir(), []Chair{{Provider: "a"}}, time.Minute)
			if tc.cancelAt > 0 {
				if !errors.Is(err, context.Canceled) {
					t.Errorf("error = %v, want cancellation", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			mu.Lock()
			defer mu.Unlock()
			attempts := 4
			if tc.cancelAt == 2 {
				attempts = 2
			}
			if tc.timeout || tc.cancelAt == 1 {
				attempts = 1
			}
			if len(chosen) != attempts || len(deleted) != attempts {
				t.Fatalf("chosen %v, deleted %v; want %d attempts cleaned up", chosen, deleted, attempts)
			}
			equal(t, deleted, chosen)
			if len(live) != 2 || !live["cao-another-run"] || !live["cao-standing-ensemble"] {
				t.Errorf("owned sessions leaked or foreign sessions changed: %v", live)
			}
			r := results[0]
			if r.Unseated != attempts || len(r.TeardownErrors) != 0 {
				t.Errorf("lost teardown result (404 must be harmless): %+v", r)
			}
		})
	}
}

type stepperTransport func(*http.Request) (*http.Response, error)

func (f stepperTransport) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestALessonsTerminalIsInspectedAfterItsStepDeadlineExpires(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		client := cao.NewClient("http://cao.invalid")
		client.HTTP.Transport = stepperTransport(func(req *http.Request) (*http.Response, error) {
			if err := req.Context().Err(); err != nil {
				return nil, err
			}
			text := ""
			switch req.Method + " " + req.URL.Path {
			case "POST /terminals/run-step":
				<-ctx.Done()
				text = `{"terminal_id":"lessons-terminal","status":"completed"}`
			case "GET /terminals/lessons-terminal":
				deadline, ok := req.Context().Deadline()
				if !ok || deadline.Sub(time.Now()) != 15*time.Second {
					t.Errorf("inspection deadline = %s, present = %v", deadline, ok)
				}
				text = `{"id":"lessons-terminal","model":"gate-model","model_honored":true}`
			default:
				t.Errorf("unexpected request: %s %s", req.Method, req.URL.Path)
				return nil, errors.New("unexpected request")
			}
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(text))}, nil
		})
		out, err := (CAOStepper{Client: client}).Run(ctx, Step{Name: "lessons", Timeout: time.Second})
		if err != nil || ctx.Err() != context.DeadlineExceeded || out.TerminalID != "lessons-terminal" || out.ReportedModel != "gate-model" || out.ModelHonored == nil || !*out.ModelHonored {
			t.Fatalf("result = %+v, error = %v, context = %v", out, err, ctx.Err())
		}
	})
}

func TestPreflightDoesNotRetryASeatWhenItsLaunchContextExpires(t *testing.T) {
	dir := t.TempDir()
	synctest.Test(t, func(t *testing.T) {
		client := cao.NewClient("http://cao.invalid")
		client.LaunchTimeout = 2 * time.Minute
		var chosen, deleted []string
		var callError error
		client.HTTP.Transport = stepperTransport(func(req *http.Request) (*http.Response, error) {
			switch {
			case req.Method == "POST" && req.URL.Path == "/sessions":
				chosen = append(chosen, req.URL.Query().Get("session_name"))
				<-req.Context().Done()
				callError = req.Context().Err()
				return nil, callError
			case req.Method == "DELETE" && strings.HasPrefix(req.URL.Path, "/sessions/"):
				if req.Context().Err() != nil {
					t.Errorf("cleanup context: %v", req.Context().Err())
				}
				deleted = append(deleted, strings.TrimPrefix(req.URL.Path, "/sessions/"))
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{}`))}, nil
			default:
				t.Errorf("unexpected request: %s %s", req.Method, req.URL.Path)
				return nil, errors.New("unexpected request")
			}
		})
		results, err := Preflight(context.Background(), CAOStepper{Client: client}, dir, []Chair{{Provider: "a"}}, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		if callError != context.DeadlineExceeded {
			t.Fatalf("call error = %v", callError)
		}
		if len(chosen) != 1 {
			t.Errorf("seat requests = %v, want one timed-out attempt", chosen)
		}
		equal(t, deleted, chosen)
		r := results[0]
		if r.OK || r.Retried || r.SeatRetries != 0 || r.Error != context.DeadlineExceeded.Error() || r.Unseated != 1 || len(r.TeardownErrors) != 0 {
			t.Errorf("timed-out seat result = %+v", r)
		}
	})
}

func TestCAOStepperReadsAChairsScreenThroughCAOWithoutTmux(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	for _, raw := range []string{"Do you trust this folder?", "\x1b[2J\x1b[HDo you trust this folder?"} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.Method + " " + r.URL.Path {
			case "GET /terminals/chair", "POST /sessions":
				_ = json.NewEncoder(w).Encode(cao.Terminal{ID: "chair", SessionName: "cao-isolated"})
			case "GET /terminals/chair/output":
				if r.URL.Query().Get("mode") != "full" {
					t.Error("output must request full mode")
				}
				_ = json.NewEncoder(w).Encode(map[string]string{"output": raw})
			case "PATCH /terminals/chair/description":
				_, _ = w.Write([]byte(`{}`))
			case "POST /terminals/run-step":
				http.Error(w, "chair is blocked", http.StatusRequestTimeout)
			default:
				t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
				w.WriteHeader(http.StatusNotFound)
			}
		}))
		stepper := CAOStepper{Client: cao.NewClient(srv.URL)}
		session, screen, err := stepper.Screen(context.Background(), "chair")
		if err != nil || session != "cao-isolated" || strings.TrimSpace(screen) != "Do you trust this folder?" {
			t.Errorf("screen = %q, session = %q, error = %v", screen, session, err)
		}
		results, err := Preflight(context.Background(), stepper, t.TempDir(), []Chair{{Provider: "p"}}, time.Minute)
		if err != nil || len(results) != 1 || !strings.Contains(results[0].Blocker, "Do you trust this folder?") {
			t.Errorf("preflight = %+v, error = %v", results, err)
		}
		srv.Close()
	}
}
