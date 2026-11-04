package loop

import (
	"context"
	"errors"
	"os/exec"
	"time"

	"github.com/jystringfellow/oko/internal/cao"
)

type CAOStepper struct {
	Client *cao.Client
}

func (c CAOStepper) Run(ctx context.Context, s Step) (StepResult, error) {
	req := cao.StepRequest{
		Provider:         s.Seat.Provider,
		Agent:            s.Seat.Profile,
		Model:            s.Seat.Model,
		Prompt:           s.Prompt,
		WorkingDirectory: s.Dir,
		ReuseTerminalID:  s.ReuseTerminal,
		Description:      s.Label,
		Timeout:          s.Timeout.Seconds(),
	}
	resp, err := c.Client.RunStep(ctx, req)
	if err != nil {
		var herr *cao.HTTPError
		if errors.As(err, &herr) && herr.TerminalID != "" {
			return StepResult{TerminalID: herr.TerminalID}, err
		}
		return StepResult{}, err
	}
	out := StepResult{TerminalID: resp.TerminalID, Status: resp.Status, LastMessage: resp.LastMessage}
	infoCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	if info, err := c.Client.Terminal(infoCtx, resp.TerminalID); err == nil {
		if info.Model != nil {
			out.ReportedModel = *info.Model
		}
		out.ModelHonored = info.ModelHonored
	}
	return out, nil
}

func (c CAOStepper) Release(ctx context.Context, terminalID string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return c.Client.DeleteTerminal(ctx, terminalID)
}

func (c CAOStepper) Screen(ctx context.Context, terminalID string) (string, string, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	info, err := c.Client.Terminal(ctx, terminalID)
	if err != nil {
		return "", "", err
	}
	out, err := exec.CommandContext(ctx, "tmux", "capture-pane", "-p", "-t", info.SessionName).Output()
	return info.SessionName, string(out), err
}
