package loop

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func childStopped(t *testing.T, pidFile string) bool {
	t.Helper()
	data, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	for range 40 {
		if syscall.Kill(pid, 0) != nil {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	_ = syscall.Kill(pid, syscall.SIGKILL)
	return false
}

func TestACommandPastItsLimitIsStoppedWithEveryProcessItStartedAndSaysTheLimit(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "pid")
	start := time.Now()
	v, err := CommandVerifierWithin("sleep 30 & echo $! >"+pidFile+"; echo started; wait", 300*time.Millisecond)(context.Background(), t.TempDir())
	if err != nil || !v.TimedOut || v.ExitCode == 0 || time.Since(start) > 10*time.Second {
		t.Fatalf("got %+v %v after %s", v, err, time.Since(start))
	}
	if !strings.Contains(v.Output, "started") || !strings.Contains(v.Output, "did not finish within 300ms") {
		t.Fatal(v.Output)
	}
	if !childStopped(t, pidFile) {
		t.Fatal("the background process outlived the command")
	}
}

func TestACancelledCommandLeavesNoProcessBehind(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "pid")
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		for {
			if _, err := os.Stat(pidFile); err == nil {
				cancel()
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
	}()
	start := time.Now()
	_, err := CommandVerifier("sleep 30 & echo $! >"+pidFile+"; wait")(ctx, t.TempDir())
	if err == nil || time.Since(start) > 10*time.Second {
		t.Fatalf("got %v after %s", err, time.Since(start))
	}
	if !childStopped(t, pidFile) {
		t.Fatal("the background process outlived the cancel")
	}
}

func TestAVerifyThatTimedOutIsNotRunAgain(t *testing.T) {
	mark := filepath.Join(t.TempDir(), "ran")
	v, err := Retrying(CommandVerifierWithin("echo ran >>"+mark+"; sleep 30", 300*time.Millisecond), 2)(context.Background(), t.TempDir())
	if err != nil || !v.TimedOut || strictRuns(t, mark) != 1 {
		t.Fatalf("got %+v %v, %d runs", v, err, strictRuns(t, mark))
	}
}

func TestStrictPastItsLimitFailsAndNamesTheLimit(t *testing.T) {
	e, _, progress, mark := strictEngine(t, "echo ran >>MARK; sleep 30", "")
	e.Config.StrictTimeout = 300 * time.Millisecond
	res := run(t, e)
	if res.Outcome != StrictFailed || res.Strict == nil || res.Strict.Status != "failed" || !res.Strict.TimedOut || strictRuns(t, mark) != 1 {
		t.Fatalf("got %+v %+v", res, res.Strict)
	}
	want := "strict did not finish within 300ms"
	if !strings.Contains(res.Reason, want) || !strings.Contains(res.Reason, "raise [test] strict_timeout") || !strings.Contains(progress.String(), want) {
		t.Fatal(res.Reason, progress.String())
	}
}

func TestAProbePastTheLimitMeansStrictNotRun(t *testing.T) {
	e, _, _, mark := strictEngine(t, "echo ran >>MARK", "sleep 30")
	e.Config.StrictTimeout = 300 * time.Millisecond
	res := run(t, e)
	if res.Outcome != StrictNotRun || res.Strict == nil || res.Strict.Status != "not_run" || strictRuns(t, mark) != 0 {
		t.Fatalf("got %+v %+v", res, res.Strict)
	}
	if !strings.Contains(res.Reason, "the strict_ready probe did not finish within 300ms") {
		t.Fatal(res.Reason)
	}
}

func TestStrictTimeoutIsAPositiveDurationThatNeedsAStrictCommand(t *testing.T) {
	test, err := strictConfig(t, "strict = \"scripts/strict\"\nstrict_timeout = \"45m\"\n")
	if err != nil || test.StrictLimit(time.Hour) != 45*time.Minute {
		t.Fatalf("got %+v %v", test, err)
	}
	if plain, err := strictConfig(t, "strict = \"scripts/strict\"\n"); err != nil || plain.StrictLimit(time.Hour) != time.Hour {
		t.Fatalf("got %+v %v", plain, err)
	}
	if _, err := strictConfig(t, "strict = \"scripts/strict\"\nstrict_timeout = \"soon\"\n"); err == nil || !strings.Contains(err.Error(), "test.strict_timeout") {
		t.Fatal(err)
	}
	if _, err := strictConfig(t, "strict_timeout = \"45m\"\n"); err == nil || !strings.Contains(err.Error(), "test.strict_timeout: needs test.strict") {
		t.Fatal(err)
	}
}

func TestAShellThatExitsWhileItsChildHoldsTheOutputLeavesNoProcessBehind(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "pid")
	start := time.Now()
	v, err := CommandVerifier("sleep 30 & echo $! >"+pidFile+"; echo done")(context.Background(), t.TempDir())
	if err != nil || v.ExitCode != 0 || !strings.Contains(v.Output, "done") || time.Since(start) > 10*time.Second {
		t.Fatalf("got %+v %v after %s", v, err, time.Since(start))
	}
	if !childStopped(t, pidFile) {
		t.Fatal("the background process outlived the command")
	}
}

func slow(limit time.Duration) Verifier {
	return CommandVerifierWithin("echo started; sleep 30", limit)
}

func TestChairsAreToldAVerifyRanOutOfTimeNotThatItExited(t *testing.T) {
	e, f, _ := newEngine(t, passing)
	calls := 0
	e.Verify = func(ctx context.Context, dir string) (VerifyResult, error) {
		if calls++; calls == 1 {
			return passing(ctx, dir)
		}
		return slow(300*time.Millisecond)(ctx, dir)
	}
	e.Config.MaxRounds = 1
	var progress bytes.Buffer
	e.Progress = &progress
	run(t, e)
	var review string
	for _, s := range f.steps {
		if s.Name == "review" {
			review = s.Prompt
		}
	}
	if !strings.Contains(review, "it did not finish within 300ms and kapellmeister stopped it: it ran out of time, which says nothing about whether the tests pass") || strings.Contains(review, "exited -1") {
		t.Fatal(review)
	}
	if !strings.Contains(progress.String(), "  did not finish within 300ms; stopped after ") || strings.Contains(progress.String(), "exit -1") {
		t.Fatal(progress.String())
	}
}

func TestABaselineVerifyThatRanOutOfTimeSaysSoAndNamesTheLimit(t *testing.T) {
	e, _, _ := newEngine(t, slow(300*time.Millisecond))
	res := run(t, e)
	if res.Outcome != Failed || !strings.Contains(res.Reason, "verify did not finish within 300ms before any change, so km stopped it: make verify faster or raise [test] verify_timeout") || strings.Contains(res.Reason, "exit -1") {
		t.Fatalf("got %+v", res)
	}
}
