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
