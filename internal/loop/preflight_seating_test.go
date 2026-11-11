package loop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/jystringfellow/kapellmeister/internal/cao"
)

func TestPreflightSeatsOneChairAtATimeButChecksThemTogether(t *testing.T) {
	dir := t.TempDir()
	synctest.Test(t, func(t *testing.T) {
		host := &preflightHost{fakeStepper: &fakeStepper{t: t, dir: dir}}
		var mu sync.Mutex
		active, peak, checks := 0, 0, 0
		allChecks := make(chan struct{})
		host.seat = func(ctx context.Context, name string, c Chair, dir string) (Seat, error) {
			mu.Lock()
			active++
			peak = max(peak, active)
			mu.Unlock()
			time.Sleep(time.Second)
			mu.Lock()
			active--
			mu.Unlock()
			return host.fakeStepper.Seat(ctx, name, c, dir)
		}
		host.contextActions = map[string]func(context.Context, Step) error{"preflight": func(_ context.Context, s Step) error {
			mu.Lock()
			checks++
			if checks == 3 {
				close(allChecks)
			}
			mu.Unlock()
			<-allChecks
			host.write(s.ReplyPath, "ready")
			return nil
		}}
		results, err := Preflight(context.Background(), host, dir, []Chair{{Provider: "a"}, {Provider: "b"}, {Provider: "c"}}, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		if peak != 1 || checks != 3 {
			t.Errorf("simultaneous seats=%d, checks=%d", peak, checks)
		}
		for _, r := range results {
			if !r.OK {
				t.Errorf("result %+v", r)
			}
		}
	})
}

func TestPreflightRetriesAnHTTP500SeatWithGrowingWaitsAndRenamedSessions(t *testing.T) {
	assertSeatRetry(t, "500", 2, 2)
}
func TestPreflightRetriesA400ThatSaysTheSessionJustAskedForWasNotFound(t *testing.T) {
	assertSeatRetry(t, "400 session", 2, 2)
}
func TestPreflightDoesNotBackOffForA400AboutSomethingElse(t *testing.T) {
	assertSeatRetry(t, "400 profile", 2, 1)
}
func TestPreflightStopsRetryingASeatAfterTheLastBackoffAndReportsTheLastError(t *testing.T) {
	assertSeatRetry(t, "500", 9, 3)
}
func TestPreflightDoesNotRetryASeatWhoseCAOCallTimedOut(t *testing.T) {
	assertSeatRetry(t, "timeout", 9, 0)
}

func assertSeatRetry(t *testing.T, kind string, failures, retries int) {
	t.Helper()
	dir := t.TempDir()
	synctest.Test(t, func(t *testing.T) {
		host := &preflightHost{fakeStepper: &fakeStepper{t: t, dir: dir}}
		var names, deleted []string
		var starts []time.Time
		host.seat = func(ctx context.Context, name string, c Chair, dir string) (Seat, error) {
			names = append(names, name)
			starts = append(starts, time.Now())
			if len(names) > failures {
				return host.fakeStepper.Seat(ctx, name, c, dir)
			}
			status, body := 500, fmt.Sprintf("Could not find object attempt %d", len(names))
			if kind == "400 session" {
				status = 400
				body = fmt.Sprintf(`{"detail":"Session '%s' not found"}`, name)
			}
			if kind == "400 profile" {
				status = 400
				body = `{"detail":"Profile 'missing' not found"}`
			}
			if kind == "timeout" {
				return Seat{}, &cao.TimeoutError{Method: "POST", Path: "/sessions", Limit: time.Second}
			}
			return Seat{}, &cao.HTTPError{Status: status, Body: body}
		}
		host.unseat = func(_ context.Context, name string) error { deleted = append(deleted, name); return nil }
		host.actions = map[string]func(Step){"preflight": func(s Step) { host.write(s.ReplyPath, "ready") }}
		results, err := Preflight(context.Background(), host, dir, []Chair{{Provider: "a"}}, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		r := results[0]
		if len(names) != 1+retries {
			t.Fatalf("attempt names=%v; want %d", names, 1+retries)
		}
		for i := 1; i < len(names); i++ {
			suffix := "r"
			if i > 1 {
				suffix = fmt.Sprintf("r%d", i)
			}
			if names[i] != names[0]+suffix {
				t.Errorf("retry name = %s", names[i])
			}
			want := time.Duration(0)
			if kind == "500" || kind == "400 session" {
				want = time.Second << uint(i-1)
			}
			if elapsed := starts[i].Sub(starts[i-1]); elapsed < want || elapsed > want+want/2 {
				t.Errorf("wait %d = %s, want %s plus at most half again", i, elapsed, want)
			}
		}
		equal(t, deleted, names)
		if r.OK != (failures < len(names)) || r.Retried != (retries > 0) {
			t.Errorf("result %+v", r)
		}
		data, _ := json.Marshal(r)
		var fields map[string]any
		json.Unmarshal(data, &fields)
		if kind == "500" || kind == "400 session" {
			if fields["seat_retries"] != float64(retries) || fields["seat_retry_error"] == nil {
				t.Errorf("retry record %s", data)
			}
		}
		if !r.OK && kind == "500" && !strings.Contains(r.Error, fmt.Sprintf("attempt %d", len(names))) {
			t.Errorf("last error lost: %+v", r)
		}
	})
}

func TestACancelDuringASeatBackoffEndsWithoutAnotherSeat(t *testing.T) {
	dir := t.TempDir()
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		host := &preflightHost{fakeStepper: &fakeStepper{t: t, dir: dir}}
		attempts := 0
		host.seat = func(context.Context, string, Chair, string) (Seat, error) {
			attempts++
			return Seat{}, &cao.HTTPError{Status: 500, Body: "race"}
		}
		cancelled := make(chan struct{})
		go func() { time.Sleep(500 * time.Millisecond); cancel(); close(cancelled) }()
		_, err := Preflight(ctx, host, dir, []Chair{{Provider: "a"}}, time.Minute)
		if !errors.Is(err, context.Canceled) || attempts != 1 {
			t.Errorf("attempts %d, err %v", attempts, err)
		}
		<-cancelled
	})
}

