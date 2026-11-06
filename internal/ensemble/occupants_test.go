package ensemble

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jystringfellow/kapellmeister/internal/loop"
)

type occupantFake struct {
	run      func(context.Context, loop.Step) (loop.StepResult, error)
	released []string
}

func (f *occupantFake) Run(ctx context.Context, s loop.Step) (loop.StepResult, error) {
	return f.run(ctx, s)
}
func (f *occupantFake) Release(_ context.Context, id string) error {
	f.released = append(f.released, id)
	return nil
}
func (f *occupantFake) Screen(_ context.Context, id string) (string, string, error) {
	return "session", "screen " + id, nil
}

func standingScore() Standing {
	s := Standing{Ensemble: "score", Session: "standing"}
	for i, part := range []string{"planner", "implementer", "reviewer", "reviewer", "gate", "classifier"} {
		name := fmt.Sprintf("chair-%d", i)
		address := part + "." + name + "@score"
		binding := loop.Chair{Provider: "codex", Profile: "km-codex", Model: "recorded-model", Address: address, Tier: "small", Timeout: time.Minute}
		s.Chairs = append(s.Chairs, StandingChair{Address: address, Section: part, Name: name, Part: part, Binding: binding, TerminalID: name})
	}
	return s
}

func TestEachPartAndEachReviewerIsRoutedToItsOwnStandingOccupant(t *testing.T) {
	s := standingScore()
	var got loop.Step
	fake := &occupantFake{run: func(_ context.Context, step loop.Step) (loop.StepResult, error) {
		got = step
		return loop.StepResult{TerminalID: "unexpected", Status: "completed"}, nil
	}}
	stepper := NewOccupantStepper(fake, s)
	for _, c := range s.Chairs {
		t.Run(c.Address, func(t *testing.T) {
			out, err := stepper.Run(context.Background(), loop.Step{Name: c.Part, Chair: c.Binding, ReuseTerminal: "wrong"})
			if err != nil || got.ReuseTerminal != c.TerminalID || out.TerminalID != c.TerminalID || out.Status != "completed" {
				t.Fatalf("step = %+v, result = %+v, error = %v", got, out, err)
			}
		})
	}
}

func TestClassifyStepsSharingOneOccupantNeverOverlap(t *testing.T) {
	s := standingScore()
	s.Chairs[0].TerminalID = s.Chairs[5].TerminalID
	var mu sync.Mutex
	active, maximum := 0, 0
	fake := &occupantFake{run: func(_ context.Context, _ loop.Step) (loop.StepResult, error) {
		mu.Lock()
		active++
		maximum = max(maximum, active)
		mu.Unlock()
		time.Sleep(10 * time.Millisecond)
		mu.Lock()
		active--
		mu.Unlock()
		return loop.StepResult{}, nil
	}}
	stepper := NewOccupantStepper(fake, s)
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			chair := s.Chairs[5].Binding
			if i%2 == 0 {
				chair = s.Chairs[0].Binding
			}
			if _, err := stepper.Run(context.Background(), loop.Step{Name: "classify-review", Chair: chair}); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if maximum != 1 {
		t.Fatalf("maximum concurrent prompts = %d", maximum)
	}
}

func TestStepsOnDifferentOccupantsStillRunTogether(t *testing.T) {
	s := standingScore()
	entered := make(chan string, 2)
	leave := make(chan struct{})
	fake := &occupantFake{run: func(ctx context.Context, step loop.Step) (loop.StepResult, error) {
		entered <- step.ReuseTerminal
		select {
		case <-leave:
			return loop.StepResult{}, nil
		case <-ctx.Done():
			return loop.StepResult{}, ctx.Err()
		}
	}}
	stepper := NewOccupantStepper(fake, s)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var wg sync.WaitGroup
	for _, c := range s.Chairs[2:4] {
		wg.Go(func() {
			if _, err := stepper.Run(ctx, loop.Step{Name: "review", Chair: c.Binding}); err != nil {
				t.Error(err)
			}
		})
	}
	for range 2 {
		select {
		case <-entered:
		case <-ctx.Done():
			t.Error("different occupants did not run together")
		}
	}
	close(leave)
	wg.Wait()
}

