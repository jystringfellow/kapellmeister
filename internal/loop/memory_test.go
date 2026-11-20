package loop

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
)

func memoryRounds(t *testing.T) (*Engine, *fakeStepper) {
	t.Helper()
	e, f := otherScoreEngine(t)
	e.Score.Parts[0].Memory = MemoryResume
	e.Config.MaxRounds = 2
	f.actions["inspect"] = func(s Step) {
		verdict := "CHANGES"
		if s.Round == 2 {
			verdict = "APPROVE"
		}
		f.write(s.ReplyPath, verdict+"\n")
	}
	return e, f
}

func TestAResumedProducePartOtherThanTheImplementerPlaysItsSecondRoundInTheTerminalOfItsFirst(t *testing.T) {
	e, f := memoryRounds(t)
	res := run(t, e)
	if res.Outcome != Approved {
		t.Fatal(res)
	}
	var steps []Step
	for _, s := range f.steps {
		if s.Name == "draft" {
			steps = append(steps, s)
		}
	}
	if len(steps) != 2 || !steps[0].KeepTerminal || steps[1].ReuseTerminal == "" {
		t.Fatalf("steps = %+v", steps)
	}
}

func TestAResumedPartWithTwoStepsPlaysItsSecondStepInTheTerminalOfItsFirst(t *testing.T) {
	e, f := otherScoreEngine(t)
	text := `
[parts.scout]
memory = "resume"
[[steps]]
name = "outline"
kind = "produce"
part = "drafter"
writes = "reply"
next = { done = "probe" }
[[steps]]
name = "survey"
kind = "produce"
part = "scout"
writes = "reply"
background = true
[[steps]]
name = "probe"
kind = "test"
part = "scout"
reads = ["survey"]
next = { approve = "approved", changes = "changes_requested", absent = "approved" }
`
	score, err := decodeScore(text, "score.toml", "survey")
	if err != nil {
		t.Fatal(err)
	}
	e.Score = score
	e.Config.Chairs.Parts["scout"] = []Chair{scoreChair("scout")}
	f.actions["probe"] = func(s Step) { f.write(s.ReplyPath, "APPROVE\n") }
	if res := run(t, e); res.Outcome != Approved {
		t.Fatal(res)
	}
	var survey, probe Step
	for _, s := range f.steps {
		if s.Name == "survey" {
			survey = s
		}
		if s.Name == "probe" {
			probe = s
		}
	}
	if !survey.KeepTerminal || probe.ReuseTerminal == "" {
		t.Fatalf("survey=%+v probe=%+v", survey, probe)
	}
}

func TestMemoryLimitsAreRefusedOnFreshPartsAndBelowOne(t *testing.T) {
	for _, limit := range []string{"resume_tokens", "resume_steps"} {
		for _, tc := range []struct {
			memory string
			value  int
			reason string
		}{
			{"fresh", 1, "requires memory = resume"}, {"", 1, "requires memory = resume"}, {"resume", 0, "must be at least 1"}, {"resume", -1, "must be at least 1"},
		} {
			t.Run(fmt.Sprintf("%s/%s/%d", limit, tc.memory, tc.value), func(t *testing.T) {
				memory := ""
				if tc.memory != "" {
					memory = fmt.Sprintf("memory = %q\n", tc.memory)
				}
				text := strings.Replace(smallScoreFile, `memory = "fresh"`, fmt.Sprintf("%s%s = %d", memory, limit, tc.value), 1)
				_, err := decodeScore(text, "score.toml", "small")
				if err == nil || !strings.Contains(err.Error(), "score.toml: part drafter: "+limit+" "+tc.reason) {
					t.Fatalf("error=%v", err)
				}
			})
		}
	}
}

