package ensemble

import (
	"context"
	"os"
	"slices"
	"strings"
	"syscall"
	"testing"

	"github.com/jystringfellow/kapellmeister/internal/loop"
)

func TestReviewReseatsKeepEarlierChairsRecordedAndRouteToTheNewOccupants(t *testing.T) {
	for _, deleteFails := range []bool{false, true} {
		t.Run(map[bool]string{false: "released", true: "left behind"}[deleteFails], func(t *testing.T) {
			fake, a, _ := standingForReseat(t, nil, 2)
			if deleteFails {
				fake.deleteStatus = map[string]int{a.Standing.Chairs[2].TerminalID: 500}
			}
			var routed string
			inner := &occupantFake{run: func(_ context.Context, s loop.Step) (loop.StepResult, error) {
				routed = s.ReuseTerminal
				return loop.StepResult{}, nil
			}}
			stepper := NewOccupantStepper(inner, a.Standing)
			reseater := &ReviewReseater{Client: a.Client, Standing: a.Standing, Stepper: stepper, Timeout: a.Timeout, KM: a.KM, Task: a.Task, StateDir: a.StateDir}
			want := slices.Clone(a.Standing.Chairs)
			for _, index := range []int{2, 3} {
				chair := a.Standing.Chairs[index]
				result, err := reseater.Reseat(context.Background(), chair.Binding)
				if deleteFails && index == 2 {
					if err == nil || !strings.Contains(err.Error(), "left behind") {
						t.Fatalf("error = %v", err)
					}
				} else if err != nil {
					t.Fatal(err)
				}
				if result.New == "" || result.New == result.Old {
					t.Fatal(result)
				}
				want[index].TerminalID = result.New
				want[index].Chair = chair.Binding.String()
				records, err := ListStanding(a.StateDir)
				if err != nil || len(records) != 1 {
					t.Fatalf("records = %v, %v", records, err)
				}
				for i, c := range records[0].Chairs {
					if c != want[i] {
						t.Errorf("chair %d = %+v, want %+v", i, c, want[i])
					}
				}
				if _, err := stepper.Run(context.Background(), loop.Step{Name: "review", Chair: chair.Binding}); err != nil || routed != result.New {
					t.Fatalf("routed %s: %v", routed, err)
				}
				if err := stepper.Release(context.Background(), result.New); err != nil {
					t.Fatal(err)
				}
				if len(inner.released) != index-2 {
					t.Fatalf("released standing terminal: %v", inner.released)
				}
				if err := stepper.Release(context.Background(), result.Old); err != nil {
					t.Fatal(err)
				}
				if inner.released[len(inner.released)-1] != result.Old {
					t.Fatal(inner.released)
				}
			}
		})
	}
}

func TestAFailedReviewReseatKeepsRoutingToTheRecordedOccupant(t *testing.T) {
	fake, a, before := standingForReseat(t, nil, 2)
	fake.behaviours = map[string]behaviour{a.Chair.Profile: {bootFails: true}}
	var routed string
	inner := &occupantFake{run: func(_ context.Context, s loop.Step) (loop.StepResult, error) {
		routed = s.ReuseTerminal
		return loop.StepResult{}, nil
	}}
	stepper := NewOccupantStepper(inner, a.Standing)
	reseater := &ReviewReseater{Client: a.Client, Standing: a.Standing, Stepper: stepper, Timeout: a.Timeout, KM: a.KM, Task: a.Task, StateDir: a.StateDir}
	result, err := reseater.Reseat(context.Background(), a.Chair)
	if err == nil || result.New != "" {
		t.Fatalf("%+v, %v", result, err)
	}
	if string(readStandingBytes(t, a.StateDir, a.Standing.Session)) != string(before) {
		t.Fatal("record changed")
	}
	if _, err := stepper.Run(context.Background(), loop.Step{Name: "review", Chair: a.Chair}); err != nil || routed != result.Old {
		t.Fatalf("routed %s, %v", routed, err)
	}
}

func TestAnInterruptedReviewReseatNamesItsSignalAndPreservesCleanupDiagnostics(t *testing.T) {
	for _, signal := range []os.Signal{nil, syscall.SIGINT, syscall.SIGTERM} {
		name := "context canceled"
		if signal != nil {
			name = (&loop.SignalError{Signal: signal}).Error()
		}
		t.Run(name, func(t *testing.T) {
			fake, a, before := standingForReseat(t, nil, 2)
			fake.behaviours = map[string]behaviour{a.Chair.Profile: {ignores: true}}
			ctx, cancel := context.WithCancelCause(context.Background())
			defer cancel(nil)
			fake.onCue = func() {
				fake.deleteStatus = map[string]int{fake.order[len(fake.order)-1]: 500}
				if signal == nil {
					cancel(context.Canceled)
				} else {
					cancel(&loop.SignalError{Signal: signal})
				}
			}
			stepper := NewOccupantStepper(&occupantFake{}, a.Standing)
			reseater := &ReviewReseater{Client: a.Client, Standing: a.Standing, Stepper: stepper, Timeout: a.Timeout, KM: a.KM, Task: a.Task, StateDir: a.StateDir}
			result, err := reseater.Reseat(ctx, a.Chair)
			fake.settle()
			if err == nil || !strings.Contains(err.Error(), name) || !strings.Contains(err.Error(), a.Address) || !strings.Contains(err.Error(), "deleting new terminal") || !strings.Contains(err.Error(), "HTTP 500") {
				t.Fatalf("reseat = %+v, error = %v", result, err)
			}
			if result.New != "" || string(readStandingBytes(t, a.StateDir, a.Standing.Session)) != string(before) {
				t.Fatal("interrupted reseat changed the record")
			}
			requireNoSessionDeleted(t, fake)
		})
	}
}

func TestOnlyProducePartsPlayedByAllChairsCheckTheirNativeDefinitions(t *testing.T) {
	s := Standing{Session: "session", Worktree: t.TempDir(), Chairs: []StandingChair{{Address: "section.chair@ensemble", Part: "writer", Binding: loop.Chair{NativeAgent: "writer"}}}}
	score := loop.Score{Steps: []loop.ScoreStep{{Part: "writer", Kind: loop.KindProduce}}}
	if err := s.CheckReseated(score); err != nil {
		t.Fatal(err)
	}
	score.Steps[0].Chairs = loop.ChairsAll
	if err := s.CheckReseated(score); err == nil || !strings.Contains(err.Error(), "section.chair@ensemble") {
		t.Fatal(err)
	}
}
