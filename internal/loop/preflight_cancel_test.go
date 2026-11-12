package loop

import (
	"context"
	"errors"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/jystringfellow/kapellmeister/internal/cao"
)

type preflightHost struct {
	*fakeStepper
	seat   func(context.Context, string, Chair, string) (Seat, error)
	unseat func(context.Context, string) error
	screen func(context.Context, string) (string, string, error)
}

func (h *preflightHost) Seat(ctx context.Context, name string, chair Chair, dir string) (Seat, error) {
	if h.seat != nil {
		return h.seat(ctx, name, chair, dir)
	}
	return h.fakeStepper.Seat(ctx, name, chair, dir)
}
func (h *preflightHost) Unseat(ctx context.Context, session string) error {
	if h.unseat != nil {
		return h.unseat(ctx, session)
	}
	return h.fakeStepper.Unseat(ctx, session)
}
func (h *preflightHost) Screen(ctx context.Context, id string) (string, string, error) {
	if h.screen != nil {
		return h.screen(ctx, id)
	}
	return h.fakeStepper.Screen(ctx, id)
}

func TestACancelledPreflightTakesDownEverySessionItSeatedIncludingChairsStillInFlight(t *testing.T) {
	dir := t.TempDir()
	host := &preflightHost{fakeStepper: &fakeStepper{t: t, dir: dir}}
	started := make(chan Step, 3)
	host.contextActions = map[string]func(context.Context, Step) error{"preflight": func(ctx context.Context, s Step) error {
		started <- s
		<-ctx.Done()
		return ctx.Err()
	}}
	host.unseat = func(ctx context.Context, session string) error {
		if ctx.Err() != nil {
			t.Errorf("teardown context cancelled: %v", ctx.Err())
		}
		if _, ok := ctx.Deadline(); !ok {
			t.Error("teardown has no deadline")
		}
		return host.fakeStepper.Unseat(ctx, session)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := Preflight(ctx, host, dir, []Chair{{Provider: "a"}, {Provider: "b"}, {Provider: "c"}}, time.Minute)
		done <- err
	}()
	for range 3 {
		s := <-started
		if s.ReuseTerminal == "" {
			t.Error("run-step created an unknown terminal")
		}
	}
	cancel()
	err := <-done
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want cancellation", err)
	}
	if len(host.seated) != 3 || len(host.unseated) != 3 {
		t.Fatalf("seated %v, unseated %v", host.seated, host.unseated)
	}
	seen := map[string]bool{}
	for _, session := range host.unseated {
		if host.seated[session] == "" || seen[session] {
			t.Errorf("deleted foreign or duplicate session %q", session)
		}
		seen[session] = true
		if !strings.HasPrefix(session, "cao-km-preflight-") {
			t.Errorf("session has no km-chosen identity: %q", session)
		}
	}
}

func TestACancelDuringSeatingStillTakesDownTheSessionCAOCreated(t *testing.T) {
	dir := t.TempDir()
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		host := &preflightHost{fakeStepper: &fakeStepper{t: t, dir: dir}}
		host.seat = func(seatCtx context.Context, name string, chair Chair, dir string) (Seat, error) {
			cancel()
			time.Sleep(time.Second)
			if seatCtx.Err() != nil {
				t.Errorf("seating abandoned: %v", seatCtx.Err())
			}
			if _, ok := seatCtx.Deadline(); !ok {
				t.Error("seating has no bound")
			}
			return host.fakeStepper.Seat(seatCtx, name, chair, dir)
		}
		_, err := Preflight(ctx, host, dir, []Chair{{Provider: "a"}}, time.Minute)
		if !errors.Is(err, context.Canceled) {
			t.Errorf("error = %v", err)
		}
		if len(host.seated) != 1 || len(host.unseated) != 1 || len(host.steps) != 0 {
			t.Fatalf("seated %v, unseated %v, steps %v", host.seated, host.unseated, host.steps)
		}
	})
}

