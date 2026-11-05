package ensemble

import (
	"context"
	"fmt"
	"time"

	"github.com/jystringfellow/kapellmeister/internal/cao"
	"github.com/jystringfellow/kapellmeister/internal/loop"
)

type Member struct {
	Section string     `json:"section"`
	Part    string     `json:"part"`
	Name    string     `json:"name"`
	Chair   loop.Chair `json:"chair"`
}

func (m Member) Address(ensemble string) string {
	return m.Section + "." + m.Name + "@" + ensemble
}

func MembersOf(c loop.Chairs) []Member {
	solo := func(part string, chair loop.Chair) Member {
		return Member{Section: part, Part: part, Name: part, Chair: chair}
	}
	members := []Member{solo("planner", c.Planner), solo("implementer", c.Implementer)}
	for i, r := range c.Reviewers {
		members = append(members, Member{Section: "reviewer", Part: "reviewer", Name: loop.ReviewerName(i, len(c.Reviewers)), Chair: r})
	}
	return append(members, solo("gate", c.Gate), solo("classifier", c.Classifier))
}

type Audition struct {
	Client   *cao.Client
	Ensemble string
	Worktree string
	Members  []Member
	Timeout  time.Duration
	KM       string
	Keep     bool
}

type ChairReport struct {
	Address    string `json:"address"`
	Chair      string `json:"chair"`
	TerminalID string `json:"terminal_id,omitempty"`
	OK         bool   `json:"ok"`
	Problem    string `json:"problem,omitempty"`
	BootMS     int64  `json:"boot_ms"`
	CueMS      int64  `json:"cue_ms,omitempty"`
	Model      string `json:"model,omitempty"`
	Honored    *bool  `json:"model_honored,omitempty"`
}

type Report struct {
	Ensemble string        `json:"ensemble"`
	Session  string        `json:"session"`
	Queue    string        `json:"queue"`
	OK       bool          `json:"ok"`
	Chairs   []ChairReport `json:"chairs"`
}

func (a Audition) Run(ctx context.Context) (Report, error) {
	crew := crew{Client: a.Client, Ensemble: a.Ensemble, Worktree: a.Worktree, Members: a.Members, Timeout: a.Timeout}
	report, err := crew.open(ctx, "audition")
	if err != nil {
		return report, err
	}
	crew.cueAll(ctx, &report, func(m Member) (string, string) {
		id := "audition-" + m.Section + "-" + m.Name
		return id, a.cueText(id, m, report.Queue)
	})
	if report.Session != "" && !a.Keep {
		if err := a.Client.DeleteSession(context.WithoutCancel(ctx), report.Session); err != nil {
			return report, fmt.Errorf("tearing down %s: %w", report.Session, err)
		}
	}
	report.OK = report.allOK()
	return report, nil
}

func (a Audition) cueText(id string, m Member, queue string) string {
	return fmt.Sprintf("Cue %s from km: an audition of ensemble %s. You are chair %s. This is a check, not work: do not read or change any files. Run this shell command exactly, then stop:\n%s cue done -queue %s %s",
		id, a.Ensemble, m.Address(a.Ensemble), shellQuote(a.KM), shellQuote(queue), id)
}

func (r Report) allOK() bool {
	for _, c := range r.Chairs {
		if !c.OK {
			return false
		}
	}
	return true
}
