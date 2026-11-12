package loop

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/jystringfellow/kapellmeister/internal/cao"
)

type CAOStepper struct {
	Session string
	Client  *cao.Client
}

func (c CAOStepper) Run(ctx context.Context, s Step) (StepResult, error) {
	if s.ReuseTerminal != "" {
		if err := c.Client.SetDescription(ctx, s.ReuseTerminal, s.Label); err != nil {
			return StepResult{TerminalID: s.ReuseTerminal}, err
		}
	}
	req := cao.StepRequest{
		Provider:         s.Chair.Provider,
		Agent:            s.Chair.Profile,
		Model:            s.Chair.Model,
		Prompt:           s.Prompt,
		WorkingDirectory: s.Dir,
		ReuseTerminalID:  s.ReuseTerminal,
		Description:      s.Label,
		Timeout:          s.Timeout.Seconds(),
	}
	if s.ReuseTerminal == "" {
		req.SessionName = c.Session
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
	out, err := (CAOWatcher{Client: c.Client}).Output(ctx, terminalID)
	return info.SessionName, out, err
}

func (c CAOStepper) Seat(ctx context.Context, name string, chair Chair, dir string) (Seat, error) {
	t, err := c.Client.CreateSession(ctx, name, cao.Launch{Provider: chair.Provider, Profile: chair.Profile, Model: chair.Model, WorkingDirectory: dir})
	var herr *cao.HTTPError
	if errors.As(err, &herr) && herr.TerminalID != "" {
		t.ID = herr.TerminalID
		if info, lookupErr := c.Client.Terminal(ctx, t.ID); lookupErr == nil {
			t = info
		}
	}
	seat := Seat{TerminalID: t.ID, Session: t.SessionName, Honored: t.ModelHonored}
	if t.Model != nil {
		seat.Model = *t.Model
	}
	return seat, err
}

func (c CAOStepper) Unseat(ctx context.Context, session string) error {
	err := c.Client.DeleteSession(ctx, session)
	var herr *cao.HTTPError
	if errors.As(err, &herr) && herr.Status == http.StatusNotFound {
		return nil
	}
	return err
}
