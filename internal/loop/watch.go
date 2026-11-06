package loop

import (
	"context"
	"io"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/x/vt"
	"github.com/jystringfellow/kapellmeister/internal/cao"
)

type WatchedTerminal struct{ ID, Session, Status, Description string }

type Watcher interface {
	Terminals(context.Context) ([]WatchedTerminal, error)
	Output(context.Context, string) (string, error)
}

type CAOWatcher struct {
	Client  *cao.Client
	Session string
}

func (w CAOWatcher) Terminals(ctx context.Context) ([]WatchedTerminal, error) {
	terminals, err := w.Client.SessionTerminals(ctx, w.Session)
	if err != nil {
		return nil, err
	}
	out := make([]WatchedTerminal, 0, len(terminals))
	for _, t := range terminals {
		out = append(out, WatchedTerminal{ID: t.ID, Session: t.TmuxSession, Status: t.Status, Description: t.Description})
	}
	return out, nil
}

func (w CAOWatcher) Output(ctx context.Context, id string) (string, error) {
	raw, err := w.Client.Output(ctx, id)
	if err != nil || !strings.Contains(raw, "\x1b") {
		return raw, err
	}
	return renderScreen(raw), nil
}

func renderScreen(raw string) (screen string) {
	emulator := vt.NewEmulator(220, 50)
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		_, _ = io.Copy(io.Discard, emulator)
	}()
	defer func() {
		if replies, ok := emulator.InputPipe().(io.Closer); ok {
			_ = replies.Close()
			<-drained
		}
		if recover() != nil {
			screen = ""
		}
	}()
	_, _ = emulator.WriteString(raw)
	return emulator.String()
}

type blockedChair struct {
	terminal   WatchedTerminal
	step       Step
	name, line string
}

type watchState struct {
	sync.Mutex
	next    uint64
	running map[uint64]Step
	blocked map[string]blockedChair
}

func (e *Engine) startWatcher(ctx context.Context) func() {
	if e.Watcher == nil || e.Config.BlockerPoll <= 0 {
		return func() {}
	}
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(e.Config.BlockerPoll)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				pollCtx, stop := context.WithTimeout(ctx, 15*time.Second)
				e.pollBlockers(pollCtx)
				stop()
			}
		}
	}()
	return func() { cancel(); <-done }
}

func (e *Engine) watchStep(s Step) func() {
	if s.ChairName == "" {
		s.ChairName = firstSet(s.Chair.Address, stepRoles[s.Name])
		if s.ChairName == "" && strings.HasPrefix(s.Name, "classify-") {
			s.ChairName = "classifier"
		}
	}
	e.watch.Lock()
	if e.watch.running == nil {
		e.watch.running = map[uint64]Step{}
		e.watch.blocked = map[string]blockedChair{}
	}
	e.watch.next++
	id := e.watch.next
	e.watch.running[id] = s
	e.watch.Unlock()
	return func() {
		e.watch.Lock()
		defer e.watch.Unlock()
		delete(e.watch.running, id)
		e.watch.next++
		for _, other := range e.watch.running {
			if other.Label == s.Label {
				return
			}
		}
		for terminal, b := range e.watch.blocked {
			if b.terminal.Description == s.Label || len(e.watch.running) == 0 {
				e.reportBlocker("unblocked", b)
				delete(e.watch.blocked, terminal)
			}
		}
	}
}

func (e *Engine) pollBlockers(ctx context.Context) {
	e.watch.Lock()
	if len(e.watch.running) == 0 {
		e.watch.Unlock()
		return
	}
	generation := e.watch.next
	e.watch.Unlock()
	terminals, err := e.Watcher.Terminals(ctx)
	if err != nil {
		return
	}
	seen := map[string]bool{}
	for _, terminal := range terminals {
		e.watch.Lock()
		active := len(e.watch.running) > 0 && e.watch.next == generation
		e.watch.Unlock()
		if !active {
			return
		}
		seen[terminal.ID] = true
		line := ""
		if terminal.Status == "waiting_user_answer" || terminal.Status == "processing" {
			screen, err := e.Watcher.Output(ctx, terminal.ID)
			if err != nil {
				continue
			}
			line = FindBlocker(screen)
			if line == "" && terminal.Status == "waiting_user_answer" {
				line = "waiting for an answer"
			}
		}
		e.watch.Lock()
		if len(e.watch.running) == 0 || e.watch.next != generation {
			e.watch.Unlock()
			return
		}
		previous, blocked := e.watch.blocked[terminal.ID]
		if line == "" && blocked {
			e.reportBlocker("unblocked", previous)
			delete(e.watch.blocked, terminal.ID)
		}
		if line != "" && (!blocked || line != previous.line) {
			b := e.blockedChair(terminal, line)
			e.watch.blocked[terminal.ID] = b
			e.reportBlocker("blocked", b)
		}
		e.watch.Unlock()
	}
	e.watch.Lock()
	defer e.watch.Unlock()
	for id, b := range e.watch.blocked {
		if !seen[id] {
			e.reportBlocker("unblocked", b)
			delete(e.watch.blocked, id)
		}
	}
}

func (e *Engine) blockedChair(t WatchedTerminal, line string) blockedChair {
	b := blockedChair{terminal: t, line: line, name: t.Description, step: Step{Name: t.Description}}
	if b.name == "" {
		b.name = t.ID
		b.step.Name = t.ID
	}
	var ids []uint64
	for id, s := range e.watch.running {
		if s.Label == t.Description {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	if len(ids) > 0 {
		b.step = e.watch.running[ids[0]]
		var chairs []string
		for _, id := range ids {
			if name := e.watch.running[id].ChairName; name != "" {
				chairs = append(chairs, name)
			}
		}
		sort.Strings(chairs)
		b.step.ChairName = strings.Join(chairs, "/")
		b.name = strings.TrimSpace(label(b.step.Name, b.step.Round) + " " + b.step.ChairName)
	}
	return b
}

func (e *Engine) reportBlocker(event string, b blockedChair) {
	if event == "blocked" {
		e.progress("  blocked: %s — %s — attach with tmux attach -t %s", b.name, b.line, b.terminal.Session)
	} else {
		e.progress("  unblocked: %s", b.name)
	}
	e.record(Record{RunID: e.Config.RunID, Step: event, BlockedStep: b.step.Name, Round: b.step.Round, ChairName: b.step.ChairName, TerminalID: b.terminal.ID, Detail: b.line})
}
