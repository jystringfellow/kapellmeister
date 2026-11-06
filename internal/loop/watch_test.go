package loop

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeWatcher struct {
	mu        sync.Mutex
	terminals []WatchedTerminal
	screens   map[string]string
	calls     int
	reads     []string
	polled    chan struct{}
}

func (f *fakeWatcher) Terminals(ctx context.Context) ([]WatchedTerminal, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.polled != nil {
		select {
		case f.polled <- struct{}{}:
		default:
		}
	}
	return append([]WatchedTerminal(nil), f.terminals...), nil
}
func (f *fakeWatcher) Output(ctx context.Context, id string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads = append(f.reads, id)
	return f.screens[id], nil
}

func TestTheWatcherSeamOffersNoWayToSendInput(t *testing.T) {
	typ := reflect.TypeFor[Watcher]()
	if typ.NumMethod() != 2 || typ.Method(0).Name != "Output" || typ.Method(1).Name != "Terminals" {
		t.Fatalf("watcher methods = %v", typ)
	}
}

func TestABlockedTerminalIsReportedOnceWhileItPersistsAndAgainWhenItChanges(t *testing.T) {
	var progress, records bytes.Buffer
	w := &fakeWatcher{terminals: []WatchedTerminal{{ID: "t", Session: "cao-run", Status: "processing", Description: "test-label"}}, screens: map[string]string{"t": claudePermissionDialog("cp")}}
	e := &Engine{Watcher: w, Progress: &progress, Records: &records, Config: Config{RunID: "r"}}
	finish := e.watchStep(Step{Name: "test", Round: 1, ChairName: "tonmeister", Label: "test-label"})
	e.pollBlockers(context.Background())
	e.pollBlockers(context.Background())
	e.pollBlockers(context.Background())
	w.screens["t"] = claudePermissionDialog("rm")
	e.pollBlockers(context.Background())
	w.terminals[0].Status = "completed"
	e.pollBlockers(context.Background())
	finish()
	want := "  blocked: test[1] tonmeister — Permission rule Bash(cp*) requires confirmation for this command. — attach with tmux attach -t cao-run\n  blocked: test[1] tonmeister — Permission rule Bash(rm*) requires confirmation for this command. — attach with tmux attach -t cao-run\n  unblocked: test[1] tonmeister\n"
	if progress.String() != want {
		t.Fatalf("progress = %q", progress.String())
	}
	dec := json.NewDecoder(&records)
	for _, event := range []string{"blocked", "blocked", "unblocked"} {
		var r Record
		if err := dec.Decode(&r); err != nil {
			t.Fatal(err)
		}
		if r.Step != event || r.BlockedStep != "test" || r.Round != 1 || r.ChairName != "tonmeister" || r.TerminalID != "t" || r.Detail == "" {
			t.Fatalf("record = %+v", r)
		}
	}
	if dec.More() {
		t.Fatal("extra telemetry")
	}
}

func TestParallelReviewersAndUnmatchedTerminalsAreReportedByTheirNames(t *testing.T) {
	var progress bytes.Buffer
	w := &fakeWatcher{terminals: []WatchedTerminal{{ID: "a", Status: "waiting_user_answer", Description: "review-a"}, {ID: "b", Status: "waiting_user_answer", Description: "review-b"}, {ID: "c", Status: "waiting_user_answer", Description: "podium"}}, screens: map[string]string{}}
	e := &Engine{Watcher: w, Progress: &progress}
	a := e.watchStep(Step{Name: "review", Round: 1, ChairName: "reviewer-1", Label: "review-a"})
	defer a()
	b := e.watchStep(Step{Name: "review", Round: 1, ChairName: "reviewer-2", Label: "review-b"})
	defer b()
	e.pollBlockers(context.Background())
	for _, name := range []string{"review[1] reviewer-1", "review[1] reviewer-2", "podium"} {
		if !strings.Contains(progress.String(), "blocked: "+name+" — waiting for an answer") {
			t.Fatalf("progress = %s", progress.String())
		}
	}
}

