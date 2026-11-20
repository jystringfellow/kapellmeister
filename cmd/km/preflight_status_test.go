package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jystringfellow/kapellmeister/internal/loop"
)

func preflightingRun(t *testing.T, f *runCAO) []string {
	t.Helper()
	wt, args := runSessionArgs(t, f)
	writeFile(t, filepath.Join(wt, loop.ConfigFile), "[test]\nverify = 'true'\n[run]\npreflight_timeout = '5s'\nreply_grace = '1ms'\nrounds = 2\n[sizing]\nmode = 'off'\n")
	return args
}

func TestARunIsInStatusFromTheStartWithPreflightAsWhatIsPlaying(t *testing.T) {
	args := preflightingRun(t, &runCAO{})
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST" && r.URL.Path == "/sessions":
			fmt.Fprint(w, `{"id":"t","session_name":"s"}`)
		case r.URL.Path == "/terminals/run-step":
			<-release
			w.WriteHeader(504)
			fmt.Fprint(w, preflightTimeoutBody)
		default:
			fmt.Fprint(w, `{}`)
		}
	}))
	defer srv.Close()
	for i, arg := range args {
		if arg == "-cao" {
			args[i+1] = srv.URL
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runAt(ctx, args, io.Discard, io.Discard, nil) }()
	defer func() { cancel(); <-done; close(release) }()
	eventually(t, func() bool { return len(statusRows(t)) == 1 })
	run := statusRows(t)[0]
	if len(run.Current) != 1 || run.Current[0].Step != "preflight" || run.Liveness != "alive" {
		t.Fatalf("current = %+v, liveness %s", run.Current, run.Liveness)
	}
	if run.Score == nil || len(run.Score.Steps) == 0 || len(run.Ensemble.Sections) != 1 || len(run.Ensemble.Sections[0].Chairs) == 0 || run.CAO == "" {
		t.Fatalf("score %+v, ensemble %+v, cao %q", run.Score, run.Ensemble, run.CAO)
	}
	for _, chair := range run.Ensemble.Sections[0].Chairs {
		if chair.State != "waiting" {
			t.Fatalf("chair %+v", chair)
		}
	}
}

func TestARunWhosePreflightFailsLeavesNothingInStatusOrReport(t *testing.T) {
	args := preflightingRun(t, &runCAO{preflightTimeout: true})
	err := runAt(context.Background(), args, io.Discard, io.Discard, nil)
	if err == nil || !strings.Contains(err.Error(), "preflight failed") {
		t.Fatal(err)
	}
	if rows := statusRows(t); len(rows) != 0 {
		t.Fatal(rows)
	}
	if runs := reportRows(t); len(runs) != 0 {
		t.Fatal(runs)
	}
}