func TestPreflightTeardownIsBoundedWhenCAOStopsAnswering(t *testing.T) {
	dir := t.TempDir()
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		host := &preflightHost{fakeStepper: &fakeStepper{t: t, dir: dir}}
		started := make(chan struct{}, 3)
		host.contextActions = map[string]func(context.Context, Step) error{"preflight": func(ctx context.Context, _ Step) error {
			started <- struct{}{}
			<-ctx.Done()
			return ctx.Err()
		}}
		go func() {
			for range 3 {
				<-started
			}
			cancel()
		}()
		host.unseat = func(ctx context.Context, _ string) error { <-ctx.Done(); return ctx.Err() }
		start := time.Now()
		_, err := Preflight(ctx, host, dir, []Chair{{Provider: "a"}, {Provider: "b"}, {Provider: "c"}}, time.Minute)
		if !errors.Is(err, context.Canceled) {
			t.Errorf("error = %v", err)
		}
		if elapsed := time.Since(start); elapsed != 30*time.Second {
			t.Errorf("teardown took %s, want 30s", elapsed)
		}
	})
}

func TestTwoPreflightsSeatTheirChairsUnderDifferentNames(t *testing.T) {
	dir := t.TempDir()
	host := &preflightHost{fakeStepper: &fakeStepper{t: t, dir: dir}}
	host.actions = map[string]func(Step){"preflight": func(s Step) { host.write(s.ReplyPath, "ready") }}
	for range 2 {
		if _, err := Preflight(context.Background(), host, dir, []Chair{{Provider: "a"}}, time.Minute); err != nil {
			t.Fatal(err)
		}
	}
	if len(host.seated) != 2 {
		t.Fatalf("sessions collided or never seated: %v", host.seated)
	}
}

func TestCancelTakesDownBlockedChairsWithoutDeletingPassingChairsTwice(t *testing.T) {
	dir := t.TempDir()
	host := &preflightHost{fakeStepper: &fakeStepper{t: t, dir: dir}}
	passed := make(chan struct{})
	blocked := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	host.contextActions = map[string]func(context.Context, Step) error{"preflight": func(ctx context.Context, s Step) error {
		switch s.Chair.Provider {
		case "ready":
			host.write(s.ReplyPath, "ready")
			return nil
		case "blocked":
			return errors.New("needs user")
		default:
			<-passed
			<-blocked
			cancel()
			return ctx.Err()
		}
	}}
	host.screen = func(context.Context, string) (string, string, error) {
		close(blocked)
		return "a-foreign-session", "Do you trust this folder?", nil
	}
	host.unseat = func(ctx context.Context, session string) error {
		err := host.fakeStepper.Unseat(ctx, session)
		if strings.HasSuffix(session, "-0") {
			close(passed)
		}
		return err
	}
	results, err := Preflight(ctx, host, dir, []Chair{{Provider: "ready"}, {Provider: "blocked"}, {Provider: "in-flight"}}, time.Minute)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v", err)
	}
	if len(host.unseated) != 3 {
		t.Fatalf("unseated %v, want every owned session once", host.unseated)
	}
	seen := map[string]bool{}
	for _, session := range host.unseated {
		if seen[session] || host.seated[session] == "" {
			t.Errorf("deleted foreign or duplicate session %s", session)
		}
		seen[session] = true
	}
	for _, r := range results {
		if r.Session != "" || r.TerminalID != "" {
			t.Errorf("removed chair retains attach target: %+v", r)
		}
	}
}

func TestPreflightReportsTeardownFailuresAndStillTakesDownTheOtherChairs(t *testing.T) {
	dir := t.TempDir()
	host := &preflightHost{fakeStepper: &fakeStepper{t: t, dir: dir}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan struct{}, 2)
	host.contextActions = map[string]func(context.Context, Step) error{"preflight": func(ctx context.Context, _ Step) error { started <- struct{}{}; <-ctx.Done(); return ctx.Err() }}
	host.unseat = func(ctx context.Context, session string) error {
		if strings.HasSuffix(session, "-0") {
			return errors.New("CAO refused deletion")
		}
		return host.fakeStepper.Unseat(ctx, session)
	}
	go func() { <-started; <-started; cancel() }()
	results, err := Preflight(ctx, host, dir, []Chair{{Provider: "a"}, {Provider: "b"}}, time.Minute)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v", err)
	}
	if len(results[0].TeardownErrors) != 1 || !strings.Contains(results[0].TeardownErrors[0], results[0].Session) || !strings.Contains(results[0].TeardownErrors[0], "CAO refused deletion") {
		t.Errorf("missing named failure: %+v", results[0])
	}
	if results[0].Unseated != 0 || results[1].Unseated != 1 {
		t.Errorf("wrong teardown counts: %+v", results)
	}
}