func TestAResumedChairStartsFreshAfterResumeStepsWhenUsageIsUnknown(t *testing.T) {
	e, f := memoryRounds(t)
	score, err := decodeScoreWithPartText(strings.Replace(smallScoreFile, `memory = "fresh"`, "memory = \"resume\"\nresume_steps = 1", 1), "score.toml", "small", true)
	if err != nil {
		t.Fatal(err)
	}
	e.Score.Parts[0] = score.Parts[0]
	var records strings.Builder
	e.Records = &records
	if res := run(t, e); res.Outcome != Approved {
		t.Fatal(res)
	}
	var second Step
	for _, s := range f.steps {
		if s.Name == "draft" && s.Round == 2 {
			second = s
		}
	}
	if second.ReuseTerminal != "" || !second.KeepTerminal {
		t.Fatalf("step=%+v", second)
	}
	found := false
	for _, line := range strings.Split(strings.TrimSpace(records.String()), "\n") {
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatal(err)
		}
		if rec["step"] == "draft" && rec["round"] == float64(2) {
			found = rec["memory"] == "fresh" && strings.Contains(fmt.Sprint(rec["memory_reason"]), "resume_steps")
		}
	}
	if !found {
		t.Fatal(records.String())
	}
	if !slices.Contains(f.released, "t1") {
		t.Fatal(f.released)
	}
}

