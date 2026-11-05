package ensemble

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/jystringfellow/kapellmeister/internal/cao"
	"github.com/jystringfellow/kapellmeister/internal/loop"
)

type Member struct {
	Part  string     `json:"part"`
	Name  string     `json:"name"`
	Chair loop.Chair `json:"chair"`
}

func (m Member) Address(ensemble string) string {
	return m.Part + "." + m.Name + "@" + ensemble
}

func MembersOf(c loop.Chairs) []Member {
	members := []Member{{"planner", "planner", c.Planner}, {"implementer", "implementer", c.Implementer}}
	for i, r := range c.Reviewers {
		members = append(members, Member{"reviewer", loop.ReviewerName(i, len(c.Reviewers)), r})
	}
	return append(members, Member{"gate", "gate", c.Gate}, Member{"classifier", "classifier", c.Classifier})
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

const pollInterval = 200 * time.Millisecond

func (a Audition) Run(ctx context.Context) (Report, error) {
	runID := "audition-" + randomHex(4)
	if err := loop.PrepareRunDir(a.Worktree, runID); err != nil {
		return Report{}, err
	}
	report := Report{Ensemble: a.Ensemble, Queue: filepath.Join(a.Worktree, loop.RunDirRel(runID), "cues.jsonl"), Chairs: make([]ChairReport, len(a.Members))}
	for i, m := range a.Members {
		report.Chairs[i] = ChairReport{Address: m.Address(a.Ensemble), Chair: m.Chair.String()}
	}
	report.Session = a.boot(ctx, "km-"+a.Ensemble+"-"+randomHex(3), report.Chairs)
	var wg sync.WaitGroup
	for i := range a.Members {
		if report.Chairs[i].TerminalID == "" {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			a.cue(ctx, a.Members[i], report.Queue, &report.Chairs[i])
		}()
	}
	wg.Wait()
	if report.Session != "" && !a.Keep {
		if err := a.Client.DeleteSession(context.WithoutCancel(ctx), report.Session); err != nil {
			return report, fmt.Errorf("tearing down %s: %w", report.Session, err)
		}
	}
	report.OK = true
	for _, c := range report.Chairs {
		report.OK = report.OK && c.OK
	}
	return report, nil
}

func (a Audition) boot(ctx context.Context, name string, chairs []ChairReport) string {
	session := ""
	first := 0
	for ; first < len(a.Members) && session == ""; first++ {
		start := time.Now()
		t, err := a.Client.CreateSession(ctx, name, a.launch(a.Members[first]))
		a.booted(ctx, a.Members[first], t, err, time.Since(start), &chairs[first])
		if err == nil {
			session = t.SessionName
		}
	}
	var wg sync.WaitGroup
	for i := first; i < len(a.Members) && session != ""; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			start := time.Now()
			t, err := a.Client.AddTerminal(ctx, session, a.launch(a.Members[i]))
			a.booted(ctx, a.Members[i], t, err, time.Since(start), &chairs[i])
		}()
	}
	wg.Wait()
	return session
}

func (a Audition) launch(m Member) cao.Launch {
	return cao.Launch{Profile: m.Chair.Profile, Provider: m.Chair.Provider, Model: m.Chair.Model, WorkingDirectory: a.Worktree}
}

func (a Audition) booted(ctx context.Context, m Member, t cao.Terminal, err error, took time.Duration, r *ChairReport) {
	r.BootMS = took.Milliseconds()
	if err != nil {
		r.Problem = "boot: " + err.Error()
		return
	}
	r.TerminalID = t.ID
	if t.Model != nil {
		r.Model = *t.Model
	}
	r.Honored = t.ModelHonored
	if err := a.Client.SetGroup(ctx, t.ID, []string{a.Ensemble, m.Part}); err != nil {
		r.Problem = "group: " + err.Error()
		return
	}
	if err := a.Client.SetMetadata(ctx, t.ID, map[string]any{"chair": m.Address(a.Ensemble), "part": m.Part, "ensemble": a.Ensemble}); err != nil {
		r.Problem = "metadata: " + err.Error()
	}
}

func (a Audition) cue(ctx context.Context, m Member, queue string, r *ChairReport) {
	if r.Problem != "" {
		return
	}
	if blocker := a.blocker(ctx, r.TerminalID); blocker != "" {
		r.Problem = "blocked before the cue: " + blocker
		return
	}
	id := "audition-" + m.Part + "-" + m.Name
	start := time.Now()
	if err := a.Client.SendMessage(ctx, r.TerminalID, "km", a.cueText(id, m, queue)); err != nil {
		r.Problem = "cue: " + err.Error()
		return
	}
	deadline := start.Add(a.Timeout)
	for time.Now().Before(deadline) {
		closed, err := Closures(queue)
		if err != nil {
			r.Problem = "reading the cue queue: " + err.Error()
			return
		}
		if _, ok := closed[id]; ok {
			r.CueMS = time.Since(start).Milliseconds()
			r.OK = true
			return
		}
		select {
		case <-ctx.Done():
			r.Problem = ctx.Err().Error()
			return
		case <-time.After(pollInterval):
		}
	}
	r.Problem = fmt.Sprintf("cue not closed within %s", a.Timeout)
	if blocker := a.blocker(context.WithoutCancel(ctx), r.TerminalID); blocker != "" {
		r.Problem += ": " + blocker
	}
}

func (a Audition) cueText(id string, m Member, queue string) string {
	return fmt.Sprintf("Cue %s from km: an audition of ensemble %s. You are chair %s. This is a check, not work: do not read or change any files. Run this shell command exactly, then stop:\n%s cue done -queue %s %s",
		id, a.Ensemble, m.Address(a.Ensemble), shellQuote(a.KM), shellQuote(queue), id)
}

var ansi = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(\x07|\x1b\\)`)

func (a Audition) blocker(ctx context.Context, terminalID string) string {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	screen, err := a.Client.Output(ctx, terminalID)
	if err != nil {
		return ""
	}
	return loop.FindBlocker(ansi.ReplaceAllString(screen, "\n"))
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