func TestABlockedPreflightKeepsTheSeatedSessionWhenScreenFails(t *testing.T) {
	dir := t.TempDir()
	host := &preflightHost{fakeStepper: &fakeStepper{t: t, dir: dir}}
	host.contextActions = map[string]func(context.Context, Step) error{"preflight": func(context.Context, Step) error {
		return &cao.TimeoutError{Method: "POST", Path: "/terminals/run-step", Limit: time.Second}
	}}
	host.screen = func(context.Context, string) (string, string, error) { return "", "", errors.New("tmux unavailable") }
	results, err := Preflight(context.Background(), host, dir, []Chair{{Provider: "a"}}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	r := results[0]
	if r.OK || r.Retried || r.Session == "" || host.seated[r.Session] != r.TerminalID || len(host.unseated) != 0 {
		t.Fatalf("blocked chair lost its attach target: %+v; unseated %v", r, host.unseated)
	}
}

func TestPreflightWaitsOnlyForTheSeatingBoundAfterAnInterrupt(t *testing.T) {
	dir := t.TempDir()
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		host := &preflightHost{fakeStepper: &fakeStepper{t: t, dir: dir}}
		host.seat = func(seatCtx context.Context, name string, chair Chair, dir string) (Seat, error) {
			cancel()
			<-seatCtx.Done()
			seat, _ := host.fakeStepper.Seat(seatCtx, name, chair, dir)
			return seat, seatCtx.Err()
		}
		start := time.Now()
		_, err := Preflight(ctx, host, dir, []Chair{{Provider: "a"}}, DefaultSettings().PreflightTimeout)
		if !errors.Is(err, context.Canceled) {
			t.Errorf("error = %v", err)
		}
		if elapsed := time.Since(start); elapsed != 90*time.Second {
			t.Errorf("seating took %s, want 90s", elapsed)
		}
		if len(host.unseated) != 1 {
			t.Errorf("seated session leaked: %v", host.seated)
		}
	})
}

func TestPreflightNeverCuesAnUnidentifiedSeat(t *testing.T) {
	for name, seat := range map[string]Seat{"no IDs": {}, "terminal only": {TerminalID: "owned-terminal"}, "session only": {Session: "owned-session"}} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			host := &preflightHost{fakeStepper: &fakeStepper{t: t, dir: dir}}
			host.actions = map[string]func(Step){"preflight": func(s Step) { host.write(s.ReplyPath, "ready") }}
			host.seat = func(context.Context, string, Chair, string) (Seat, error) { return seat, nil }
			results, err := Preflight(context.Background(), host, dir, []Chair{{Provider: "a"}}, time.Minute)
			if err != nil || len(results) != 1 || results[0].OK || !strings.Contains(results[0].Error, "no terminal or session") {
				t.Fatalf("invalid seat was not reported: %+v, %v", results, err)
			}
			if len(host.steps) != 0 {
				t.Fatalf("cued a chair without an identified session and terminal: %v", host.steps)
			}
		})
	}
}

