package ensemble

import (
	"context"
	"fmt"
	"sync"

	"github.com/jystringfellow/kapellmeister/internal/loop"
)

type OccupantStepper struct {
	mu     sync.RWMutex
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

func (s *OccupantStepper) Seat(address, terminalID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	chair := s.chairs[address]
	old := chair.TerminalID
	chair.TerminalID = terminalID
	s.chairs[address] = chair
	if s.slots[terminalID] == nil {
		s.slots[terminalID] = make(chan struct{}, 1)
	}
	for _, other := range s.chairs {
		if other.TerminalID == old {
			return
		}
	}
	delete(s.slots, old)
}

func (s *OccupantStepper) Run(ctx context.Context, step loop.Step) (loop.StepResult, error) {
	s.mu.RLock()
	c, ok := s.chairs[step.Chair.Address]
	slot := s.slots[c.TerminalID]
	s.mu.RUnlock()
	if !ok {
		return loop.StepResult{}, fmt.Errorf("step %s has no standing chair at address %q", step.Name, step.Chair.Address)
	}
	binding := c.Binding
	chair := step.Chair
	if chair.Provider != binding.Provider || chair.Profile != binding.Profile || chair.Model != binding.Model {
		return loop.StepResult{}, fmt.Errorf("chair %s is occupied by %s but step %s resolved to %s; km play does not swap occupants", c.Address, binding, step.Name, chair)
	}
	if step.FreshTerminal {
		return s.inner.Run(ctx, step)
	}
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
	s.mu.RLock()
	_, ok := s.slots[terminalID]
	s.mu.RUnlock()
	if ok {
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
