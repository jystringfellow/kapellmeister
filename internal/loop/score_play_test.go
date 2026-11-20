package loop

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func otherScoreEngine(t *testing.T) (*Engine, *fakeStepper) {
	t.Helper()
	e, f, _ := newEngine(t, passing)
	e.Score = Score{Parts: []Part{{Name: "drafter", Prompt: map[string]string{"draft": "Write the change."}}, {Name: "checker"}, {Name: "judge"}}, Steps: []ScoreStep{
		{Name: "draft", Kind: KindProduce, Part: "drafter", Escalate: true, Exits: []Exit{{"done", "inspect"}}},
		{Name: "inspect", Kind: KindReview, Part: "checker", Reads: []string{"draft"}, Exits: []Exit{{"approve", "rule"}, {"changes", "draft"}}},
		{Name: "rule", Kind: KindDecide, Part: "judge", Reads: []string{"draft", "inspect"}, Exits: []Exit{{"approve", "approved"}, {"changes", "changes_requested"}}},
	}}
	e.Config.Chairs.Parts = map[string][]Chair{"drafter": {scoreChair("drafter")}, "checker": {scoreChair("checker")}, "judge": {scoreChair("judge")}}
	for _, name := range []string{"inspect", "rule"} {
		f.actions[name] = func(s Step) { f.write(s.ReplyPath, "APPROVE\n") }
	}
	return e, f
}

func TestAScoreOfProduceReviewAndDecideStepsPlaysInTheOrderItsExitsGive(t *testing.T) {
	e, f := otherScoreEngine(t)
	verifies := 0
	e.Verify = func(ctx context.Context, dir string) (VerifyResult, error) { verifies++; return passing(ctx, dir) }
	res := run(t, e)
	if res.Outcome != Approved || res.Rounds != 1 {
		t.Fatalf("result = %+v", res)
	}
	equal(t, f.names(), []string{"draft[1]", "inspect[1]", "rule[1]"})
	if verifies != 2 {
		t.Fatalf("verify calls = %d", verifies)
	}
	for _, s := range f.steps {
		if filepath.Base(s.ReplyPath) != s.Name+"-1.md" {
			t.Errorf("reply = %s", s.ReplyPath)
		}
		if !strings.Contains(s.Prompt, s.ReplyPath) {
			t.Errorf("brief omits reply: %s", s.Prompt)
		}
	}
}

func TestAScoreEndsChangesRequestedWhenTheDecideStepSendsChangesToThatEnding(t *testing.T) {
	e, f := otherScoreEngine(t)
	f.actions["rule"] = func(s Step) { f.write(s.ReplyPath, "CHANGES\n") }
	res := run(t, e)
	if res.Outcome != "changes_requested" || res.Rounds != 1 {
		t.Fatalf("result = %+v", res)
	}
	equal(t, f.names(), []string{"draft[1]", "inspect[1]", "rule[1]"})
}

