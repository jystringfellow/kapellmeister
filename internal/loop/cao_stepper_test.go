package loop

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jystringfellow/kapellmeister/internal/cao"
)

func hungCAOPreflight(t *testing.T) ([]PreflightResult, int64, time.Duration) {
	t.Helper()
	var calls atomic.Int64
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

func TestPreflightAgainstAHungCAOFailsEachChairWithinItsTimeoutPlusTheMargin(t *testing.T) {
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

func TestPreflightDoesNotRetryAChairWhoseCAOCallTimedOut(t *testing.T) {
	results, calls, _ := hungCAOPreflight(t)
	if calls != int64(len(results)) {
		t.Errorf("requests = %d, want %d", calls, len(results))
	}
	for _, res := range results {
		if res.Retried {
			t.Errorf("timed out chair was retried: %+v", res)
		}
	}
}
