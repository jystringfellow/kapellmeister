package loop

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestTuneOrdersWritingCheckingProofAndReviewAndCarriesFeedback(t *testing.T) {
	eng, fake, records := newEngine(t, func(context.Context, string) (VerifyResult, error) {
		t.Fatal("baseline ran")
		return VerifyResult{}, nil
	})
	eng.Meter = NewLogMeter()
	patchDir := t.TempDir()
	for _, name := range []string{"source.patch", "test.patch"} {
		if err := os.WriteFile(filepath.Join(patchDir, name), []byte("patch"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	reply, _ := json.Marshal(TuneReply{Command: "tests", Patches: []TunePatch{{File: "source.patch", Kind: "source"}, {File: "test.patch", Kind: "test"}}})
	var order []string
	fake.actions["implement"] = func(s Step) {
		order = append(order, "write")
		if s.Round == 2 && !strings.Contains(s.Prompt, "proof-1.txt") {
			t.Error(s.Prompt)
		}
		if s.Round > 1 && s.ReuseTerminal == "" {
			t.Error("writer was not reused")
		}
		fake.write(s.ReplyPath, string(reply))
	}
	fake.actions["review"] = func(s Step) { order = append(order, "review"); fake.write(s.ReplyPath, "APPROVE") }
	playingProof := false
	eng.Playing = func(_ int, steps []ActiveStep) {
		for _, s := range steps {
			if s.Step == "prove" {
				playingProof = true
			}
		}
	}
	hooks := TuneHooks{
		PatchDir: patchDir,
		Changed:  func() ([]string, error) { order = append(order, "changed"); return nil, nil },
		Snapshot: func() (string, error) { return "same", nil },
		Prove: func(_ context.Context, _ TuneReply, round int) (TuneProof, error) {
			order = append(order, "prove")
			return TuneProof{Proved: round == 2, Path: "proof-1.txt"}, nil
		},
		Publish: func(int) error { order = append(order, "publish"); return nil },
	}
	res, err := eng.Tune(t.Context(), hooks)
	if err != nil || res.Outcome != Approved || res.Rounds != 2 {
		t.Fatal(res, err)
	}
	want := []string{"write", "changed", "prove", "write", "changed", "prove", "review", "publish"}
	if !reflect.DeepEqual(order, want) {
		t.Fatal(order)
	}
	if !playingProof || !strings.Contains(records.String(), `"step":"prove"`) || res.Usage == nil || res.Usage.UnknownSteps != 3 {
		t.Fatal(records.String(), res.Usage)
	}
}

func TestTuneRejectsPatchesOutsideTheRecordDirectoryAndNeedsBothKinds(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.patch")
	if err := os.WriteFile(outside, []byte("patch"), 0644); err != nil {
		t.Fatal(err)
	}
	inside := filepath.Join(root, "inside.patch")
	if err := os.WriteFile(inside, []byte("patch"), 0644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link.patch")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	for _, patches := range [][]TunePatch{
		{{outside, "source"}, {inside, "test"}},
		{{link, "source"}, {inside, "test"}},
		{{inside, "source"}, {inside, "test"}},
		{{inside, "source"}},
		{{inside, "unknown"}, {outside, "test"}},
	} {
		data, _ := json.Marshal(TuneReply{Command: "tests", Patches: patches})
		if _, err := readTuneReply(string(data), root); err == nil {
			t.Fatal(string(data))
		}
	}
}

func tuneTestHooks(t *testing.T, eng *Engine, fake *fakeStepper) TuneHooks {
	t.Helper()
	dir := t.TempDir()
	for _, name := range []string{"source.patch", "test.patch"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("patch"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	reply, _ := json.Marshal(TuneReply{Command: "tests", Patches: []TunePatch{{File: "source.patch", Kind: "source"}, {File: "test.patch", Kind: "test"}}})
	fake.actions["implement"] = func(s Step) { fake.write(s.ReplyPath, string(reply)) }
	fake.actions["review"] = func(s Step) { fake.write(s.ReplyPath, "CHANGES") }
	return TuneHooks{PatchDir: dir, Changed: func() ([]string, error) { return nil, nil }, Snapshot: func() (string, error) { return "same", nil }, Prove: func(context.Context, TuneReply, int) (TuneProof, error) {
		return TuneProof{Proved: true, Path: "proof.txt", Status: "proved"}, nil
	}, Publish: func(int) error { return nil }}
}

func TestTuneKeepsAProofOrProvesAgainAsTheComparisonRequires(t *testing.T) {
	for _, keep := range []bool{true, false} {
		t.Run(fmt.Sprint(keep), func(t *testing.T) {
			eng, fake, records := newEngine(t, nil)
			eng.Config.MaxRounds = 2
			hooks := tuneTestHooks(t, eng, fake)
			calls := 0
			published := 0
			hooks.Prove = func(context.Context, TuneReply, int) (TuneProof, error) {
				calls++
				return TuneProof{Proved: true, Path: "proof.txt"}, nil
			}
			hooks.Keep = func(TuneReply, int) (bool, string, string, error) {
				return keep, "proof-2.txt", "commands and setup matched", nil
			}
			hooks.Publish = func(round int) error { published = round; return nil }
			fake.actions["review"] = func(s Step) {
				v := "CHANGES"
				if s.Round == 2 {
					v = "APPROVE"
				}
				fake.write(s.ReplyPath, v)
			}
			res, err := eng.Tune(t.Context(), hooks)
			want := 2
			if keep {
				want = 1
			}
			if err != nil || res.Outcome != Approved || calls != want || published != 2 {
				t.Fatal(res, err, calls, published)
			}
			if strings.Contains(records.String(), `"step":"proof-kept"`) != keep {
				t.Fatal(records.String())
			}
		})
	}
}

func TestTuneStopsAtAStepBoundaryAndNeverInterruptsAProofForItsBudget(t *testing.T) {
	for _, boundary := range []string{"implement", "prove", "review"} {
		t.Run(boundary, func(t *testing.T) {
			eng, fake, _ := newEngine(t, nil)
			eng.Config.MaxRounds = 2
			eng.Config.Budget = time.Minute
			now := time.Unix(1, 0)
			eng.Now = func() time.Time { return now }
			hooks := tuneTestHooks(t, eng, fake)
			published := 0
			proved := false
			reviews := 0
			hooks.Publish = func(r int) error { published = r; return nil }
			write := fake.actions["implement"]
			fake.actions["implement"] = func(s Step) {
				write(s)
				if boundary == "implement" {
					now = now.Add(time.Minute)
				}
			}
			hooks.Prove = func(context.Context, TuneReply, int) (TuneProof, error) {
				proved = true
				if boundary == "prove" {
					now = now.Add(time.Minute)
				}
				return TuneProof{Proved: true, Path: "proof.txt"}, nil
			}
			fake.actions["review"] = func(s Step) { reviews++; fake.write(s.ReplyPath, "CHANGES"); now = now.Add(time.Minute) }
			res, err := eng.Tune(t.Context(), hooks)
			if err != nil || res.Outcome != BudgetExhausted {
				t.Fatal(res, err)
			}
			want := 1
			if boundary == "implement" {
				want = 0
			}
			if published != want || proved != (want == 1) || reviews > 1 || boundary == "prove" && reviews != 0 {
				t.Fatal(res, published, proved, reviews)
			}
		})
	}
}

func TestTunePreservesTheLastProofWhenALaterRevisionCannotBeAdopted(t *testing.T) {
	for _, why := range []string{"flaky", "failed", "changes", "outside", "invalid", "interrupt"} {
		t.Run(why, func(t *testing.T) {
			eng, fake, _ := newEngine(t, nil)
			eng.Config.MaxRounds = 2
			hooks := tuneTestHooks(t, eng, fake)
			published := 0
			round := 0
			hooks.Publish = func(r int) error { published = r; return nil }
			write := fake.actions["implement"]
			fake.actions["implement"] = func(s Step) {
				round = s.Round
				write(s)
				if round == 2 && why == "invalid" {
					fake.write(s.ReplyPath, "bad json")
				}
			}
			hooks.Changed = func() ([]string, error) {
				if round == 2 && why == "outside" {
					return []string{"source"}, nil
				}
				return nil, nil
			}
			hooks.Prove = func(context.Context, TuneReply, int) (TuneProof, error) {
				if round == 2 {
					if why == "interrupt" {
						return TuneProof{}, context.Canceled
					}
					if why != "changes" {
						return TuneProof{Status: why, Reason: "unreliable"}, nil
					}
				}
				return TuneProof{Proved: true, Path: "proof.txt"}, nil
			}
			res, err := eng.Tune(t.Context(), hooks)
			if err != nil {
				t.Fatal(err)
			}
			if why == "interrupt" {
				if res.Outcome != Failed || published != 0 {
					t.Fatal(res, published)
				}
				return
			}
			want := 1
			if why == "changes" {
				want = 2
			}
			if res.Outcome != Approved || published != want || res.Reason == "" {
				t.Fatal(res, published)
			}
			if why == "flaky" && !strings.Contains(res.Reason, "flaky") {
				t.Fatal(res)
			}
		})
	}
}

func TestTheReadingChairReviewsPassingCommandsAndNeverRequestsWordingChanges(t *testing.T) {
	prompt := tuneReviewPrompt("task", "reply", "proof", "out")
	for _, want := range []string{"reflects what the repository treats as passing", "left out", "by-hand command", "Do not ask for wording, comment, label or style changes", "name a command to add, remove or alter", "APPROVE or CHANGES", "Do not rerun verification"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("missing %q", want)
		}
	}
}

func TestTuneWithoutAProvedRoundExhaustsItsRoundsAndPublishesNothing(t *testing.T) {
	eng, fake, _ := newEngine(t, nil)
	eng.Config.MaxRounds = 2
	hooks := tuneTestHooks(t, eng, fake)
	hooks.Prove = func(context.Context, TuneReply, int) (TuneProof, error) {
		return TuneProof{Status: "failed", Reason: "clean failure"}, nil
	}
	hooks.Publish = func(int) error { t.Fatal("unproved setup published"); return nil }
	res, err := eng.Tune(t.Context(), hooks)
	if err != nil || res.Outcome != RoundsExhausted || !strings.Contains(res.Reason, "failed") {
		t.Fatal(res, err)
	}
}

func TestTuneNeverPublishesWhenCancellationArrivesDuringASuccessfulProof(t *testing.T) {
	eng, fake, _ := newEngine(t, nil)
	hooks := tuneTestHooks(t, eng, fake)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	hooks.Prove = func(context.Context, TuneReply, int) (TuneProof, error) {
		cancel()
		return TuneProof{Proved: true}, nil
	}
	hooks.Publish = func(int) error { t.Fatal("published after cancellation"); return nil }
	res, err := eng.Tune(ctx, hooks)
	if err != nil || res.Outcome != Failed {
		t.Fatal(res, err)
	}
}

func TestTuneReportsItsSpentBudgetBeforeRejectingAnUnadoptedRevision(t *testing.T) {
	eng, fake, _ := newEngine(t, nil)
	eng.Config.MaxRounds = 1
	eng.Config.Budget = time.Minute
	now := time.Unix(1, 0)
	eng.Now = func() time.Time { return now }
	hooks := tuneTestHooks(t, eng, fake)
	write := fake.actions["implement"]
	fake.actions["implement"] = func(s Step) { write(s); now = now.Add(time.Minute) }
	hooks.Changed = func() ([]string, error) { return []string{"source"}, nil }
	res, err := eng.Tune(t.Context(), hooks)
	if err != nil || res.Outcome != BudgetExhausted {
		t.Fatal(res, err)
	}
}

func TestTheReviewerRefusesAnOmissionWithoutAnObservedReasonAndEvidence(t *testing.T) {
	prompt := tuneReviewPrompt("task", "reply", "proof", "out")
	for _, want := range []string{
		"each test left out has one of the permitted reasons with its evidence",
		"failed or hung in a run made during this tune", "which run", "what it printed",
		"needs something the clean copy cannot have", "network", "a service", "a credential", "another machine",
		"the suite does not fit the time target and this is the part cut", "measured time",
		"by-hand command", "ask for changes when a test is left out without one",
		"when the whole suite would fit the target",
		"A test that looks timing-dependent is not a reason.",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("missing %q", want)
		}
	}
}
