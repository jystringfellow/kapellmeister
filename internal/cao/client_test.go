package cao

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSessionTerminalsReadsStatusAndDescriptionFromTheSessionEndpoint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/sessions/cao-run" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		w.Write([]byte(`{"session":{"name":"cao-run"},"terminals":[{"id":"a","status":"waiting_user_answer","description":"km r · tester","tmux_session":"cao-run"}]}`))
	}))
	defer srv.Close()
	ts, err := NewClient(srv.URL).SessionTerminals(context.Background(), "cao-run")
	if err != nil {
		t.Fatal(err)
	}
	if len(ts) != 1 || ts[0].Status != "waiting_user_answer" || ts[0].Description != "km r · tester" || ts[0].TmuxSession != "cao-run" {
		t.Fatalf("terminals = %+v", ts)
	}
}

func TestBeforeLaunchRunsBeforeEveryRequestThatStartsATerminal(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls != 1 {
			t.Errorf("hook calls before request = %d", calls)
		}
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	c := NewClient(srv.URL)
	c.BeforeLaunch = func(provider, dir string) {
		if provider != "codex" || dir != "/repo" {
			t.Errorf("launch %s %s", provider, dir)
		}
		calls++
	}
	ctx := context.Background()
	l := Launch{Provider: "codex", WorkingDirectory: "/repo"}
	if _, err := c.CreateSession(ctx, "s", l); err != nil {
		t.Fatal(err)
	}
	calls = 0
	if _, err := c.AddTerminal(ctx, "s", l); err != nil {
		t.Fatal(err)
	}
	calls = 0
	if _, err := c.RunStep(ctx, StepRequest{Provider: "codex", WorkingDirectory: "/repo"}); err != nil {
		t.Fatal(err)
	}
}
func TestBeforeLaunchDoesNotRunForARunStepThatReusesATerminalOrAnIncompleteLaunch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{}`)) }))
	defer srv.Close()
	c := NewClient(srv.URL)
	c.BeforeLaunch = func(string, string) { t.Error("unexpected launch hook") }
	ctx := context.Background()
	c.RunStep(ctx, StepRequest{Provider: "codex", WorkingDirectory: "/repo", ReuseTerminalID: "t"})
	c.CreateSession(ctx, "s", Launch{Provider: "codex"})
	c.AddTerminal(ctx, "s", Launch{WorkingDirectory: "/repo"})
}
