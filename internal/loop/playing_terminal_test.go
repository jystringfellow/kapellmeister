package loop

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"
)

func holdPlayingSteps(t *testing.T, e *Engine, f *fakeStepper, steps ...Step) func() {
	t.Helper()
	entered := make(chan struct{}, len(steps))
	release := make(chan struct{})
	results := make(chan error, len(steps))
	for _, s := range steps {
		f.actions[s.Name] = func(s Step) {
			entered <- struct{}{}
			<-release
			f.write(s.ReplyPath, "reply\n")
		}
	}
	var once sync.Once
	finish := func() {
		once.Do(func() {
			close(release)
			for range steps {
				if err := <-results; err != nil {
					t.Error(err)
				}
			}
		})
	}
	t.Cleanup(finish)
	for _, s := range steps {
		go func() {
			_, err := e.attempt(context.Background(), s)
			results <- err
		}()
	}
	for range steps {
		select {
		case <-entered:
		case <-time.After(2 * time.Second):
			t.Fatal("chair did not start playing")
		}
	}
	return finish
}

func TestAFreshChairsTerminalIsRecordedWhilePlayingItsStep(t *testing.T) {
	e, f, _ := newEngine(t, nil)
	w := &fakeWatcher{terminals: []WatchedTerminal{{ID: "fresh", Description: "run · planner", Status: "processing"}}}
	e.Watcher, e.Config.BlockerPoll = w, time.Millisecond
	updates := make(chan []ActiveStep, 16)
	e.Playing = func(_ int, active []ActiveStep) { updates <- active }
	finish := holdPlayingSteps(t, e, f, Step{Name: "plan", ChairName: "planner", Label: "run · planner", ReplyPath: "reply.md"})
	stop := e.startWatcher(context.Background())
	defer stop()
	timeout := time.After(2 * time.Second)
	for {
		select {
		case active := <-updates:
			if len(active) != 1 || active[0].Terminal == "" {
				continue
			}
			if active[0].Terminal != "fresh" || f.inFlight() != 1 {
				t.Fatalf("playing = %+v, steps playing = %d", active, f.inFlight())
			}
			data, err := json.Marshal(active)
			if err != nil {
				t.Fatal(err)
			}
			var recorded []map[string]any
			if err := json.Unmarshal(data, &recorded); err != nil || recorded[0]["terminal"] != "fresh" {
				t.Fatalf("recorded playing = %s, error = %v", data, err)
			}
			finish()
			if active = <-updates; len(active) != 0 {
				t.Fatalf("playing after step = %+v", active)
			}
			return
		case <-timeout:
			t.Fatal("fresh chair's terminal was not recorded while playing")
		}
	}
}

func TestTwoReviewersPlayingAtOnceEachGetTheirOwnTerminal(t *testing.T) {
	e, f, _ := newEngine(t, nil)
	w := &fakeWatcher{terminals: []WatchedTerminal{
		{ID: "second", Description: "run · reviewer-2", Status: "processing"},
		{ID: "podium", Description: "podium"},
		{ID: "first", Description: "run · reviewer-1", Status: "idle"},
	}}
	e.Watcher = w
	var active []ActiveStep
	notifications := 0
	e.Playing = func(_ int, steps []ActiveStep) { active = steps; notifications++ }
	finish := holdPlayingSteps(t, e, f,
		Step{Name: "review", Round: 1, ChairName: "reviewer-1", Label: "run · reviewer-1", ReplyPath: "review-1.md"},
		Step{Name: "review", Round: 1, ChairName: "reviewer-2", Label: "run · reviewer-2", ReplyPath: "review-2.md"},
	)
	e.pollBlockers(context.Background())
	want := map[string]string{"reviewer-1": "first", "reviewer-2": "second"}
	for _, s := range active {
		if s.Terminal != want[s.ChairName] {
			t.Errorf("%s terminal = %q, want %q", s.ChairName, s.Terminal, want[s.ChairName])
		}
	}
	if w.calls != 1 || notifications != 3 {
		t.Errorf("listings = %d, playing notifications = %d", w.calls, notifications)
	}
	e.pollBlockers(context.Background())
	if notifications != 3 {
		t.Errorf("unchanged terminals sent another playing notification: %d", notifications)
	}
	finish()
}

