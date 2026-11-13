package loop

import (
	"context"
	"encoding/json"
	"fmt"
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
