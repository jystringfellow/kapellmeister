package loop

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestSignalContextCancelsWithASignalErrorThatIsStillContextCanceled(t *testing.T) {
	for _, sig := range []os.Signal{syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP} {
		t.Run(sig.String(), func(t *testing.T) {
			ctx, stop := SignalContext(context.Background(), sig)
			defer stop()
			if err := syscall.Kill(os.Getpid(), sig.(syscall.Signal)); err != nil {
				t.Fatal(err)
			}
			<-ctx.Done()
			cause := SignalOf(ctx)
			if cause == nil || cause.Signal != sig || !errors.Is(cause, context.Canceled) {
				t.Fatalf("signal cause = %v", cause)
			}
		})
	}
	ctx, stop := SignalContext(context.Background(), os.Interrupt)
	stop()
	if SignalOf(ctx) != nil || context.Cause(ctx) != context.Canceled {
		t.Fatalf("stop cause = %v", context.Cause(ctx))
	}
}

func TestARunInterruptedBySIGINTFailsWithReasonNamingTheSignal(t *testing.T) {
	assertInterruptedRun(t, syscall.SIGINT, false)
}
func TestARunInterruptedBySIGTERMNamesSIGTERM(t *testing.T) {
	assertInterruptedRun(t, syscall.SIGTERM, false)
}
func TestACancellationWithoutASignalKeepsTheContextCanceledReason(t *testing.T) {
	assertInterruptedRun(t, nil, false)
}
func TestASignalDuringTheTestStepAfterGateApprovalEndsUntestedAndSaysTheApprovalStandsUnverified(t *testing.T) {
	assertInterruptedRun(t, syscall.SIGINT, true)
}
func TestAPlainCancelDuringTheTestStepStillFails(t *testing.T) { assertInterruptedRun(t, nil, true) }

func assertInterruptedRun(t *testing.T, sig os.Signal, tester bool) {
	t.Helper()
	e, f, records := newEngine(t, passing)
	var progress bytes.Buffer
	e.Progress = &progress
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	step := "plan"
	if tester {
		step = "test"
		e.Config.Chairs.Tester = &Chair{Provider: "p", Profile: "tester"}
	}
	f.contextActions = map[string]func(context.Context, Step) error{step: func(ctx context.Context, s Step) error {
		if tester {
			f.write(s.ReplyPath, "partial scenario")
		}
		if sig != nil {
			cancel(&SignalError{Signal: sig})
		} else {
			cancel(context.Canceled)
		}
		return ctx.Err()
	}}
	res, err := e.Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	wantReason := "context canceled"
	wantOutcome := Failed
	if sig != nil {
		wantReason = "interrupted by SIGINT"
		if sig == syscall.SIGTERM {
			wantReason = "interrupted by SIGTERM"
		}
		if tester {
			wantOutcome = Untested
		}
	}
	if res.Outcome != wantOutcome || !strings.Contains(res.Reason, wantReason) {
		t.Errorf("result = %+v", res)
	}
	if sig == nil {
		if strings.Contains(records.String(), `"step":"interrupted"`) {
			t.Errorf("plain cancellation recorded as a signal: %s", records)
		}
		return
	}
	if strings.Contains(res.Reason, "context canceled") || !strings.Contains(progress.String(), wantReason) {
		t.Errorf("reason %q; progress %s", res.Reason, &progress)
	}
	foundInterrupt, foundStep, foundUnverified := false, false, false
	dec := json.NewDecoder(records)
	for dec.More() {
		var r Record
		if err := dec.Decode(&r); err != nil {
			t.Fatal(err)
		}
		if r.Step == "interrupted" && r.Error == wantReason {
			foundInterrupt = true
		}
		if r.Step == step && r.Error == wantReason {
			foundStep = true
		}
		if r.Step == "unverified" && r.Detail == res.Reason {
			foundUnverified = true
		}
	}
	if !foundInterrupt || !foundStep {
		t.Errorf("signal missing from telemetry: interrupted=%v step=%v", foundInterrupt, foundStep)
	}
	if tester {
		for _, want := range []string{"tester did not finish", "gate's approval stands unverified", "partial report:"} {
			if !strings.Contains(res.Reason, want) || !strings.Contains(progress.String(), want) {
				t.Errorf("missing %q: %s; %s", want, res.Reason, &progress)
			}
		}
		if !foundUnverified {
			t.Error("no unverified record")
		}
	}
}

