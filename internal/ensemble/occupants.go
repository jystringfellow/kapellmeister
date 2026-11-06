package ensemble

import (
	"context"
	"fmt"

	"github.com/jystringfellow/kapellmeister/internal/loop"
)

type OccupantStepper struct {
	inner  loop.Stepper
	chairs map[string]StandingChair
	slots  map[string]chan struct{}
}

func NewOccupantStepper(inner loop.Stepper, standing Standing) *OccupantStepper {
	s := &OccupantStepper{inner: inner, chairs: map[string]StandingChair{}, slots: map[string]chan struct{}{}}
	for _, c := range standing.Chairs {
		s.chairs[c.Address] = c
		if s.slots[c.TerminalID] == nil {
			s.slots[c.TerminalID] = make(chan struct{}, 1)
		}
	}
	return s
}

func (s *OccupantStepper) Run(ctx context.Context, step loop.Step) (loop.StepResult, error) {
	c, ok := s.chairs[step.Chair.Address]
	if !ok {
		return loop.StepResult{}, fmt.Errorf("step %s has no standing chair at address %q", step.Name, step.Chair.Address)
	}
	binding := c.Binding
	chair := step.Chair
	if chair.Provider != binding.Provider || chair.Profile != binding.Profile || chair.Model != binding.Model {
		return loop.StepResult{}, fmt.Errorf("chair %s is occupied by %s but step %s resolved to %s; km play does not swap occupants", c.Address, binding, step.Name, chair)
	}
	slot := s.slots[c.TerminalID]
	select {
	case slot <- struct{}{}:
		defer func() { <-slot }()
	case <-ctx.Done():
		return loop.StepResult{}, ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return loop.StepResult{}, err
	}
	step.ReuseTerminal = c.TerminalID
	out, err := s.inner.Run(ctx, step)
	out.TerminalID = c.TerminalID
	return out, err
}

func (s *OccupantStepper) Release(ctx context.Context, terminalID string) error {
	if _, ok := s.slots[terminalID]; ok {
		return nil
	}
	return s.inner.Release(ctx, terminalID)
}

func (s *OccupantStepper) Screen(ctx context.Context, terminalID string) (string, string, error) {
	if sc, ok := s.inner.(loop.Screener); ok {
		return sc.Screen(ctx, terminalID)
	}
	return "", "", fmt.Errorf("stepper cannot screen terminal %s", terminalID)
}
