package ensemble

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jystringfellow/kapellmeister/internal/cao"
)

type Rehearsal struct {
	Client   *cao.Client
	Ensemble string
	Worktree string
	Members  []Member
	Timeout  time.Duration
	KM       string
	Task     string
	TaskFile string
	StateDir string
}

func (a Rehearsal) Run(ctx context.Context) (Report, error) {
	crew := crew{Client: a.Client, Ensemble: a.Ensemble, Worktree: a.Worktree, Members: a.Members, Timeout: a.Timeout}
	report, err := crew.open(ctx, "rehearse")
	if err != nil {
		return report, err
	}
	if report.Session != "" {
		standing := Standing{Ensemble: a.Ensemble, Session: report.Session, Worktree: a.Worktree, Queue: report.Queue, TaskFile: a.TaskFile}
		if a.TaskFile == "" {
			standing.Task = a.Task
		}
		for i, c := range report.Chairs {
			if c.TerminalID == "" {
				continue
			}
			m := a.Members[i]
			standing.Chairs = append(standing.Chairs, StandingChair{Address: c.Address, Section: m.Section, Part: m.Part, Chair: c.Chair, TerminalID: c.TerminalID})
		}
		if err := standing.Save(a.StateDir); err != nil {
			return report, fmt.Errorf("recording standing ensemble: session %s is still running: %w", report.Session, err)
		}
	}
	crew.cueAll(ctx, &report, func(m Member) (string, string) {
		id := "rehearse-" + m.Section + "-" + m.Name
		return id, a.briefing(id, m, report.Queue)
	})
	report.OK = report.allOK()
	return report, nil
}

func (a Rehearsal) briefing(id string, m Member, queue string) string {
	var mates []string
	for _, other := range a.Members {
		if other.Section == m.Section && other.Name != m.Name {
			mates = append(mates, other.Address(a.Ensemble))
		}
	}
	sectionMates := "none"
	if len(mates) > 0 {
		sectionMates = strings.Join(mates, ", ")
	}
	return fmt.Sprintf("Cue %s from km: a rehearsal of ensemble %s. You are chair %s (part %s).\nSection-mates: %s\nTask:\n%s\nThis is a briefing: read and understand the task, but do not change any files yet. When you have, run this shell command exactly, then stop:\n%s cue done -queue %s %s", id, a.Ensemble, m.Address(a.Ensemble), m.Part, sectionMates, a.Task, shellQuote(a.KM), shellQuote(queue), id)
}
