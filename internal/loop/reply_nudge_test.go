package loop

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

type replyWatcher struct {
	listing func(context.Context) ([]WatchedTerminal, error)
}

func (w replyWatcher) Terminals(ctx context.Context) ([]WatchedTerminal, error) {
	return w.listing(ctx)
}
func (replyWatcher) Output(context.Context, string) (string, error) { return "", nil }

func TestAChairThatEndedItsTurnIsNudgedAccordingToItsTerminalAndReply(t *testing.T) {
	for _, tc := range []struct {
		name                           string
		nudge, busy, turn, reply, want time.Duration
		listing                        string
		silentNudge                    bool
	}{
		{name: "idle", nudge: 3 * time.Second, want: 3 * time.Second, listing: "idle"},
		{name: "no watcher", nudge: 30 * time.Second, want: 30 * time.Second},
		{name: "zero nudges at once", listing: "idle"},
		{name: "processing until it stops", nudge: 30 * time.Second, busy: 50 * time.Second, want: 50 * time.Second, listing: "processing"},
		{name: "processing until grace", nudge: 30 * time.Second, busy: time.Hour, want: time.Minute, listing: "processing"},
		{name: "listing fails", nudge: 30 * time.Second, want: 30 * time.Second, listing: "error"},
		{name: "terminal absent", nudge: 30 * time.Second, want: 30 * time.Second, listing: "absent"},
		{name: "reply while waiting", nudge: 30 * time.Second, reply: 10 * time.Second},
		{name: "reply while processing", nudge: 30 * time.Second, reply: 40 * time.Second, busy: time.Hour, listing: "processing"},
		{name: "still inside turn", nudge: 30 * time.Second, turn: 50 * time.Second},
		{name: "nudge also silent", nudge: 30 * time.Second, want: 30 * time.Second, silentNudge: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, f, records := newEngine(t, passing)
			var progress bytes.Buffer
			e.Progress = &progress
			e.Config.ReplyGrace, e.Config.ReplyNudge = time.Minute, tc.nudge
			synctest.Test(t, func(t *testing.T) {
				start := time.Now()
				calls := 0
				if tc.listing != "" {
					e.Watcher = replyWatcher{listing: func(context.Context) ([]WatchedTerminal, error) {
						calls++
						if tc.listing == "error" {
							return nil, errors.New("listing failed")
						}
						if tc.listing == "absent" {
							return []WatchedTerminal{{ID: "another", Status: "processing"}}, nil
						}
						status := "idle"
						if time.Since(start) < tc.busy {
							status = "processing"
						}
						return []WatchedTerminal{{ID: "t1", Status: status}}, nil
					}}
				}
				f.actions["plan"] = func(s Step) {
					if tc.turn > 0 {
						time.Sleep(tc.turn)
						f.write(s.ReplyPath, "done")
						return
					}
					if tc.reply > 0 {
						go func() { time.Sleep(tc.reply); f.write(s.ReplyPath, "done") }()
					}
				}
				nudges := 0
				f.actions["plan-nudge"] = func(s Step) {
					nudges++
					if waited := time.Since(start); waited != tc.want {
						t.Errorf("nudged after %s, want %s", waited, tc.want)
					}
					if s.ReuseTerminal != "t1" {
						t.Errorf("terminal = %q", s.ReuseTerminal)
					}
					if !tc.silentNudge {
						f.write(s.ReplyPath, "done")
					}
				}
				_, err := e.step(context.Background(), Step{Name: "plan", Chair: e.Config.Chairs.Planner, ReplyPath: "reply.md"})
				wantNudges := 1
				if tc.turn > 0 || tc.reply > 0 {
					wantNudges = 0
				}
				if nudges != wantNudges {
					t.Errorf("nudges = %d, want %d", nudges, wantNudges)
				}
				if tc.silentNudge {
					if !errors.Is(err, errNoReply) || time.Since(start) != tc.want+time.Minute {
						t.Errorf("silent nudge: %s, %v", time.Since(start), err)
					}
				} else if err != nil {
					t.Fatal(err)
				}
				if tc.busy == 50*time.Second && calls < 2 {
					t.Errorf("listing calls = %d", calls)
				}
				dec := json.NewDecoder(strings.NewReader(records.String()))
				for dec.More() {
					var rec map[string]any
					if err := dec.Decode(&rec); err != nil {
						t.Fatal(err)
					}
					if rec["step"] == "plan-nudge" {
						if rec["nudged_after_ms"] != float64(tc.want.Milliseconds()) {
							t.Errorf("nudge record = %v", rec)
						}
						note := "nudged at once"
						if tc.want > 0 {
							note = "nudged after " + tc.want.String()
						}
						if !strings.Contains(progress.String(), note) {
							t.Errorf("progress lacks %q: %s", note, progress.String())
						}
					} else if _, ok := rec["nudged_after_ms"]; ok {
						t.Errorf("ordinary step records nudge: %v", rec)
					}
				}
			})
		})
	}
}