func TestTheTerminalIsNotRecordedWhenTwoStepsOrTwoTerminalsShareTheLabel(t *testing.T) {
	for _, tc := range []struct {
		name      string
		labels    []string
		terminals []WatchedTerminal
	}{
		{"two steps", []string{"classifier", "classifier"}, []WatchedTerminal{{ID: "first", Description: "classifier"}}},
		{"two terminals", []string{"planner"}, []WatchedTerminal{{ID: "first", Description: "planner"}, {ID: "second", Description: "planner"}}},
		{"empty label", []string{""}, []WatchedTerminal{{ID: "first"}}},
		{"unmatched label", []string{"planner"}, []WatchedTerminal{{ID: "first", Description: "podium"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, f, _ := newEngine(t, nil)
			e.Watcher = &fakeWatcher{terminals: tc.terminals}
			var active []ActiveStep
			e.Playing = func(_ int, steps []ActiveStep) { active = steps }
			var steps []Step
			for i, label := range tc.labels {
				name := ReviewerName(i, len(tc.labels))
				steps = append(steps, Step{Name: "classify-review", ChairName: name, Label: label, ReplyPath: name + ".md"})
			}
			finish := holdPlayingSteps(t, e, f, steps...)
			e.pollBlockers(context.Background())
			for _, s := range active {
				if s.Terminal != "" {
					t.Errorf("ambiguous terminal recorded: %+v", s)
				}
			}
			finish()
		})
	}
}

type playingTerminalWatcher struct {
	*fakeWatcher
	list func(context.Context) ([]WatchedTerminal, error)
}

func (w playingTerminalWatcher) Terminals(ctx context.Context) ([]WatchedTerminal, error) {
	return w.list(ctx)
}

func TestAListingTakenBeforeTheStepBeganIsNotApplied(t *testing.T) {
	e, f, _ := newEngine(t, nil)
	var active []ActiveStep
	e.Playing = func(_ int, steps []ActiveStep) { active = steps }
	finish := holdPlayingSteps(t, e, f, Step{Name: "plan", Label: "planner", ReplyPath: "first.md"})
	var finishNext func()
	e.Watcher = playingTerminalWatcher{fakeWatcher: &fakeWatcher{}, list: func(context.Context) ([]WatchedTerminal, error) {
		finish()
		finishNext = holdPlayingSteps(t, e, f, Step{Name: "plan", Label: "planner", ReplyPath: "second.md"})
		return []WatchedTerminal{{ID: "previous", Description: "planner"}}, nil
	}}
	e.pollBlockers(context.Background())
	if len(active) != 1 || active[0].Terminal != "" {
		t.Errorf("stale listing applied: %+v", active)
	}
	finishNext()
}

func TestAReusedTerminalKeepsItsRecordedIdWhenAnotherIsListed(t *testing.T) {
	e, f, _ := newEngine(t, nil)
	e.Watcher = &fakeWatcher{terminals: []WatchedTerminal{{ID: "other", Description: "implementer"}}}
	var active []ActiveStep
	e.Playing = func(_ int, steps []ActiveStep) { active = steps }
	finish := holdPlayingSteps(t, e, f, Step{Name: "implement", Label: "implementer", ReuseTerminal: "kept", ReplyPath: "reply.md"})
	e.pollBlockers(context.Background())
	if len(active) != 1 || active[0].Terminal != "kept" {
		t.Errorf("playing = %+v", active)
	}
	finish()
}

func TestWithTheWatcherOffTheTerminalStaysEmptyWhilePlaying(t *testing.T) {
	for _, watcher := range []bool{false, true} {
		t.Run(map[bool]string{false: "no watcher", true: "zero poll"}[watcher], func(t *testing.T) {
			e, f, _ := newEngine(t, nil)
			w := &fakeWatcher{terminals: []WatchedTerminal{{ID: "fresh", Description: "planner"}}}
			e.Config.BlockerPoll = time.Millisecond
			if watcher {
				e.Watcher, e.Config.BlockerPoll = w, 0
			}
			var active []ActiveStep
			e.Playing = func(_ int, steps []ActiveStep) { active = steps }
			stop := e.startWatcher(context.Background())
			defer stop()
			finish := holdPlayingSteps(t, e, f, Step{Name: "plan", Label: "planner", ReplyPath: "reply.md"})
			if len(active) != 1 || active[0].Terminal != "" || w.calls != 0 {
				t.Errorf("playing = %+v, listings = %d", active, w.calls)
			}
			finish()
		})
	}
}

func TestAFreshChairNeverReceivesAFinishedStepsLingeringTerminal(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(map[bool]string{false: "completed step", true: "failed step"}[failed], func(t *testing.T) {
			e, f, _ := newEngine(t, nil)
			if failed {
				f.errFor = func(Step) error { return context.Canceled }
			}
			previous, err := e.step(context.Background(), Step{Name: "classify-review", ReplyPath: "review.md"})
			if (err != nil) != failed || previous.TerminalID == "" {
				t.Fatalf("previous step = %+v, error = %v", previous, err)
			}
			if len(f.released) != 1 || f.released[0] != previous.TerminalID {
				t.Fatalf("released terminals = %v", f.released)
			}
			f.errFor = nil
			label := TerminalLabel(e.Config.RunID, "classify-gate")
			w := &fakeWatcher{terminals: []WatchedTerminal{{ID: previous.TerminalID, Description: label, Status: "idle"}}}
			e.Watcher = w
			var active []ActiveStep
			e.Playing = func(_ int, steps []ActiveStep) { active = steps }
			finish := holdPlayingSteps(t, e, f, Step{Name: "classify-gate", Label: label, ReplyPath: "gate.md"})
			e.pollBlockers(context.Background())
			if len(active) != 1 || active[0].Terminal != "" {
				t.Errorf("finished step's terminal assigned to fresh chair: %+v", active)
			}
			w.terminals = append(w.terminals, WatchedTerminal{ID: "next", Description: label, Status: "processing"})
			e.pollBlockers(context.Background())
			if len(active) != 1 || active[0].Terminal != "next" {
				t.Errorf("fresh chair's terminal not recorded within one poll: %+v", active)
			}
			if w.calls != 2 {
				t.Errorf("session listings = %d, want 2", w.calls)
			}
			finish()
		})
	}
}

func TestAResumedChairKeepsATerminalReturnedByAnEarlierStep(t *testing.T) {
	e, f, _ := newEngine(t, nil)
	previous, err := e.step(context.Background(), Step{Name: "implement", KeepTerminal: true, ReplyPath: "first.md"})
	if err != nil || previous.TerminalID == "" || len(f.released) != 0 {
		t.Fatalf("previous step = %+v, error = %v, released = %v", previous, err, f.released)
	}
	label := TerminalLabel(e.Config.RunID, "implement")
	e.Watcher = &fakeWatcher{terminals: []WatchedTerminal{{ID: previous.TerminalID, Description: label}}}
	var active []ActiveStep
	e.Playing = func(_ int, steps []ActiveStep) { active = steps }
	finish := holdPlayingSteps(t, e, f, Step{Name: "implement", Round: 2, Label: label, ReuseTerminal: previous.TerminalID, ReplyPath: "second.md"})
	e.pollBlockers(context.Background())
	if len(active) != 1 || active[0].Terminal != previous.TerminalID {
		t.Errorf("resumed chair's terminal = %+v, want %q", active, previous.TerminalID)
	}
	finish()
}