func TestEveryKeptTerminalIsReleasedWhenTheRunEndsApprovedFailedOrInterrupted(t *testing.T) {
	for _, ending := range []string{"approved", "failed", "interrupted"} {
		t.Run(ending, func(t *testing.T) {
			e, f := memoryRounds(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if ending != "approved" {
				f.errFor = func(s Step) error {
					if s.Name == "inspect" {
						if ending == "interrupted" {
							cancel()
							return context.Canceled
						}
						return errors.New("step failed")
					}
					return nil
				}
			}
			res, err := e.Run(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if (ending == "approved") != (res.Outcome == Approved) {
				t.Fatal(res)
			}
			if !slices.Contains(f.released, "t1") {
				t.Fatal(f.released)
			}
		})
	}
}

func TestAResumedChairRecordsWhetherItStartsFreshOrResumes(t *testing.T) {
	e, _ := memoryRounds(t)
	var records strings.Builder
	e.Records = &records
	if res := run(t, e); res.Outcome != Approved {
		t.Fatal(res)
	}
	var got []string
	for _, line := range strings.Split(strings.TrimSpace(records.String()), "\n") {
		var rec Record
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatal(err)
		}
		if rec.Step == "draft" {
			got = append(got, rec.Memory)
		}
	}
	if !slices.Equal(got, []string{"fresh", "resume"}) {
		t.Fatal(got)
	}
}

func TestAResumedChairStartsFreshWhenTheKeptTerminalIsRefusedAndTheRunGoesOn(t *testing.T) {
	for _, refused := range []bool{true, false} {
		t.Run(fmt.Sprint(refused), func(t *testing.T) {
			e, f := memoryRounds(t)
			var records strings.Builder
			e.Records = &records
			f.errFor = func(s Step) error {
				if s.Name == "draft" && s.ReuseTerminal != "" {
					if refused {
						return fmt.Errorf("%w: gone", ErrReuseRefused)
					}
					return context.DeadlineExceeded
				}
				return nil
			}
			res := run(t, e)
			if (res.Outcome == Approved) != refused {
				t.Fatal(res)
			}
			var recs []Record
			for _, line := range strings.Split(strings.TrimSpace(records.String()), "\n") {
				var rec Record
				json.Unmarshal([]byte(line), &rec)
				if rec.Step == "draft" {
					recs = append(recs, rec)
				}
			}
			if refused && (len(recs) != 2 || recs[1].Memory != "fresh" || !strings.Contains(recs[1].MemoryReason, "could not be reused") || recs[1].Error != "") {
				t.Fatalf("records=%+v", recs)
			}
			if !slices.Contains(f.released, "t1") {
				t.Fatal(f.released)
			}
		})
	}
}

type memoryMeter struct{ usage Usage }

func (m memoryMeter) Open(context.Context, Step) (func() Usage, error) {
	return func() Usage { return m.usage }, nil
}

func TestAResumedChairStartsFreshWhenItsLastStepUsedMoreInputTokensThanResumeTokens(t *testing.T) {
	for _, tc := range []struct {
		known  bool
		tokens int64
		fresh  bool
	}{{true, 11, true}, {true, 10, false}, {false, 11, false}} {
		t.Run(fmt.Sprint(tc), func(t *testing.T) {
			e, f := memoryRounds(t)
			e.Score.Parts[0].ResumeTokens = 10
			e.Meter = memoryMeter{Usage{Known: tc.known, Tokens: &Tokens{InputTokens: tc.tokens}}}
			var records strings.Builder
			e.Records = &records
			if res := run(t, e); res.Outcome != Approved {
				t.Fatal(res)
			}
			s := stepNamed(t, f, "draft", 2)
			if (s.ReuseTerminal == "") != tc.fresh {
				t.Fatalf("step=%+v", s)
			}
			if tc.fresh && !strings.Contains(records.String(), "resume_tokens") {
				t.Fatal(records.String())
			}
		})
	}
}

func TestAResumedChairWhoseTierAgentOrModelChangesStartsFreshAndRecordsWhy(t *testing.T) {
	for _, change := range []string{"tier", "agent", "model"} {
		t.Run(change, func(t *testing.T) {
			e, f, records := tieredEngine(t, SizingOff)
			e.Config.MaxRounds = 2
			after := 1
			if change == "tier" {
				e.Config.Policy.Sizing.EscalateAfter = &after
			}
			f.verdicts["review"] = []string{"CHANGES", "APPROVE"}
			if change != "tier" {
				f.actions["review"] = func(s Step) {
					if s.Round == 1 {
						if change == "model" {
							spec := e.Config.Policy.Tiers[e.Config.Chairs.Implementer.Provider]["medium"]
							spec.Model = "other"
							e.Config.Policy.Tiers[e.Config.Chairs.Implementer.Provider]["medium"] = spec
						} else {
							spec := e.Config.Policy.Tiers[e.Config.Chairs.Implementer.Provider]["medium"]
							spec.Effort = "low"
							e.Config.Policy.Tiers[e.Config.Chairs.Implementer.Provider]["medium"] = spec
						}
						f.write(s.ReplyPath, "CHANGES\n")
					} else {
						f.write(s.ReplyPath, "APPROVE\n")
					}
				}
			}
			if res := run(t, e); res.Outcome != Approved {
				t.Fatal(res)
			}
			recs := recordsOf(t, records, "implement")
			if len(recs) != 2 || recs[1].Memory != "fresh" || !strings.Contains(recs[1].MemoryReason, change) {
				t.Fatalf("records=%+v", recs)
			}
			if stepNamed(t, f, "implement", 2).ReuseTerminal != "" {
				t.Fatal("reused changed chair")
			}
		})
	}
}

func TestAKeptTerminalIsNotLentToTwoStepsAtOnce(t *testing.T) {
	e, f := otherScoreEngine(t)
	e.Score.Parts[0].Memory = MemoryResume
	e.Score.Steps = append([]ScoreStep{
		{Name: "outline", Part: "drafter", Kind: KindProduce, Writes: "reply", Exits: []Exit{{"done", "pause"}}},
		{Name: "survey", Part: "drafter", Kind: KindProduce, Writes: "reply", Background: true},
		{Name: "pause", Part: "judge", Kind: KindProduce, Writes: "reply", Exits: []Exit{{"done", "draft"}}},
	}, e.Score.Steps...)
	started, finish := make(chan struct{}), make(chan struct{})
	f.actions["pause"] = func(s Step) { <-started; f.write(s.ReplyPath, "pause") }
	f.actions["survey"] = func(s Step) { close(started); <-finish; f.write(s.ReplyPath, "survey") }
	f.actions["draft"] = func(s Step) {
		<-started
		if s.ReuseTerminal != "" || s.KeepTerminal || s.Memory != "fresh" || s.MemoryReason != "terminal in use" {
			t.Errorf("step=%+v", s)
		}
		close(finish)
		f.write(s.ReplyPath, "draft")
	}
	if res := run(t, e); res.Outcome != Approved {
		t.Fatal(res)
	}
}

func TestEveryKeptBackgroundTerminalIsReleasedAfterItsStepEndsOnEveryRunEnding(t *testing.T) {
	for _, ending := range []string{"approved", "failed", "interrupted"} {
		t.Run(ending, func(t *testing.T) {
			e, f := otherScoreEngine(t)
			e.Score.Parts = append(e.Score.Parts, Part{Name: "scout", Memory: MemoryResume})
			e.Config.Chairs.Parts["scout"] = []Chair{scoreChair("scout")}
			e.Score.Steps = append([]ScoreStep{
				{Name: "outline", Part: "drafter", Kind: KindProduce, Writes: "reply", Exits: []Exit{{"done", "draft"}}},
				{Name: "survey", Part: "scout", Kind: KindProduce, Writes: "reply", Background: true},
			}, e.Score.Steps...)
			e.Score.Parts[0].Memory = MemoryResume
			started := make(chan struct{})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			f.contextActions = map[string]func(context.Context, Step) error{"survey": func(ctx context.Context, s Step) error {
				close(started)
				if ending != "approved" {
					<-ctx.Done()
				}
				f.write(s.ReplyPath, "survey")
				return nil
			}}
			f.errFor = func(s Step) error {
				if s.Name == "draft" {
					<-started
					if ending == "interrupted" {
						cancel()
						return context.Canceled
					}
					if ending == "failed" {
						return errors.New("step failed")
					}
				}
				return nil
			}
			res, err := e.Run(ctx)
			if err != nil || (res.Outcome == Approved) != (ending == "approved") {
				t.Fatalf("result=%+v err=%v", res, err)
			}
			if f.inFlight() != 0 {
				t.Fatal("step still playing")
			}
			ids := map[string]bool{}
			for _, s := range f.steps {
				if s.KeepTerminal {
					if s.ReuseTerminal != "" {
						ids[s.ReuseTerminal] = true
					}
				}
			}
			for _, line := range strings.Split(strings.TrimSpace(e.Records.(*bytes.Buffer).String()), "\n") {
				var rec Record
				json.Unmarshal([]byte(line), &rec)
				if rec.Kept {
					ids[rec.TerminalID] = true
				}
			}
			if len(ids) == 0 {
				t.Fatal("no kept terminal")
			}
			for id := range ids {
				if !slices.Contains(f.released, id) {
					t.Fatalf("terminal %s not released: %v", id, f.released)
				}
			}
		})
	}
}

func TestAFilePartOtherThanTheImplementerCanResumeWithBothLimits(t *testing.T) {
	score, err := decodeScore(strings.Replace(smallScoreFile, `memory = "fresh"`, "memory = \"resume\"\nresume_steps = 3\nresume_tokens = 10", 1), "score.toml", "small")
	if err != nil {
		t.Fatal(err)
	}
	part, ok := score.Part("drafter")
	if !ok || part.Memory != MemoryResume || part.ResumeSteps != 3 || part.ResumeTokens != 10 {
		t.Fatal(part)
	}
}

func TestAReviewPartCannotSetMemoryLimits(t *testing.T) {
	for _, limit := range []string{"resume_tokens", "resume_steps"} {
		_, err := decodeScore(strings.Replace(smallScoreFile, "[parts.drafter]", fmt.Sprintf("[parts.reviewer]\n%s = 1\n[parts.drafter]", limit), 1), "score.toml", "small")
		if err == nil || !strings.Contains(err.Error(), "score.toml: part reviewer: "+limit+" requires memory = resume") {
			t.Fatalf("error=%v", err)
		}
	}
}
