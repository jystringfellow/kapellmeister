package ensemble

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/jystringfellow/kapellmeister/internal/cao"
	"github.com/jystringfellow/kapellmeister/internal/loop"
)

type ReviewReseater struct {
	Client   *cao.Client
	Standing Standing
	Stepper  *OccupantStepper
	Timeout  time.Duration
	KM       string
	Task     string
	StateDir string
	mu       sync.Mutex
}

func (r *ReviewReseater) Reseat(ctx context.Context, chair loop.Chair) (loop.Reseated, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	index, err := r.Standing.ChairIndex(chair.Address)
	if err != nil {
		return loop.Reseated{}, err
	}
	binding := r.Standing.Chairs[index].Binding
	result, err := (Reseat{Client: r.Client, Standing: r.Standing, Address: chair.Address, Chair: binding, Timeout: r.Timeout, KM: r.KM, Task: r.Task, StateDir: r.StateDir}).Run(ctx)
	if result.New != "" {
		r.Standing.Chairs = slices.Clone(r.Standing.Chairs)
		c := &r.Standing.Chairs[index]
		binding.Address = chair.Address
		c.TerminalID, c.Binding, c.Chair, c.Briefed = result.New, binding, binding.String(), true
		r.Stepper.Seat(chair.Address, result.New)
	}
	if cause := loop.SignalOf(ctx); err != nil && cause != nil {
		err = fmt.Errorf("%s: %w", cause, err)
	}
	return loop.Reseated{Old: result.Old, New: result.New}, err
}
