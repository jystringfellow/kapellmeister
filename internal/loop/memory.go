package loop

import (
	"context"
	"fmt"
)

type keptChair struct {
	terminal string
	chair    Chair
	tier     string
	steps    int
	usage    *Usage
	busy     bool
}

func (e *Engine) prepareMemory(ctx context.Context, s *Step) *keptChair {
	if s.MemoryKey == "" {
		return nil
	}
	part, _ := e.playingScore().Part(s.MemoryKey)
	e.mu.Lock()
	if e.kept == nil {
		e.kept = map[string]*keptChair{}
	}
	seat := e.kept[s.MemoryKey]
	s.Memory, s.MemoryReason = string(MemoryFresh), "first step"
	if seat != nil && seat.busy {
		s.MemoryReason = "terminal in use"
		s.KeepTerminal, s.ReuseTerminal = false, ""
		e.mu.Unlock()
		return nil
	}
	if seat == nil {
		seat = &keptChair{}
		e.kept[s.MemoryKey] = seat
		e.keptOrder = append(e.keptOrder, s.MemoryKey)
	}
	old := ""
	if seat.terminal != "" {
		reason := ""
		switch {
		case seat.tier != s.Assignment.Tier:
			reason = fmt.Sprintf("tier changed from %s to %s", seat.tier, s.Assignment.Tier)
		case seat.chair.Provider != s.Chair.Provider || seat.chair.Profile != s.Chair.Profile:
			reason = "agent changed"
		case seat.chair.Model != s.Chair.Model:
			reason = "model changed"
		case seat.chair != s.Chair:
			reason = "chair changed"
		case part.ResumeSteps > 0 && seat.steps >= part.ResumeSteps:
			reason = fmt.Sprintf("resume_steps limit reached: %d steps", seat.steps)
		case part.ResumeTokens > 0 && seat.usage != nil && seat.usage.Known && seat.usage.Tokens != nil && seat.usage.InputTokens > part.ResumeTokens:
			reason = fmt.Sprintf("resume_tokens limit exceeded: %d input tokens", seat.usage.InputTokens)
		}
		if reason != "" {
			old, seat.terminal = seat.terminal, ""
			s.MemoryReason = reason
		} else {
			s.Memory, s.MemoryReason = string(MemoryResume), ""
		}
	}
	s.KeepTerminal, s.ReuseTerminal = true, seat.terminal
	seat.busy = true
	e.mu.Unlock()
	if old != "" {
		_ = e.Stepper.Release(context.WithoutCancel(ctx), old)
	}
	return seat
}

func (e *Engine) finishMemory(ctx context.Context, s Step, seat *keptChair, out StepResult, err error) {
	e.mu.Lock()
	old := seat.terminal
	seat.busy = false
	if err == nil && out.TerminalID != "" {
		if old == out.TerminalID {
			seat.steps++
		} else {
			seat.steps = 1
		}
		seat.terminal, seat.chair, seat.tier, seat.usage = out.TerminalID, s.Chair, s.Assignment.Tier, out.usage
		e.mu.Unlock()
		if old != "" && old != out.TerminalID {
			_ = e.Stepper.Release(context.WithoutCancel(ctx), old)
		}
		return
	}
	seat.terminal = ""
	e.mu.Unlock()
	if old != "" {
		_ = e.Stepper.Release(context.WithoutCancel(ctx), old)
	}
	if out.TerminalID != "" && out.TerminalID != old {
		_ = e.Stepper.Release(context.WithoutCancel(ctx), out.TerminalID)
	}
}

func (e *Engine) releaseKept(ctx context.Context) {
	e.mu.Lock()
	var terminals []string
	for _, part := range e.keptOrder {
		if seat := e.kept[part]; seat != nil && seat.terminal != "" {
			terminals = append(terminals, seat.terminal)
			seat.terminal = ""
		}
	}
	e.mu.Unlock()
	for _, terminal := range terminals {
		_ = e.Stepper.Release(context.WithoutCancel(ctx), terminal)
	}
}