func TestACancelWhileWaitingForASeatNeverStartsAnotherSeat(t *testing.T) {
	dir := t.TempDir()
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		host := &preflightHost{fakeStepper: &fakeStepper{t: t, dir: dir}}
		attempts := 0
		host.seat = func(ctx context.Context, name string, c Chair, dir string) (Seat, error) {
			attempts++
			time.Sleep(time.Second)
			cancel()
			return host.fakeStepper.Seat(ctx, name, c, dir)
		}
		_, err := Preflight(ctx, host, dir, []Chair{{Provider: "a"}, {Provider: "b"}, {Provider: "c"}}, time.Minute)
		if !errors.Is(err, context.Canceled) || attempts != 1 {
			t.Errorf("attempts %d, err %v", attempts, err)
		}
	})
}

func TestPreflightKeepsOneImmediateRetryForAnHTTP500FromACheck(t *testing.T) {
	dir := t.TempDir()
	synctest.Test(t, func(t *testing.T) {
		host := &preflightHost{fakeStepper: &fakeStepper{t: t, dir: dir}}
		host.contextActions = map[string]func(context.Context, Step) error{"preflight": func(context.Context, Step) error {
			return &cao.HTTPError{Status: 500, Body: "step failed"}
		}}
		start := time.Now()
		results, err := Preflight(context.Background(), host, dir, []Chair{{Provider: "a"}}, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		r := results[0]
		if len(host.seated) != 2 || len(host.steps) != 2 || time.Since(start) != 0 || r.SeatRetries != 0 || !r.Retried || r.OK {
			t.Errorf("check retry changed: %+v; seats %v; steps %d", r, host.seated, len(host.steps))
		}
	})
}

func TestPreflightDoesNotRetryAChairWhoseDeadlineErrorIsWrapped(t *testing.T) {
	for _, step := range []string{"seat", "check"} {
		t.Run(step, func(t *testing.T) {
			dir := t.TempDir()
			host := &preflightHost{fakeStepper: &fakeStepper{t: t, dir: dir}}
			attempts := 0
			timeout := fmt.Errorf("CAO call: %w", context.DeadlineExceeded)
			host.seat = func(ctx context.Context, name string, chair Chair, dir string) (Seat, error) {
				attempts++
				if step == "seat" {
					return Seat{}, timeout
				}
				return host.fakeStepper.Seat(ctx, name, chair, dir)
			}
			host.contextActions = map[string]func(context.Context, Step) error{"preflight": func(context.Context, Step) error { return timeout }}
			results, err := Preflight(context.Background(), host, dir, []Chair{{Provider: "a"}}, time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			r := results[0]
			if attempts != 1 || r.OK || r.Retried || r.SeatRetries != 0 || r.Error != timeout.Error() {
				t.Errorf("attempts = %d, result = %+v", attempts, r)
			}
			if step == "seat" && r.Unseated != 1 {
				t.Errorf("timed-out seat was not cleaned up: %+v", r)
			}
			if step == "check" && (r.Session == "" || len(host.unseated) != 0) {
				t.Errorf("timed-out check lost its attach target: %+v", r)
			}
		})
	}
}