func TestAStepWaitingForAnOccupantStopsWaitingWhenItsContextIsCancelled(t *testing.T) {
	s := standingScore()
	entered := make(chan struct{}, 2)
	leave := make(chan struct{})
	fake := &occupantFake{run: func(_ context.Context, _ loop.Step) (loop.StepResult, error) {
		entered <- struct{}{}
		<-leave
		return loop.StepResult{}, nil
	}}
	stepper := NewOccupantStepper(fake, s)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = stepper.Run(context.Background(), loop.Step{Chair: s.Chairs[5].Binding})
	}()
	<-entered
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := stepper.Run(ctx, loop.Step{Chair: s.Chairs[5].Binding}); !errors.Is(err, context.Canceled) {
		t.Errorf("waiting step: %v", err)
	}
	close(leave)
	<-done
	if len(entered) != 0 {
		t.Fatal("cancelled step reached the occupant")
	}
}

func TestReleaseNeverDeletesAStandingOccupant(t *testing.T) {
	s := standingScore()
	fake := &occupantFake{}
	stepper := NewOccupantStepper(fake, s)
	for _, c := range s.Chairs {
		if err := stepper.Release(context.Background(), c.TerminalID); err != nil {
			t.Fatal(err)
		}
	}
	if len(fake.released) != 0 {
		t.Fatalf("released occupants: %v", fake.released)
	}
	if err := stepper.Release(context.Background(), "other"); err != nil {
		t.Fatal(err)
	}
	if len(fake.released) != 1 || fake.released[0] != "other" {
		t.Fatalf("release = %v", fake.released)
	}
}

func TestAStepWhoseBindingDiffersFromItsOccupantFailsNamingTheAddressAndBothBindings(t *testing.T) {
	s := standingScore()
	fake := &occupantFake{run: func(context.Context, loop.Step) (loop.StepResult, error) {
		t.Error("mismatch reached occupant")
		return loop.StepResult{}, nil
	}}
	stepper := NewOccupantStepper(fake, s)
	for _, field := range []string{"provider", "profile", "model"} {
		chair := s.Chairs[0].Binding
		switch field {
		case "provider":
			chair.Provider = "other"
		case "profile":
			chair.Profile = "other"
		case "model":
			chair.Model = "other"
		}
		_, err := stepper.Run(context.Background(), loop.Step{Name: "plan", Chair: chair})
		for _, want := range []string{chair.Address, chair.String(), s.Chairs[0].Binding.String()} {
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Errorf("%s mismatch: %v lacks %q", field, err, want)
			}
		}
	}
	for _, address := range []string{"", "missing@score"} {
		chair := s.Chairs[0].Binding
		chair.Address = address
		if _, err := stepper.Run(context.Background(), loop.Step{Name: "plan", Chair: chair}); err == nil || !strings.Contains(err.Error(), "plan") {
			t.Errorf("unknown address: %v", err)
		}
	}
}

func TestOccupantRoutingIgnoresTierAndTimeoutAndPreservesTheOccupantOnFailure(t *testing.T) {
	s := standingScore()
	failure := errors.New("step failed")
	fake := &occupantFake{run: func(context.Context, loop.Step) (loop.StepResult, error) {
		return loop.StepResult{TerminalID: "unexpected"}, failure
	}}
	chair := s.Chairs[0].Binding
	chair.Tier, chair.Timeout = "large", time.Second
	out, err := NewOccupantStepper(fake, s).Run(context.Background(), loop.Step{Chair: chair})
	if !errors.Is(err, failure) || out.TerminalID != s.Chairs[0].TerminalID {
		t.Fatalf("result = %+v, %v", out, err)
	}
}

func TestScreenIsForwardedToTheInnerStepper(t *testing.T) {
	session, screen, err := NewOccupantStepper(&occupantFake{}, standingScore()).Screen(context.Background(), "occupant")
	if err != nil || session != "session" || screen != "screen occupant" {
		t.Fatalf("screen = %q %q %v", session, screen, err)
	}
}
