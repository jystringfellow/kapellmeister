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
