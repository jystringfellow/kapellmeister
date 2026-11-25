package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jystringfellow/kapellmeister/internal/cao"
	"github.com/jystringfellow/kapellmeister/internal/loop"
)

const paneBudget = "Your organization or enterprise has exceeded its Copilot budget. Contact your admin to resume usage."
const paneHeader = "the agent's screen ends with (untrusted text from the agent's pane):"
const paneOutput = "❯ readiness check\n" + paneBudget + "\n────────────────\n❯\n────────────────\nfooter"

func paneServer(t *testing.T, timeout bool) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST" && r.URL.Path == "/sessions":
			json.NewEncoder(w).Encode(cao.Terminal{ID: "ours", SessionName: "preflight-fake"})
		case r.URL.Path == "/terminals/run-step":
			if timeout {
				http.Error(w, preflightTimeoutBody, 504)
			} else {
				http.Error(w, "check failed", 400)
			}
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/output"):
			json.NewEncoder(w).Encode(map[string]string{"output": paneOutput})
		default:
			fmt.Fprint(w, `{}`)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestABlockedPreflightQuotesTheEndOfThePaneBesideTheRetryAdvice(t *testing.T) {
	srv := paneServer(t, false)
	out, err := captureStdout(t, func() error {
		return preflight(context.Background(), cao.NewClient(srv.URL), t.TempDir(), time.Minute, time.Millisecond, loop.Chair{Provider: "a"})
	})
	if err == nil {
		t.Fatal(out)
	}
	if !strings.Contains(out, "| "+paneBudget) || !strings.Contains(out, "the retry after waiting 1ms failed the same way: run the command again in a few minutes") || strings.Index(out, paneHeader) <= strings.Index(out, "BLOCKED") {
		t.Fatal(out)
	}
	if !strings.Contains(err.Error(), paneBudget) || strings.Index(err.Error(), paneHeader) <= strings.Index(err.Error(), "tmux attach -t preflight-fake") {
		t.Fatal(err)
	}
}

func TestATimedOutPreflightQuotesThePaneOnceInTheOutputAndOnceInTheError(t *testing.T) {
	srv := paneServer(t, true)
	out, err := captureStdout(t, func() error {
		return preflight(context.Background(), cao.NewClient(srv.URL), t.TempDir(), time.Second, 0, loop.Chair{Provider: "a"})
	})
	if err == nil {
		t.Fatal(out)
	}
	for _, s := range []string{out, err.Error()} {
		if strings.Count(s, paneHeader) != 1 || !strings.Contains(s, paneBudget) {
			t.Fatal(s)
		}
	}
	s := err.Error()
	if !strings.Contains(s, "1. run the command again") || strings.Index(s, paneHeader) <= strings.Index(s, "tmux attach -t preflight-fake") || strings.Index(s, "server detail:") <= strings.Index(s, paneHeader) {
		t.Fatal(s)
	}
}

func TestTuneVerifyQuotesThePaneOfAChairBlockedWithNoKnownPrompt(t *testing.T) {
	root := tuneFixture(t)
	proofWrite(t, loop.Home(), "config.toml", "[agents]\navailable = ['mock']\n[agents.define]\nmock = 'test:mock'\n[run]\npreflight_timeout = '1s'\npreflight_retry_wait = '0s'\n", 0644)
	srv := (&tuneCAO{mode: "blocked", output: paneOutput}).serve(t)
	code, out := tuneCall(t, "verify", "-worktree", root, "-cao", srv.URL)
	for _, want := range []string{"cannot answer preflight", "tmux attach -t tune-fake", paneHeader, paneBudget} {
		if code != 1 || !strings.Contains(out, want) {
			t.Fatal(code, out)
		}
	}
}