func TestPreflightDeletesTheChosenNameWhenSeatingFailsWithoutASession(t *testing.T) {
	for _, tc := range []struct {
		name        string
		cancelAt    int
		timeout     bool
		deleteFails bool
		deleteHangs bool
	}{
		{name: "both attempts fail"},
		{name: "launch times out", timeout: true},
		{name: "cancel during first attempt", cancelAt: 1},
		{name: "cancel during retry", cancelAt: 2},
		{name: "cancel before launch timeout", cancelAt: 1, timeout: true},
		{name: "deletion failures are reported", deleteFails: true},
		{name: "cancelled deletion failure is reported", cancelAt: 1, deleteFails: true},
		{name: "cancelled deletion is bounded", cancelAt: 1, deleteHangs: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				host := &preflightHost{fakeStepper: &fakeStepper{t: t, dir: dir}}
				var chosen, deleted []string
				host.seat = func(seatCtx context.Context, name string, _ Chair, _ string) (Seat, error) {
					chosen = append(chosen, name)
					if len(chosen) == tc.cancelAt {
						cancel()
					}
					if tc.timeout {
						<-seatCtx.Done()
						return Seat{}, &cao.TimeoutError{Method: "POST", Path: "/sessions", Limit: 90 * time.Second}
					}
					return Seat{}, errors.New("launch failed without an ID")
				}
				host.unseat = func(cleanupCtx context.Context, name string) error {
					if cleanupCtx.Err() != nil {
						t.Errorf("cleanup context already cancelled: %v", cleanupCtx.Err())
					}
					deadline, ok := cleanupCtx.Deadline()
					if !ok || time.Until(deadline) > 30*time.Second {
						t.Errorf("cleanup lacks a 30s bound: %v, %v", deadline, ok)
					}
					deleted = append(deleted, name)
					if tc.deleteHangs {
						<-cleanupCtx.Done()
						return cleanupCtx.Err()
					}
					if tc.deleteFails {
						return errors.New("delete failed")
					}
					return nil
				}
				start := time.Now()
				results, err := Preflight(ctx, host, dir, []Chair{{Provider: "a"}}, time.Minute)
				if tc.deleteHangs && time.Since(start) != 30*time.Second {
					t.Errorf("cleanup took %s, want 30s", time.Since(start))
				}
				if tc.cancelAt > 0 {
					if !errors.Is(err, context.Canceled) {
						t.Errorf("error = %v, want cancellation", err)
					}
				} else if err != nil {
					t.Fatal(err)
				}
				attempts := 2
				if tc.timeout || tc.cancelAt == 1 {
					attempts = 1
				}
				if len(chosen) != attempts || len(deleted) != attempts {
					t.Fatalf("chosen %v, deleted %v; want %d attempts cleaned up", chosen, deleted, attempts)
				}
				equal(t, deleted, chosen)
				if len(host.steps) != 0 {
					t.Errorf("cued after failed launch: %v", host.steps)
				}
				r := results[0]
				if tc.deleteFails || tc.deleteHangs {
					if r.Unseated != 0 || len(r.TeardownErrors) != attempts {
						t.Fatalf("lost teardown failures: %+v", r)
					}
					problem := "delete failed"
					if tc.deleteHangs {
						problem = context.DeadlineExceeded.Error()
					}
					for i, name := range chosen {
						if !strings.Contains(r.TeardownErrors[i], name) || !strings.Contains(r.TeardownErrors[i], problem) {
							t.Errorf("missing named failure: %v", r.TeardownErrors)
						}
					}
				} else if r.Unseated != attempts || len(r.TeardownErrors) != 0 {
					t.Errorf("lost teardown counts: %+v", r)
				}
			})
		})
	}
}

func TestPreflightGivesTheSeatADeadlineDerivedFromItsTimeout(t *testing.T) {
	for _, tc := range []struct{ timeout, want time.Duration }{
		{4 * time.Minute, 90 * time.Second}, {8 * time.Minute, 3 * time.Minute}, {50 * time.Millisecond, 10 * time.Second},
	} {
		host := &preflightHost{fakeStepper: &fakeStepper{t: t, dir: t.TempDir()}}
		host.seat = func(ctx context.Context, name string, chair Chair, dir string) (Seat, error) {
			deadline, ok := ctx.Deadline()
			remaining := time.Until(deadline)
			if !ok || remaining > tc.want || remaining < tc.want-10*time.Second {
				t.Errorf("seat deadline remaining %s, want near %s", remaining, tc.want)
			}
			return Seat{}, errors.New("seat unavailable")
		}
		_, err := Preflight(context.Background(), host, host.dir, []Chair{{Provider: "p"}}, tc.timeout)
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestSeatTimeoutIsThreeEighthsOfThePreflightTimeoutWithTodaysValueAtTheDefault(t *testing.T) {
	for _, tc := range []struct{ timeout, want time.Duration }{
		{DefaultSettings().PreflightTimeout, 90 * time.Second}, {8 * time.Minute, 3 * time.Minute}, {50 * time.Millisecond, 10 * time.Second},
	} {
		if got := seatTimeout(tc.timeout); got != tc.want {
			t.Errorf("seatTimeout(%s) = %s, want %s", tc.timeout, got, tc.want)
		}
	}
}