func TestTheTesterBriefingNamesTheConductorsPidAndForbidsSignallingItOrProcessesFoundByName(t *testing.T) {
	e, f := testerEngine(t)
	scratch, err := filepath.EvalSymlinks(e.Config.ScratchDir)
	if err != nil {
		t.Fatal(err)
	}
	e.Config.ScratchDir = scratch
	run(t, e)
	for _, name := range []string{"test-plan", "test"} {
		p := testerSteps(f, name)[0].Prompt
		for _, want := range []string{fmt.Sprintf("process %d", os.Getpid()), "never signal or kill it", "never signal or kill a process found by name", "pgrep", "pkill", "killall", "ps | grep", "signal only processes you started yourself", e.Config.ScratchDir + "/pids"} {
			if !strings.Contains(p, want) {
				t.Errorf("%s prompt lacks %q", name, want)
			}
		}
	}
}

func TestNoOtherPartsPromptMentionsTheConductorsPid(t *testing.T) {
	e, f := testerEngine(t)
	e.Config.Lessons = true
	run(t, e)
	for _, s := range f.steps {
		if s.Name != "test-plan" && s.Name != "test" && strings.Contains(s.Prompt, fmt.Sprintf("process %d", os.Getpid())) {
			t.Errorf("pid in %s prompt", s.Name)
		}
	}
}

func TestASignalDuringParallelReviewsNamesTheSignal(t *testing.T) {
	e, f, records := newEngine(t, passing)
	e.Config.Chairs.Reviewers = []Chair{{Provider: "a"}, {Provider: "b"}}
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	f.contextActions = map[string]func(context.Context, Step) error{"review": func(ctx context.Context, _ Step) error {
		cancel(&SignalError{Signal: syscall.SIGINT})
		<-ctx.Done()
		return ctx.Err()
	}}
	res, err := e.Run(ctx)
	if err != nil || res.Outcome != Failed || res.Reason != "interrupted by SIGINT" {
		t.Fatalf("result %+v; err %v", res, err)
	}
	if strings.Contains(records.String(), "context canceled") {
		t.Errorf("signal lost in review telemetry: %s", records)
	}
}

func TestASignalDoesNotReplaceAnUnrelatedFailure(t *testing.T) {
	e, f, _ := newEngine(t, passing)
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	f.contextActions = map[string]func(context.Context, Step) error{"plan": func(context.Context, Step) error {
		cancel(&SignalError{Signal: syscall.SIGINT})
		return errors.New("unrelated failure")
	}}
	res, err := e.Run(ctx)
	if err != nil || res.Outcome != Failed || res.Reason != "unrelated failure" {
		t.Fatalf("result %+v; err %v", res, err)
	}
}

func TestASignalDuringVerificationNamesTheSignalInsteadOfACommandExitFailure(t *testing.T) {
	e, _, _ := newEngine(t, nil)
	parent, stop := context.WithTimeout(context.Background(), 10*time.Second)
	defer stop()
	ctx, cancel := context.WithCancelCause(parent)
	defer cancel(nil)
	e.Verify = CommandVerifier("printf ready > verify-started; exec sleep 30")
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if _, err := os.Stat(filepath.Join(e.Config.Worktree, "verify-started")); err == nil {
					cancel(&SignalError{Signal: syscall.SIGTERM})
					return
				}
			}
		}
	}()
	res, err := e.Run(ctx)
	<-done
	if err != nil || res.Outcome != Failed || res.Reason != "interrupted by SIGTERM" {
		t.Fatalf("result %+v; err %v", res, err)
	}
}
