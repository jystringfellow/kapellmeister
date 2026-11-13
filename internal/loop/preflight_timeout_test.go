package loop

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/jystringfellow/kapellmeister/internal/cao"
)

func TestPreflightMarksOnlyAStepTimeoutAsTimedOut(t *testing.T) {
	for _, kind := range []string{"timeout", "wrapped timeout", "seat deadline", "no reply", "400", "500", "blocker"} {
		t.Run(kind, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				host := &preflightHost{fakeStepper: &fakeStepper{t: t, dir: t.TempDir()}}
				stepErr := error(&cao.HTTPError{Method: "POST", Path: "/terminals/run-step", Status: 504, Kind: "timeout"})
				switch kind {
				case "wrapped timeout":
					stepErr = fmt.Errorf("step: %w", stepErr)
				case "seat deadline":
					host.seat = func(context.Context, string, Chair, string) (Seat, error) { return Seat{}, context.DeadlineExceeded }
				case "no reply", "blocker":
					stepErr = nil
				case "400":
					stepErr = &cao.HTTPError{Method: "POST", Path: "/terminals/run-step", Status: 400}
				case "500":
					stepErr = &cao.HTTPError{Method: "POST", Path: "/terminals/run-step", Status: 500}
				}
				if kind == "blocker" {
					host.screen = func(context.Context, string) (string, string, error) { return "", "Do you want to proceed?", nil }
				}
				host.contextActions = map[string]func(context.Context, Step) error{"preflight": func(context.Context, Step) error { return stepErr }}
				results, err := Preflight(context.Background(), host, host.dir, []Chair{{Provider: "a"}}, time.Minute)
				if err != nil || len(results) != 1 {
					t.Fatal(results, err)
				}
				data, err := json.Marshal(results[0])
				if err != nil {
					t.Fatal(err)
				}
				var fields map[string]any
				if err := json.Unmarshal(data, &fields); err != nil {
					t.Fatal(err)
				}
				timedOut, _ := fields["timed_out"].(bool)
				if timedOut != (kind == "timeout" || kind == "wrapped timeout") {
					t.Fatalf("%s: %s", kind, data)
				}
				if results[0].OK {
					t.Fatal(results)
				}
			})
		})
	}
}

func TestPreflightTearsDownAChairThatTimedOutAndStartsItOnceMoreReportingTheRetry(t *testing.T) {
	assertTimeoutRetry(t, "pass")
}
func TestPreflightKeepsTheSecondTimeoutAndItsAttachTargetAfterOneRetry(t *testing.T) {
	assertTimeoutRetry(t, "timeout")
}
func TestPreflightDoesNotRetryATimeoutThatShowsABlockerOnScreen(t *testing.T) {
	assertTimeoutRetry(t, "blocker")
}
func TestPreflightDoesNotAddATimeoutRetryToOtherFailures(t *testing.T) {
	assertTimeoutRetry(t, "400")
}
func assertTimeoutRetry(t *testing.T, outcome string) {
	t.Helper()
	synctest.Test(t, func(t *testing.T) {
		host := &preflightHost{fakeStepper: &fakeStepper{t: t, dir: t.TempDir()}}
		calls := 0
		host.contextActions = map[string]func(context.Context, Step) error{"preflight": func(_ context.Context, s Step) error {
			calls++
			if outcome == "400" {
				return &cao.HTTPError{Status: 400, Body: "failure"}
			}
			if calls == 2 && outcome == "pass" {
				host.write(s.ReplyPath, "ready")
				return nil
			}
			return context.DeadlineExceeded
		}}
		if outcome == "blocker" {
			host.screen = func(context.Context, string) (string, string, error) { return "", "Trust this folder?", nil }
		}
		results, err := Preflight(context.Background(), host, host.dir, []Chair{{Provider: "codex"}}, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		r := results[0]
		data, _ := json.Marshal(r)
		var fields map[string]any
		json.Unmarshal(data, &fields)
		want := 2
		if outcome == "blocker" {
			want = 1
		}
		retried, _ := fields["timeout_retried"].(bool)
		if calls != want || retried != (outcome == "pass" || outcome == "timeout") || len(host.seated) != want {
			t.Fatalf("calls=%d seated=%v result=%s", calls, host.seated, data)
		}
		if outcome == "pass" {
			if !r.OK || len(host.unseated) != 2 || r.Session != "" {
				t.Fatal(r, host.unseated)
			}
		} else if outcome == "timeout" {
			if r.OK || !r.TimedOut || len(host.unseated) != 1 || r.Session == "" || !strings.HasSuffix(r.Session, "r") {
				t.Fatal(r, host.unseated)
			}
		}
	})
}
