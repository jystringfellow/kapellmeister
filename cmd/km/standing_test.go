package main

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jystringfellow/kapellmeister/internal/ensemble"
	"github.com/jystringfellow/kapellmeister/internal/loop"
)

func standingState(t *testing.T) string {
	t.Helper()
	t.Setenv(loop.HomeEnv, t.TempDir())
	t.Setenv(loop.StateEnv, t.TempDir())
	dir := filepath.Join(loop.StateDir(), "ensembles")
	for _, s := range []ensemble.Standing{
		{Session: "alpha", Ensemble: "thorough", Worktree: "/work/alpha", Chairs: []ensemble.StandingChair{{Address: "a"}, {Address: "b"}}},
		{Session: "beta", Ensemble: "quick", Worktree: "/work/beta", Chairs: []ensemble.StandingChair{{Address: "c"}}},
	} {
		if err := s.Save(dir); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestEnsembleListShowsEachStandingEnsemble(t *testing.T) {
	standingState(t)
	var out bytes.Buffer
	if err := ensembleCommand([]string{"list"}, &out); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("output = %q", out.String())
	}
	for i, want := range []string{"SESSION ENSEMBLE WORKTREE CHAIRS", "alpha thorough /work/alpha 2", "beta quick /work/beta 1"} {
		if got := strings.Join(strings.Fields(lines[i]), " "); got != want {
			t.Errorf("line = %q, want %q", got, want)
		}
	}
}

func TestEnsembleListSaysWhenThereAreNoStandingEnsembles(t *testing.T) {
	t.Setenv(loop.StateEnv, t.TempDir())
	var out bytes.Buffer
	if err := ensembleCommand([]string{"list"}, &out); err != nil || out.String() != "no standing ensembles\n" {
		t.Fatalf("output = %q, error = %v", out.String(), err)
	}
}

func TestEnsembleDownDeletesTheSessionThenRemovesItsRecord(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusNotFound} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			dir := standingState(t)
			var calls []string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls = append(calls, r.Method+" "+r.URL.Path)
				records, err := ensemble.ListStanding(dir)
				if err != nil || len(records) != 2 {
					t.Errorf("record removed before CAO deletion: %+v, %v", records, err)
				}
				w.WriteHeader(status)
			}))
			defer srv.Close()
			var out bytes.Buffer
			if err := ensembleCommand([]string{"down", "-cao", srv.URL, "alpha"}, &out); err != nil {
				t.Fatal(err)
			}
			if len(calls) != 1 || calls[0] != "DELETE /sessions/alpha" {
				t.Errorf("calls = %v", calls)
			}
			records, err := ensemble.ListStanding(dir)
			if err != nil || len(records) != 1 || records[0].Session != "beta" {
				t.Fatalf("records after deletion = %+v, %v", records, err)
			}
			if !strings.Contains(out.String(), "took down alpha") || (status == 404 && !strings.Contains(out.String(), "already gone")) {
				t.Errorf("output = %q", out.String())
			}
		})
	}
}

func TestEnsembleDownOfAnUnknownSessionListsTheRecordedOnesAndCallsNothing(t *testing.T) {
	standingState(t)
	for _, session := range []string{"missing", "../escape"} {
		err := ensembleCommand([]string{"down", "-cao", unreachableCAO, session}, new(bytes.Buffer))
		if err == nil || !strings.Contains(err.Error(), "unknown session") || !strings.Contains(err.Error(), "recorded: alpha, beta") {
			t.Fatalf("error = %v", err)
		}
	}
	t.Setenv(loop.StateEnv, t.TempDir())
	err := ensembleCommand([]string{"down", "-cao", unreachableCAO, "missing"}, new(bytes.Buffer))
	if err == nil || !strings.Contains(err.Error(), "none recorded") {
		t.Fatalf("error = %v", err)
	}
}

func TestEnsembleDownKeepsTheRecordWhenCAOFails(t *testing.T) {
	for _, unreachable := range []bool{false, true} {
		dir := standingState(t)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) }))
		defer srv.Close()
		url := srv.URL
		if unreachable {
			url = unreachableCAO
		}
		if err := ensembleCommand([]string{"down", "-cao", url, "alpha"}, new(bytes.Buffer)); err == nil {
			t.Fatal("down succeeded despite CAO failure")
		}
		records, err := ensemble.ListStanding(dir)
		if err != nil || len(records) != 2 {
			t.Fatalf("lost record after CAO failure: %+v, %v", records, err)
		}
	}
}

func TestEnsembleNeedsAValidSubcommandAndDownNeedsOneSession(t *testing.T) {
	for _, args := range [][]string{nil, {"unknown"}, {"list", "extra"}, {"down"}, {"down", "a", "b"}} {
		if err := ensembleCommand(args, new(bytes.Buffer)); err == nil || !strings.Contains(err.Error(), "usage: km ensemble") {
			t.Errorf("args %v: error = %v", args, err)
		}
	}
}