func TestScreensAreReadOnlyForWaitingAndProcessingWithOneListingPerPoll(t *testing.T) {
	w := &fakeWatcher{screens: map[string]string{}, terminals: []WatchedTerminal{{ID: "a", Status: "idle"}, {ID: "b", Status: "completed"}, {ID: "c", Status: "unknown"}, {ID: "d", Status: "processing"}, {ID: "e", Status: "waiting_user_answer"}}}
	var progress bytes.Buffer
	e := &Engine{Watcher: w, Progress: &progress}
	finish := e.watchStep(Step{Name: "plan"})
	defer finish()
	e.pollBlockers(context.Background())
	if w.calls != 1 || strings.Join(w.reads, ",") != "d,e" {
		t.Fatalf("calls=%d reads=%v", w.calls, w.reads)
	}
	if strings.Count(progress.String(), "blocked:") != 1 {
		t.Fatalf("progress = %s", progress.String())
	}
}

func TestAStepEndingWhileBlockedReportsUnblockedAndStopsPolling(t *testing.T) {
	var progress bytes.Buffer
	w := &fakeWatcher{terminals: []WatchedTerminal{{ID: "t", Status: "waiting_user_answer", Description: "plan"}}, screens: map[string]string{}}
	e := &Engine{Watcher: w, Progress: &progress}
	finish := e.watchStep(Step{Name: "plan", ChairName: "planner", Label: "plan"})
	e.pollBlockers(context.Background())
	finish()
	e.pollBlockers(context.Background())
	if w.calls != 1 || !strings.Contains(progress.String(), "  unblocked: plan planner\n") {
		t.Fatalf("calls=%d progress=%q", w.calls, progress.String())
	}
}

