package ensemble

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/jystringfellow/kapellmeister/internal/cao"
	"github.com/jystringfellow/kapellmeister/internal/loop"
)

type Reseat struct {
	Client   *cao.Client
	Standing Standing
	Address  string
	Chair    loop.Chair
	Timeout  time.Duration
	KM       string
	Task     string
	StateDir string
}

type Reseated struct {
	Address string
	Old     string
	New     string
}

func (s Standing) ChairIndex(address string) (int, error) {
	var addresses []string
	for i, c := range s.Chairs {
		if c.Address == address {
			return i, nil
		}
		addresses = append(addresses, c.Address)
	}
	return -1, fmt.Errorf("unknown chair %q in session %s (recorded: %s)", address, s.Session, strings.Join(addresses, ", "))
}

func (a Reseat) Run(ctx context.Context) (Reseated, error) {
	index, err := a.Standing.ChairIndex(a.Address)
	if err != nil {
		return Reseated{}, err
	}
	result := Reseated{Address: a.Address, Old: a.Standing.Chairs[index].TerminalID}
	runID := "reseat-" + randomHex(4)
	if err := loop.PrepareRunDir(a.Standing.Worktree, runID); err != nil {
		return result, fmt.Errorf("chair %s: %w", a.Address, err)
	}
	queue := filepath.Join(a.Standing.Worktree, loop.RunDirRel(runID), "cues.jsonl")
	members := a.Standing.Members()
	m := members[index]
	m.Chair = a.Chair
	m.Chair.Address = a.Address
	c := conductor{Client: a.Client, Ensemble: a.Standing.Ensemble, Worktree: a.Standing.Worktree, Timeout: a.Timeout}
	report := ChairReport{Address: a.Address, Chair: m.Chair.String()}
	c.bootInto(ctx, a.Standing.Session, m, &report)
	if report.TerminalID != "" {
		id := "reseat-" + m.Section + "-" + m.Name
		c.cueAndWait(ctx, id, briefing(a.Standing.Ensemble, a.Task, a.KM, id, m, members, queue), queue, &report)
	}
	if err := ctx.Err(); err != nil {
		report.OK = false
		report.Problem = err.Error()
		if report.TerminalID != "" {
			if blocker := c.blocker(context.WithoutCancel(ctx), report.TerminalID); blocker != "" {
				report.Problem += ": " + blocker
			}
		}
	}
	if !report.OK {
		err := fmt.Errorf("chair %s: %s", a.Address, report.Problem)
		if report.TerminalID != "" {
			if cleanupErr := deleteOccupant(context.WithoutCancel(ctx), a.Client, report.TerminalID); cleanupErr != nil {
				err = fmt.Errorf("%w; deleting new terminal %s: %v", err, report.TerminalID, cleanupErr)
			}
		}
		return result, err
	}
	result.New = report.TerminalID
	var deleteErr error
	if result.Old != "" {
		deleteErr = deleteOccupant(context.WithoutCancel(ctx), a.Client, result.Old)
	}
	standing := a.Standing
	standing.Chairs = slices.Clone(standing.Chairs)
	chair := &standing.Chairs[index]
	chair.TerminalID, chair.Binding, chair.Chair, chair.Briefed = result.New, m.Chair, m.Chair.String(), true
	if err := standing.Save(a.StateDir); err != nil {
		return result, fmt.Errorf("chair %s: recording new occupant %s: %w", a.Address, result.New, err)
	}
	if deleteErr != nil {
		return result, fmt.Errorf("chair %s: new occupant %s recorded; old terminal %s left behind: %w", a.Address, result.New, result.Old, deleteErr)
	}
	return result, nil
}

func deleteOccupant(ctx context.Context, client *cao.Client, terminal string) error {
	err := client.DeleteTerminal(ctx, terminal)
	if err == nil || notFound(err) {
		return nil
	}
	if _, lookupErr := client.Terminal(ctx, terminal); notFound(lookupErr) {
		return nil
	}
	return err
}

func notFound(err error) bool {
	var httpErr *cao.HTTPError
	return errors.As(err, &httpErr) && httpErr.Status == http.StatusNotFound
}