func TestABackwardExitStartsANewRoundAndRunsOutOfRounds(t *testing.T) {
	for _, approve := range []bool{false, true} {
		t.Run(fmt.Sprint(approve), func(t *testing.T) {
			e, f := otherScoreEngine(t)
			e.Config.MaxRounds = 2
			f.actions["inspect"] = func(s Step) {
				verdict := "CHANGES"
				if approve && s.Round == 2 {
					verdict = "APPROVE"
				}
				f.write(s.ReplyPath, verdict+"\n")
			}
			res := run(t, e)
			want := RoundsExhausted
			names := []string{"draft[1]", "inspect[1]", "draft[2]", "inspect[2]"}
			if approve {
				want = Approved
				names = append(names, "rule[2]")
			}
			if res.Outcome != want || res.Rounds != 2 {
				t.Fatalf("result = %+v", res)
			}
			equal(t, f.names(), names)
			if _, err := os.Stat(filepath.Join(res.RunDir, "draft-2.md")); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestADecideStepTakesChangesWithoutPlayingWhileTheLatestVerifyFails(t *testing.T) {
	e, f := otherScoreEngine(t)
	e.Score.Steps = e.Score.Steps[:1]
	e.Score.Steps[0].Exits = []Exit{{"done", "rule"}}
	e.Score.Steps = append(e.Score.Steps, ScoreStep{Name: "rule", Kind: KindDecide, Part: "judge", Exits: []Exit{{"approve", "approved"}, {"changes", "draft"}}})
	verifies := 0
	e.Verify = func(context.Context, string) (VerifyResult, error) {
		verifies++
		code := 0
		if verifies == 2 {
			code = 1
		}
		return VerifyResult{ExitCode: code}, nil
	}
	res := run(t, e)
	if res.Outcome != Approved || res.Rounds != 2 {
		t.Fatalf("result = %+v", res)
	}
	equal(t, f.names(), []string{"draft[1]", "draft[2]", "rule[2]"})
}

func TestATestStepTakesAbsentWhenNoChairPlaysItsOptionalPart(t *testing.T) {
	e, f := otherScoreEngine(t)
	e.Score.Parts = append(e.Score.Parts, Part{Name: "prober", Optional: true})
	e.Score.Steps[2].Exits[0].To = "probe"
	e.Score.Steps = append(e.Score.Steps, ScoreStep{Name: "probe", Kind: KindTest, Part: "prober", Exits: []Exit{{"approve", "approved"}, {"changes", "draft"}, {"absent", "approved"}}})
	res := run(t, e)
	if res.Outcome != Approved || res.Rounds != 1 {
		t.Fatalf("result = %+v", res)
	}
	equal(t, f.names(), []string{"draft[1]", "inspect[1]", "rule[1]"})
}

func TestATestStepSendsChangesBackThroughItsExit(t *testing.T) {
	e, f := otherScoreEngine(t)
	e.Score.Parts = append(e.Score.Parts, Part{Name: "prober", Optional: true})
	e.Config.Chairs.Parts["prober"] = []Chair{scoreChair("prober")}
	e.Score.Steps[2].Exits[0].To = "probe"
	e.Score.Steps = append(e.Score.Steps, ScoreStep{Name: "probe", Kind: KindTest, Part: "prober", Reads: []string{"draft"}, Exits: []Exit{{"approve", "approved"}, {"changes", "draft"}, {"absent", "approved"}}})
	f.actions["probe"] = func(s Step) {
		verdict := "CHANGES"
		if s.Round == 2 {
			verdict = "APPROVE"
		}
		f.write(s.ReplyPath, verdict+"\n")
	}
	res := run(t, e)
	if res.Outcome != Approved || res.Rounds != 2 {
		t.Fatalf("result = %+v", res)
	}
	equal(t, f.names(), []string{"draft[1]", "inspect[1]", "rule[1]", "probe[1]", "draft[2]", "inspect[2]", "rule[2]", "probe[2]"})
}

func TestTheBackgroundStepStartsAfterTheStepBeforeItAndIsJoinedBeforeTheFirstStepThatReadsIt(t *testing.T) {
	for _, seated := range []bool{true, false} {
		t.Run(fmt.Sprint(seated), func(t *testing.T) {
			e, f := otherScoreEngine(t)
			e.Score.Parts = append(e.Score.Parts, Part{Name: "scout", Optional: true})
			e.Score.Steps = append([]ScoreStep{{Name: "outline", Kind: KindProduce, Part: "drafter", Writes: "reply", Exits: []Exit{{"done", "draft"}}}, {Name: "survey", Kind: KindProduce, Part: "scout", Writes: "reply", Background: true, Reads: []string{"outline"}}}, e.Score.Steps...)
			e.Score.Steps[3].Reads = append(e.Score.Steps[3].Reads, "survey")
			if seated {
				e.Config.Chairs.Parts["scout"] = []Chair{scoreChair("scout")}
			}
			draftDone := make(chan struct{})
			f.actions["draft"] = func(s Step) { close(draftDone); f.write(s.ReplyPath, "change") }
			f.actions["survey"] = func(s Step) {
				if _, err := os.Stat(filepath.Join(f.dir, e.rel("outline.md"))); err != nil {
					t.Error(err)
				}
				<-draftDone
				f.write(s.ReplyPath, "survey finished")
			}
			f.actions["inspect"] = func(s Step) {
				data, err := os.ReadFile(filepath.Join(f.dir, e.rel("survey.md")))
				if seated && (err != nil || string(data) != "survey finished" || !strings.Contains(s.Prompt, e.rel("survey.md"))) {
					t.Errorf("background reply = %q, %v; brief = %s", data, err, s.Prompt)
				}
				f.write(s.ReplyPath, "APPROVE\n")
			}
			res := run(t, e)
			if res.Outcome != Approved || res.Rounds != 1 {
				t.Fatalf("result = %+v", res)
			}
			names := f.names()
			if names[0] != "outline" {
				t.Fatalf("steps = %v", names)
			}
			if seated {
				if len(names) != 5 {
					t.Fatalf("steps = %v", names)
				}
			} else {
				equal(t, names, []string{"outline", "draft[1]", "inspect[1]", "rule[1]"})
			}
		})
	}
}

func TestSeveralChairsOfAReviewPartPlayInParallelAndEachWritesItsOwnReplyFile(t *testing.T) {
	for _, when := range []string{"any", "all", "always"} {
		t.Run(when, func(t *testing.T) {
			e, f := otherScoreEngine(t)
			e.Config.MaxRounds = 1
			e.Score.Steps[1].ApproveWhen = when
			e.Config.Chairs.Parts["checker"] = []Chair{scoreChair("first"), scoreChair("second")}
			firstStarted, secondStarted := make(chan struct{}), make(chan struct{})
			f.actions["inspect"] = func(s Step) {
				verdict := "APPROVE"
				if s.Chair.Profile == "first" {
					close(firstStarted)
					<-secondStarted
				} else {
					close(secondStarted)
					<-firstStarted
					verdict = "CHANGES"
				}
				f.write(s.ReplyPath, verdict+"\n")
			}
			res := run(t, e)
			want := Approved
			if when == "all" {
				want = RoundsExhausted
			}
			if res.Outcome != want {
				t.Fatalf("result = %+v", res)
			}
			paths := map[string]bool{}
			for _, s := range f.steps {
				if s.Name == "inspect" {
					paths[filepath.Base(s.ReplyPath)] = true
				}
			}
			if len(paths) != 2 || !paths["inspect-1-1.md"] || !paths["inspect-1-2.md"] {
				t.Fatalf("replies = %v", paths)
			}
		})
	}
}

func TestTheEngineDefaultsToTheDefaultScoreWhenNoneIsGiven(t *testing.T) {
	e, f, _ := newEngine(t, passing)
	implicit := run(t, e)
	other, g, _ := newEngine(t, passing)
	other.Score = DefaultScore()
	explicit := run(t, other)
	if implicit.Outcome != explicit.Outcome || implicit.Rounds != explicit.Rounds {
		t.Fatalf("results = %+v, %+v", implicit, explicit)
	}
	equal(t, f.names(), g.names())
}

func TestTheDefaultScoreDeclaresItsHandoffsAndTierRules(t *testing.T) {
	score := DefaultScore()
	reads := map[string][]string{"plan": nil, "test-plan": {"plan"}, "implement": {"plan", "review", "gate", "test"}, "review": {"plan", "implement"}, "gate": {"plan", "implement", "review"}, "test": {"plan", "test-plan", "implement"}}
	for i, step := range score.Steps {
		if score.Index(step.Name) != i {
			t.Errorf("index of %s", step.Name)
		}
		got, ok := score.Step(step.Name)
		if !ok || got.Name != step.Name {
			t.Errorf("step lookup = %+v, %v", got, ok)
		}
		equal(t, step.Reads, reads[step.Name])
		if step.Sizes != (step.Name == "plan") || step.Escalate != (step.Name == "implement") || step.Sized != (step.Name != "plan") {
			t.Errorf("rules for %s = %+v", step.Name, step)
		}
		if step.Brief != step.Name {
			t.Errorf("brief for %s = %s", step.Name, step.Brief)
		}
	}
	review, _ := score.Step("review")
	background, _ := score.Step("test-plan")
	if review.ApproveWhen != "any" || background.Tier != "test" || score.LoopHead() != 2 {
		t.Fatalf("score = %+v", score)
	}
	if score.Index("missing") != -1 {
		t.Fatal("unknown step has an index")
	}
	if _, ok := score.Step("missing"); ok {
		t.Fatal("unknown step exists")
	}
}

func TestAScoreRoutesToStepsInTheOrderItsExitsNameRatherThanTheirListOrder(t *testing.T) {
	e, f := otherScoreEngine(t)
	e.Score.Steps[1], e.Score.Steps[2] = e.Score.Steps[2], e.Score.Steps[1]
	res := run(t, e)
	if res.Outcome != Approved || res.Rounds != 2 {
		t.Fatalf("result = %+v", res)
	}
	equal(t, f.names(), []string{"draft[1]", "inspect[1]", "rule[2]"})
}

func TestAnExitBackToTheSameStepStartsANewRound(t *testing.T) {
	e, f := otherScoreEngine(t)
	e.Score.Steps[1].Exits = []Exit{{"approve", "approved"}, {"changes", "inspect"}}
	f.actions["inspect"] = func(s Step) {
		verdict := "CHANGES"
		if s.Round == 2 {
			verdict = "APPROVE"
		}
		f.write(s.ReplyPath, verdict+"\n")
	}
	res := run(t, e)
	if res.Outcome != Approved || res.Rounds != 2 {
		t.Fatalf("result = %+v", res)
	}
	equal(t, f.names(), []string{"draft", "inspect[1]", "inspect[2]"})
}

func TestUnclearRepliesInAScoreUseTheClassifierForTheirStepKind(t *testing.T) {
	e, f := otherScoreEngine(t)
	e.Score.Classifier = "classifier"
	delete(f.actions, "inspect")
	delete(f.actions, "rule")
	res := run(t, e)
	if res.Outcome != Approved {
		t.Fatalf("result = %+v", res)
	}
	equal(t, f.names(), []string{"draft[1]", "inspect[1]", "classify-inspect[1]", "rule[1]", "classify-rule[1]"})
	for _, s := range f.steps {
		if s.Name == "classify-rule" && !strings.Contains(s.Prompt, "Points it lists as nits or non-blocking are not required changes.") {
			t.Errorf("decision classifier brief = %s", s.Prompt)
		}
		if s.Name == "draft" && s.Label != "km r1 · drafter" {
			t.Errorf("label = %s", s.Label)
		}
	}
}
