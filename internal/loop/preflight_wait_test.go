package loop

import (
	"context"
	"errors"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

const stillLoading = "Managed account policy is still loading. Please wait a moment and try again."

func TestARefusalThatSaysToWaitAndTryAgainIsNotABlocker(t *testing.T) {
	if got := FindBlocker("> ready check\n" + stillLoading + "\n"); got != "" {
		t.Fatalf("blocker = %q", got)
	}
}

func waitingHost(t *testing.T, dir string, check func(attempt int, s Step) error) *preflightHost {
	t.Helper()
	host := &preflightHost{fakeStepper: &fakeStepper{t: t, dir: dir}}
	attempt := 0
	host.contextActions = map[string]func(context.Context, Step) error{"preflight": func(_ context.Context, s Step) error {
		attempt++
		return check(attempt, s)
	}}
	return host
}

func TestPreflightWaitsBeforeItsOneRetryWhenACheckFailsFastWithNoBlockerOnScreen(t *testing.T) {
	dir := t.TempDir()
	synctest.Test(t, func(t *testing.T) {
		var host *preflightHost
		host = waitingHost(t, dir, func(attempt int, s Step) error {
			if attempt == 1 {
				return errors.New(stillLoading)
			}
			host.write(s.ReplyPath, "ready")
			return nil
		})
		host.screen = func(context.Context, string) (string, string, error) { return "", stillLoading, nil }
		start := time.Now()
		results, err := PreflightRetryingAfter(context.Background(), host, dir, []Chair{{Provider: "a"}}, time.Minute, 20*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		r := results[0]
		if !r.OK || !r.Retried || len(host.seated) != 2 || time.Since(start) != 20*time.Second {
			t.Fatalf("result %+v after %s; seats %v", r, time.Since(start), host.seated)
		}
		if r.RetryWaitMS != 20000 || r.RetryWaitError != stillLoading {
			t.Fatalf("wait record %+v", r)
		}
	})
}

func TestPreflightRetriesAtOnceWhenTheWaitIsZeroOrTheCheckDidNotFailFast(t *testing.T) {
	for name, wait := range map[string]time.Duration{"zero wait": 0, "slow failure": 20 * time.Second} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			synctest.Test(t, func(t *testing.T) {
				host := waitingHost(t, dir, func(int, Step) error {
					if wait > 0 {
						time.Sleep(15 * time.Second)
					}
					return errors.New("refused")
				})
				start := time.Now()
				results, err := PreflightRetryingAfter(context.Background(), host, dir, []Chair{{Provider: "a"}}, time.Minute, wait)
				if err != nil {
					t.Fatal(err)
				}
				want := time.Duration(0)
				if wait > 0 {
					want = 30 * time.Second
				}
				if r := results[0]; r.OK || !r.Retried || len(host.seated) != 2 || time.Since(start) != want || r.RetryWaitMS != 0 {
					t.Fatalf("result %+v after %s; seats %v", r, time.Since(start), host.seated)
				}
			})
		})
	}
}

func TestPreflightDoesNotWaitOrRetryWhenAFastFailureLeavesABlockerOnScreen(t *testing.T) {
	dir := t.TempDir()
	synctest.Test(t, func(t *testing.T) {
		host := waitingHost(t, dir, func(int, Step) error { return errors.New("refused") })
		host.screen = func(context.Context, string) (string, string, error) { return "", "Please sign in to continue", nil }
		start := time.Now()
		results, err := PreflightRetryingAfter(context.Background(), host, dir, []Chair{{Provider: "a"}}, time.Minute, 20*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		if r := results[0]; r.OK || r.Retried || len(host.seated) != 1 || time.Since(start) != 0 || !strings.Contains(r.Blocker, "sign in") {
			t.Fatalf("result %+v after %s; seats %v", r, time.Since(start), host.seated)
		}
	})
}

func TestACancelDuringTheWaitBeforeARetryEndsWithoutAnotherSeat(t *testing.T) {
	dir := t.TempDir()
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		host := waitingHost(t, dir, func(int, Step) error { return errors.New("refused") })
		cancelled := make(chan struct{})
		go func() { time.Sleep(5 * time.Second); cancel(); close(cancelled) }()
		start := time.Now()
		_, err := PreflightRetryingAfter(ctx, host, dir, []Chair{{Provider: "a"}}, time.Minute, 20*time.Second)
		if !errors.Is(err, context.Canceled) || len(host.seated) != 1 || time.Since(start) != 5*time.Second {
			t.Fatalf("err %v after %s; seats %v", err, time.Since(start), host.seated)
		}
		<-cancelled
	})
}

func TestTheWaitBeforeAPreflightRetryDefaultsToTwentySecondsAndCanBeOverriddenOrDisabled(t *testing.T) {
	if got := DefaultSettings().PreflightRetryWait; got != 20*time.Second {
		t.Fatalf("default = %s", got)
	}
	for _, value := range []string{"4m", "0", "-1s", "soon"} {
		t.Run(value, func(t *testing.T) {
			repo := t.TempDir()
			writeConfig(t, repo, "[run]\npreflight_retry_wait = '"+value+"'\n")
			s, err := LoadSettings(writeUserConfig(t, ""), repo)
			if value == "-1s" || value == "soon" {
				if err == nil || !strings.Contains(err.Error(), "run.preflight_retry_wait") {
					t.Fatalf("error = %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if want, _ := time.ParseDuration(value); s.PreflightRetryWait != want {
				t.Fatalf("wait = %s", s.PreflightRetryWait)
			}
		})
	}
}