func TestWatchingRunsOnlyWhileTheStepperRunsAndZeroTurnsItOff(t *testing.T) {
	for _, interval := range []time.Duration{0, time.Millisecond} {
		t.Run(interval.String(), func(t *testing.T) {
			e, f, _ := newEngine(t, nil)
			w := &fakeWatcher{polled: make(chan struct{}, 1)}
			e.Watcher = w
			e.Config.BlockerPoll = interval
			f.errFor = func(s Step) error { return context.Canceled }
			f.actions = map[string]func(Step){"plan": func(s Step) {
				if interval > 0 {
					select {
					case <-w.polled:
					case <-time.After(time.Second):
						t.Error("no poll during step")
					}
				}
			}}
			_, err := e.Run(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			w.mu.Lock()
			calls := w.calls
			w.mu.Unlock()
			if interval == 0 && calls != 0 {
				t.Fatalf("disabled watcher called %d times", calls)
			}
			select {
			case <-w.polled:
			default:
			}
			select {
			case <-w.polled:
				t.Fatal("watcher continued after run")
			case <-time.After(5 * time.Millisecond):
			}
		})
	}
}

func TestAChairWithoutAnExplicitNameIsReportedByItsPart(t *testing.T) {
	var progress, records bytes.Buffer
	e := &Engine{Watcher: &fakeWatcher{terminals: []WatchedTerminal{{ID: "t", Status: "waiting_user_answer", Description: "test-label"}}, screens: map[string]string{}}, Progress: &progress, Records: &records}
	finish := e.watchStep(Step{Name: "test", Round: 1, Label: "test-label"})
	defer finish()
	e.pollBlockers(context.Background())
	var r Record
	if err := json.NewDecoder(&records).Decode(&r); err != nil {
		t.Fatal(err)
	}
	if r.ChairName != "tester" || !strings.Contains(progress.String(), "blocked: test[1] tester —") {
		t.Fatalf("record=%+v progress=%s", r, progress.String())
	}
}

func TestADisappearingTerminalClearsItsBlocker(t *testing.T) {
	var progress bytes.Buffer
	w := &fakeWatcher{terminals: []WatchedTerminal{{ID: "t", Status: "waiting_user_answer", Description: "plan"}}, screens: map[string]string{}}
	e := &Engine{Watcher: w, Progress: &progress}
	finish := e.watchStep(Step{Name: "plan", Label: "plan"})
	defer finish()
	e.pollBlockers(context.Background())
	w.terminals = nil
	e.pollBlockers(context.Background())
	e.pollBlockers(context.Background())
	if strings.Count(progress.String(), "  blocked:") != 1 || strings.Count(progress.String(), "  unblocked:") != 1 {
		t.Fatalf("progress=%q", progress.String())
	}
}

func TestConcurrentClassifiersSharingADescriptionIncludeEveryChairName(t *testing.T) {
	var progress bytes.Buffer
	w := &fakeWatcher{terminals: []WatchedTerminal{{ID: "t", Status: "waiting_user_answer", Description: "classifier"}}, screens: map[string]string{}}
	e := &Engine{Watcher: w, Progress: &progress}
	a := e.watchStep(Step{Name: "classify-review", Round: 1, ChairName: "reviewer-1", Label: "classifier"})
	defer a()
	b := e.watchStep(Step{Name: "classify-review", Round: 1, ChairName: "reviewer-2", Label: "classifier"})
	defer b()
	e.pollBlockers(context.Background())
	if !strings.Contains(progress.String(), "blocked: classify-review[1] reviewer-1/reviewer-2 —") {
		t.Fatalf("progress=%q", progress.String())
	}
}

func TestWithoutAWatcherTheEngineStillRuns(t *testing.T) {
	e, _, _ := newEngine(t, passing)
	e.Config.BlockerPoll = time.Millisecond
	result, err := e.Run(context.Background())
	if err != nil || result.Outcome != Approved {
		t.Fatalf("result=%+v error=%v", result, err)
	}
}

func TestABusyScreenIsBlockedOnlyByAPermissionDialogNotByPromptTextOrProse(t *testing.T) {
	busy := []string{
		"APPROVE if it approves the change as it stands with no required changes,",
		"Write your review to .kapellmeister/runs/r/review-1-2.md: state plainly whether you approve the change as it stands, then list every required change with file and line.",
		"● I approve the change as it stands, with no required changes; the review is in  .kapellmeister/runs/r/review-2-1.md .",
		`echo "I approve." > "$REPLY"`,
		"blocking point you find yourself. Approve when the task is done, the verification passed, and nothing is blocking.",
		"The watcher reports a rule that requires confirmation. Do you want to proceed? is the dialog's question.",
	}
	for _, screen := range busy {
		var progress bytes.Buffer
		w := &fakeWatcher{terminals: []WatchedTerminal{{ID: "t", Session: "cao-run", Status: "processing", Description: "review"}}, screens: map[string]string{"t": screen}}
		e := &Engine{Watcher: w, Progress: &progress, Records: &bytes.Buffer{}, Config: Config{RunID: "r"}}
		finish := e.watchStep(Step{Name: "review", Round: 1, ChairName: "reviewer-1", Label: "review"})
		e.pollBlockers(context.Background())
		finish()
		if progress.Len() != 0 {
			t.Errorf("screen %q reported %q", screen, progress.String())
		}
	}
}

func claudePermissionDialog(command string) string {
	return "⏺ Bash(" + command + " a b)\nPermission rule Bash(" + command + "*) requires confirmation for this command.\n Do you want to proceed?\n ❯ 1. Yes\n   2. No\nEsc to cancel · Tab to amend"
}

func TestAWaitingTerminalWithoutAPermissionDialogIsReportedAsWaitingForAnAnswer(t *testing.T) {
	var progress bytes.Buffer
	w := &fakeWatcher{terminals: []WatchedTerminal{{ID: "t", Session: "cao-run", Status: "waiting_user_answer", Description: "plan"}}, screens: map[string]string{"t": "APPROVE if it approves the change as it stands\n↑/↓ to navigate · Enter to select"}}
	e := &Engine{Watcher: w, Progress: &progress, Records: &bytes.Buffer{}, Config: Config{RunID: "r"}}
	finish := e.watchStep(Step{Name: "plan", ChairName: "planner", Label: "plan"})
	e.pollBlockers(context.Background())
	finish()
	if want := "  blocked: plan planner — waiting for an answer — attach with tmux attach -t cao-run\n  unblocked: plan planner\n"; progress.String() != want {
		t.Fatalf("progress = %q", progress.String())
	}
}
