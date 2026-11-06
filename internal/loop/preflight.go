package loop

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

type PreflightResult struct {
	Chair      Chair  `json:"chair"`
	OK         bool   `json:"ok"`
	DurationMS int64  `json:"duration_ms"`
	TerminalID string `json:"terminal_id,omitempty"`
	Session    string `json:"session,omitempty"`
	Blocker    string `json:"blocker,omitempty"`
	Error      string `json:"error,omitempty"`
	Retried    bool   `json:"retried,omitempty"`
	Model      string `json:"model,omitempty"`
	Honored    *bool  `json:"model_honored,omitempty"`
}

var blockerRE = regexp.MustCompile(`(?i)requires confirmation|do you want to proceed|needs review|do you trust|trust (this|all|the)|sign in|log ?in|authenticat|approve|allow .* to`)

func UniqueChairs(chairs ...Chair) []Chair {
	seen := map[Chair]bool{}
	var out []Chair
	for _, s := range chairs {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func Preflight(ctx context.Context, stepper Stepper, worktree string, chairs []Chair, timeout time.Duration) ([]PreflightResult, error) {
	if err := PrepareRunDir(worktree, "preflight"); err != nil {
		return nil, err
	}
	results := make([]PreflightResult, len(chairs))
	var wg sync.WaitGroup
	for i, chair := range chairs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res := preflightChair(ctx, stepper, worktree, i, chair, timeout)
			if !res.OK && res.Blocker == "" && ctx.Err() == nil {
				if res.TerminalID != "" {
					_ = stepper.Release(context.WithoutCancel(ctx), res.TerminalID)
				}
				res = preflightChair(ctx, stepper, worktree, i, chair, timeout)
				res.Retried = true
			}
			results[i] = res
		}()
	}
	wg.Wait()
	return results, nil
}

func preflightChair(ctx context.Context, stepper Stepper, worktree string, i int, chair Chair, timeout time.Duration) PreflightResult {
	rel := filepath.Join(RunDirRel("preflight"), fmt.Sprintf("%d-%s.txt", i, chair.Provider))
	abs := filepath.Join(worktree, rel)
	_ = os.Remove(abs)
	res := PreflightResult{Chair: chair}
	start := time.Now()
	out, err := stepper.Run(ctx, Step{Name: "preflight", Label: TerminalLabel("preflight", chair.Provider), Chair: chair, Prompt: preflightPrompt(rel), Dir: worktree, ReplyPath: rel, Timeout: timeout})
	res.DurationMS = time.Since(start).Milliseconds()
	res.TerminalID = out.TerminalID
	res.Model, res.Honored = out.ReportedModel, out.ModelHonored
	if err == nil {
		awaitFile(ctx, abs, preflightReplyGrace)
		data, rerr := os.ReadFile(abs)
		if rerr == nil && strings.Contains(strings.ToLower(string(data)), "ready") {
			res.OK = true
			if out.TerminalID != "" {
				_ = stepper.Release(context.WithoutCancel(ctx), out.TerminalID)
			}
			res.TerminalID = ""
			return res
		}
		err = fmt.Errorf("%w at %s", errNoReply, rel)
	}
	res.Error = err.Error()
	if sc, ok := stepper.(Screener); ok && out.TerminalID != "" {
		if session, screen, serr := sc.Screen(context.WithoutCancel(ctx), out.TerminalID); serr == nil {
			res.Session = session
			res.Blocker = FindBlocker(screen)
		}
	}
	return res
}

const blockerLines = 12

var preflightReplyGrace = 30 * time.Second

func awaitFile(ctx context.Context, path string, grace time.Duration) {
	deadline := time.Now().Add(grace)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func FindBlocker(screen string) string {
	var lines []string
	for _, line := range strings.Split(screen, "\n") {
		if strings.TrimSpace(line) != "" {
			lines = append(lines, line)
		}
	}
	for _, line := range lines[max(0, len(lines)-blockerLines):] {
		if blockerRE.MatchString(line) {
			return strings.TrimSpace(line)
		}
	}
	return ""
}
