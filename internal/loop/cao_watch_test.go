package loop

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jystringfellow/kapellmeister/internal/cao"
)

func TestCAOStepperPassesItsSessionOnlyForNewTerminals(t *testing.T) {
	for _, reuse := range []string{"", "existing"} {
		t.Run(reuse, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "POST" {
					var req map[string]any
					json.NewDecoder(r.Body).Decode(&req)
					want := "cao-run"
					if reuse != "" {
						want = ""
					}
					got, _ := req["session_name"].(string)
					if got != want {
						t.Errorf("session_name = %q, want %q", got, want)
					}
					w.Write([]byte(`{"terminal_id":"t","status":"completed"}`))
					return
				}
				w.Write([]byte(`{"id":"t"}`))
			}))
			defer srv.Close()
			_, err := (CAOStepper{Client: cao.NewClient(srv.URL), Session: "cao-run"}).Run(context.Background(), Step{ReuseTerminal: reuse})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCAOWatcherReadsClaudeCodesCursorPositionedPermissionPrompt(t *testing.T) {
	raw := "\x1b[H\x1b[2J\x1b[1CPermission\x1b[13Grule\x1b[18G\x1b[1mBash(cp*)\x1b[28G\x1b[22mrequires\x1b[37Gconfirmation\x1b[50Gfor\x1b[54Gthis\x1b[59Gcommand.\r\x1b[1BDo\x1b[4Gyou\x1b[8Gwant\x1b[13Gto\x1b[16Gproceed?\r\x1b[1B❯ 1. Yes\r\x1b[1B  2. No\r\x1b[1BEsc to cancel · Tab to amend"
	raw += strings.Repeat("\x1b[50;1H\x1b[H\x1b[9B⏺", 30)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"output": raw})
	}))
	defer srv.Close()
	screen, err := (CAOWatcher{Client: cao.NewClient(srv.URL), Session: "cao-run"}).Output(context.Background(), "t")
	if err != nil {
		t.Fatal(err)
	}
	if got := FindBlocker(screen); got != "Permission rule Bash(cp*) requires confirmation for this command." {
		t.Fatalf("blocker = %q, screen = %q", got, screen)
	}
}

func TestAReusedTerminalGetsTheRunningStepsDescriptionBeforeItsCue(t *testing.T) {
	updated := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "PATCH" && r.URL.Path == "/terminals/standing/description" {
			var body struct {
				Description string `json:"description"`
			}
			json.NewDecoder(r.Body).Decode(&body)
			if body.Description != "km r · tester" {
				t.Errorf("description = %q", body.Description)
			}
			updated = true
		}
		if r.Method == "POST" {
			if !updated {
				t.Error("cue ran before description was updated")
			}
			w.Write([]byte(`{"terminal_id":"standing"}`))
			return
		}
		w.Write([]byte(`{"id":"standing"}`))
	}))
	defer srv.Close()
	_, err := (CAOStepper{Client: cao.NewClient(srv.URL)}).Run(context.Background(), Step{ReuseTerminal: "standing", Label: "km r · tester"})
	if err != nil {
		t.Fatal(err)
	}
	if !updated {
		t.Fatal("running description was never set")
	}
}
