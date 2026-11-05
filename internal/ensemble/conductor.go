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

type conductor struct {
	Client   *cao.Client
	Ensemble string
	Worktree string
	Members  []Member
	Timeout  time.Duration
}

const pollInterval = 200 * time.Millisecond

func (c conductor) seat(ctx context.Context, prefix string) (Report, error) {
	runID := prefix + "-" + randomHex(4)
	if err := loop.PrepareRunDir(c.Worktree, runID); err != nil {
		return Report{}, err
	}
	report := Report{Ensemble: c.Ensemble, Queue: filepath.Join(c.Worktree, loop.RunDirRel(runID), "cues.jsonl"), Chairs: make([]ChairReport, len(c.Members))}
	for i, m := range c.Members {
		report.Chairs[i] = ChairReport{Address: m.Address(c.Ensemble), Chair: m.Chair.String()}
	}
	report.Session = c.boot(ctx, "km-"+c.Ensemble+"-"+randomHex(3), report.Chairs)
	return report, nil
}

func (c conductor) cueAll(ctx context.Context, report *Report, cue func(Member) (string, string)) {
	var wg sync.WaitGroup
	for i := range c.Members {
		if report.Chairs[i].TerminalID == "" {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			id, text := cue(c.Members[i])
			c.cueAndWait(ctx, id, text, report.Queue, &report.Chairs[i])
		}()
	}
	wg.Wait()
}

func (c conductor) boot(ctx context.Context, name string, chairs []ChairReport) string {
	session := ""
	first := 0
	for ; first < len(c.Members) && session == ""; first++ {
		start := time.Now()
		t, err := c.Client.CreateSession(ctx, name, c.launch(c.Members[first]))
		c.booted(ctx, c.Members[first], t, err, time.Since(start), &chairs[first])
		if err == nil {
			session = t.SessionName
		}
	}
	var wg sync.WaitGroup
	for i := first; i < len(c.Members) && session != ""; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			start := time.Now()
			t, err := c.Client.AddTerminal(ctx, session, c.launch(c.Members[i]))
			c.booted(ctx, c.Members[i], t, err, time.Since(start), &chairs[i])
		}()
	}
	wg.Wait()
	return session
}

func (c conductor) launch(m Member) cao.Launch {
	return cao.Launch{Profile: m.Chair.Profile, Provider: m.Chair.Provider, Model: m.Chair.Model, WorkingDirectory: c.Worktree}
}

func (c conductor) booted(ctx context.Context, m Member, t cao.Terminal, err error, took time.Duration, r *ChairReport) {
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
	if err := c.Client.SetGroup(ctx, t.ID, []string{c.Ensemble, m.Section}); err != nil {
		r.Problem = "group: " + err.Error()
		return
	}
	if err := c.Client.SetMetadata(ctx, t.ID, map[string]any{"chair": m.Address(c.Ensemble), "section": m.Section, "part": m.Part, "ensemble": c.Ensemble}); err != nil {
		r.Problem = "metadata: " + err.Error()
	}
}

func (c conductor) cueAndWait(ctx context.Context, id, text, queue string, r *ChairReport) {
	if r.Problem != "" {
		return
	}
	if blocker := c.blocker(ctx, r.TerminalID); blocker != "" {
		r.Problem = "blocked before the cue: " + blocker
		return
	}
	start := time.Now()
	if err := c.Client.SendMessage(ctx, r.TerminalID, "km", text); err != nil {
		r.Problem = "cue: " + err.Error()
		return
	}
	deadline := start.Add(c.Timeout)
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
	r.Problem = fmt.Sprintf("cue not closed within %s", c.Timeout)
	if blocker := c.blocker(context.WithoutCancel(ctx), r.TerminalID); blocker != "" {
		r.Problem += ": " + blocker
	}
}

var ansi = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(\x07|\x1b\\)`)

func (c conductor) blocker(ctx context.Context, terminalID string) string {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	screen, err := c.Client.Output(ctx, terminalID)
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