func TestTheNudgePromptSaysToDoThePreviousWorkThenWriteTheFile(t *testing.T) {
	got := nudgePrompt("reply.md")
	want := "You ended your turn without writing reply.md. If you have not yet done what the previous instructions asked, do that now, then write reply.md and reply with one short sentence.\n"
	if got != want {
		t.Fatalf("prompt = %q", got)
	}
}

func TestReplyNudgeSettingsUseTheDefaultAndMergeBeforeValidation(t *testing.T) {
	for _, tc := range []struct {
		name, user, repo string
		want             time.Duration
		bad              bool
	}{
		{name: "default", want: 30 * time.Second},
		{name: "repository wins", user: "reply_nudge = '20s'", repo: "reply_nudge = '10s'", want: 10 * time.Second},
		{name: "user value", user: "reply_nudge = '20s'", want: 20 * time.Second},
		{name: "zero", repo: "reply_nudge = '0s'"},
		{name: "clamp implicit default", repo: "reply_grace = '1ms'", want: time.Millisecond},
		{name: "too long", repo: "reply_grace = '1s'\nreply_nudge = '2s'", bad: true},
		{name: "merged too long", user: "reply_nudge = '20s'", repo: "reply_grace = '10s'", bad: true},
		{name: "final grace wins", user: "reply_nudge = '20s'\nreply_grace = '10s'", repo: "reply_grace = '30s'", want: 20 * time.Second},
		{name: "negative", repo: "reply_nudge = '-1s'", bad: true},
		{name: "invalid", repo: "reply_nudge = 'soon'", bad: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := writeUserConfig(t, "[run]\n"+tc.user)
			repo := t.TempDir()
			writeConfig(t, repo, "[run]\n"+tc.repo)
			s, err := LoadSettings(home, repo)
			if tc.bad {
				if err == nil || !strings.Contains(err.Error(), "reply_nudge") {
					t.Fatalf("error = %v", err)
				}
				return
			}
			if err != nil || s.ReplyNudge != tc.want {
				t.Fatalf("nudge = %s, want %s, error = %v", s.ReplyNudge, tc.want, err)
			}
		})
	}
}

func emptyReplyStep(t *testing.T, plan, nudge func(f *fakeStepper, s Step)) (int, error, time.Duration) {
	t.Helper()
	e, f, _ := newEngine(t, passing)
	e.Config.ReplyGrace, e.Config.ReplyNudge = time.Minute, 30*time.Second
	nudges := 0
	var err error
	var took time.Duration
	synctest.Test(t, func(t *testing.T) {
		start := time.Now()
		f.actions["plan"] = func(s Step) { plan(f, s) }
		f.actions["plan-nudge"] = func(s Step) {
			nudges++
			nudge(f, s)
		}
		_, err = e.step(context.Background(), Step{Name: "plan", Chair: e.Config.Chairs.Planner, ReplyPath: "reply.md"})
		took = time.Since(start)
	})
	return nudges, err, took
}

func TestAReplyFileThatExistsButIsStillEmptyIsWaitedForAndReadOnceItHasContent(t *testing.T) {
	nudges, err, took := emptyReplyStep(t, func(f *fakeStepper, s Step) {
		f.write(s.ReplyPath, "")
		go func() { time.Sleep(12 * time.Second); f.write(s.ReplyPath, "done") }()
	}, func(*fakeStepper, Step) {})
	if err != nil || nudges != 0 || took >= 30*time.Second {
		t.Fatalf("nudges = %d, took %s, error = %v", nudges, took, err)
	}
}

func TestAChairWhoseTurnEndedLeavingAnEmptyReplyFileIsNudgedLikeOneThatWroteNone(t *testing.T) {
	nudges, err, took := emptyReplyStep(t, func(f *fakeStepper, s Step) { f.write(s.ReplyPath, " \n") }, func(f *fakeStepper, s Step) { f.write(s.ReplyPath, "done") })
	if err != nil || nudges != 1 || took != 30*time.Second {
		t.Fatalf("nudges = %d, took %s, error = %v", nudges, took, err)
	}
}

func TestAReplyFileStillEmptyAfterTheNudgeAndTheGraceFailsTheStepAndSaysItIsEmpty(t *testing.T) {
	nudges, err, took := emptyReplyStep(t, func(f *fakeStepper, s Step) { f.write(s.ReplyPath, "") }, func(*fakeStepper, Step) {})
	if err == nil || !strings.Contains(err.Error(), "empty reply file at reply.md") || nudges != 1 || took != 90*time.Second {
		t.Fatalf("nudges = %d, took %s, error = %v", nudges, took, err)
	}
}
