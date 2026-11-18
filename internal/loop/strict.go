package loop

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	StrictFailed = "strict_failed"
	StrictNotRun = "strict_not_run"
)

type StrictResult struct {
	Status     string `json:"status"`
	Command    string `json:"command"`
	Ready      string `json:"ready,omitempty"`
	ExitCode   int    `json:"exit_code"`
	DurationMS int64  `json:"duration_ms"`
	Reply      string `json:"reply,omitempty"`
}

func (e *Engine) strict(ctx context.Context, res Result) Result {
	cfg := e.Config
	finish := e.beginStep(res.Rounds, ActiveStep{Step: "strict"})
	defer finish()
	reply := filepath.Join(RunDirRel(cfg.RunID), "strict.txt")
	strict := &StrictResult{Command: cfg.StrictCommand, Ready: cfg.StrictReadyCommand, Reply: reply}
	start := time.Now()
	settle := func(step string, v VerifyResult, err error) error {
		strict.ExitCode, strict.DurationMS = v.ExitCode, time.Since(start).Milliseconds()
		rec := Record{RunID: cfg.RunID, Round: res.Rounds, Step: step, DurationMS: strict.DurationMS, ExitCode: &strict.ExitCode, Reply: reply}
		if err != nil {
			rec.Error = cancellationReason(ctx, err)
		}
		e.record(rec)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(cfg.Worktree, reply), []byte(tail(v.Output, 400)), 0o644)
	}
	if cfg.StrictReadyCommand != "" {
		e.progress("→ strict-ready %s", cfg.StrictReadyCommand)
		v, err := CommandVerifier(cfg.StrictReadyCommand)(ctx, cfg.Worktree)
		if err := settle("strict-ready", v, err); err != nil {
			return e.fail(res, err)
		}
		if v.ExitCode != 0 {
			strict.Status = "not_run"
			res.Strict, res.Outcome = strict, StrictNotRun
			res.Reason = fmt.Sprintf("change approved by the gate, but strict not run: %s exited %d: %s; the approved change is left in place and strict still has to pass", cfg.StrictReadyCommand, v.ExitCode, strings.TrimSpace(tail(v.Output, 40)))
			e.progress("  strict not run: %s exited %d\n%s", cfg.StrictReadyCommand, v.ExitCode, strings.TrimRight(tail(v.Output, 40), "\n"))
			return res
		}
		start = time.Now()
	}
	e.progress("→ strict %s", cfg.StrictCommand)
	v, err := CommandVerifier(cfg.StrictCommand)(ctx, cfg.Worktree)
	if err := settle("strict", v, err); err != nil {
		return e.fail(res, err)
	}
	res.Strict = strict
	if v.ExitCode != 0 {
		strict.Status = "failed"
		res.Outcome = StrictFailed
		res.Reason = fmt.Sprintf("change approved by the gate, but strict failed (exit %d); see %s; the approved change is left in place", v.ExitCode, reply)
		e.progress("  strict failed (exit %d) in %s; see %s", v.ExitCode, secs(strict.DurationMS), reply)
		return res
	}
	strict.Status = "passed"
	e.progress("  strict passed in %s", secs(strict.DurationMS))
	return res
}
