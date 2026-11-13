package loop

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func twoReviewers(e *Engine) {
	e.Config.Chairs.Reviewers = []Chair{{Provider: "p", Profile: "x"}, {Provider: "q", Profile: "y"}}
}

func reviewVerdicts(f *fakeStepper, verdicts ...string) {
	f.actions["classify-review"] = func(s Step) {
		i := 0
		if strings.HasSuffix(s.ReplyPath, "-2.verdict") {
			i = 1
		}
		f.write(s.ReplyPath, verdicts[i])
	}
}

func TestReviewersRunInParallelAndTheGateReadsEveryReview(t *testing.T) {
	eng, fake, _ := newEngine(t, passing)
	twoReviewers(eng)
	var started sync.WaitGroup
	started.Add(2)
	both := make(chan struct{})
	go func() { started.Wait(); close(both) }()
	fake.actions["review"] = func(s Step) {
		started.Done()
		select {
		case <-both:
		case <-time.After(2 * time.Second):
			t.Error("reviewers did not run in parallel")
		}
		fake.write(s.ReplyPath, "approved")
	}
	if res := run(t, eng); res.Outcome != Approved {
		t.Fatal(res)
	}
	gate := stepNamed(t, fake, "gate", 1)
	for _, path := range []string{"review-1-1.md", "review-1-2.md"} {
		if !strings.Contains(gate.Prompt, path) {
			t.Errorf("gate lacks %s: %s", path, gate.Prompt)
		}
	}
}

func TestEachReviewerHasItsOwnReplyFileTerminalLabelAndChairName(t *testing.T) {
	eng, fake, records := newEngine(t, passing)
	twoReviewers(eng)
	var progress bytes.Buffer
	eng.Progress = &progress
	run(t, eng)
	seen := map[string]bool{}
	for _, s := range fake.steps {
		if s.Name == "classify-review" && s.Label != "km r1 · classifier" {
			t.Errorf("classifier label: %s", s.Label)
		}
		if s.Name != "review" {
			continue
		}
		n := strings.TrimPrefix(s.ChairName, "reviewer-")
		if s.ReplyPath != eng.rel("review-1-"+n+".md") || s.Label != "km r1 · "+s.ChairName {
			t.Errorf("review step: %+v", s)
		}
		if _, err := os.Stat(filepath.Join(fake.dir, eng.rel("review-1-"+n+".verdict"))); err != nil {
			t.Error(err)
		}
		seen[s.ChairName] = true
	}
	if !seen["reviewer-1"] || !seen["reviewer-2"] {
		t.Fatal(seen)
	}
	for _, step := range []string{"review", "classify-review", "verdict-review"} {
		recs := recordsOf(t, records, step)
		if len(recs) != 2 || recs[0].ChairName == "" || recs[1].ChairName == "" || recs[0].ChairName == recs[1].ChairName {
			t.Errorf("%s: %+v", step, recs)
		}
	}
	for name := range seen {
		if !strings.Contains(progress.String(), name) {
			t.Error(progress.String())
		}
	}
}

func TestASingleReviewerKeepsTodaysNamesFilesAndLabels(t *testing.T) {
	eng, fake, records := newEngine(t, passing)
	run(t, eng)
	s := stepNamed(t, fake, "review", 1)
	if s.ChairName != "" || s.Label != "km r1 · reviewer" || s.ReplyPath != eng.rel("review-1.md") {
		t.Fatal(s)
	}
	if _, err := os.Stat(filepath.Join(fake.dir, eng.rel("review-1.verdict"))); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(records.Bytes(), []byte(`"chair_name"`)) {
		t.Fatal(records.String())
	}
}

func TestAReviewerWithoutAReplyIsNudgedInItsOwnTerminal(t *testing.T) {
	eng, fake, records := newEngine(t, passing)
	twoReviewers(eng)
	fake.actions["review"] = func(s Step) {
		if s.ChairName == "reviewer-1" {
			fake.write(s.ReplyPath, "approved")
		}
	}
	run(t, eng)
	var nudges []Step
	for _, s := range fake.steps {
		if s.Name == "review-nudge" {
			nudges = append(nudges, s)
		}
	}
	if len(nudges) != 1 {
		t.Fatalf("nudges: %+v", nudges)
	}
	s := nudges[0]
	for _, r := range recordsOf(t, records, "review") {
		if r.ChairName == "reviewer-2" && nudges[0].ReuseTerminal != r.TerminalID {
			t.Fatalf("nudge reused %s instead of %s", nudges[0].ReuseTerminal, r.TerminalID)
		}
	}
	if s.ChairName != "reviewer-2" || s.ReuseTerminal == "" || s.Label != "km r1 · reviewer-2" || !strings.Contains(s.Prompt, "review-1-2.md") {
		t.Fatal(s)
	}
}

func TestGateWhenDecidesWhetherTheGateRuns(t *testing.T) {
	for _, tc := range []struct {
		when              string
		verdicts          []string
		verifyFails, gate bool
	}{
		{"any", []string{"CHANGES", "CHANGES"}, false, false},
		{"any", []string{"APPROVE", "CHANGES"}, false, true},
		{"any", []string{"CHANGES", "APPROVE"}, false, true},
		{"all", []string{"APPROVE", "CHANGES"}, false, false},
		{"all", []string{"APPROVE", "APPROVE"}, false, true},
		{"always", []string{"CHANGES", "CHANGES"}, false, true},
		{"any", []string{"APPROVE", "FAILED"}, false, true},
		{"all", []string{"APPROVE", "FAILED"}, false, false},
		{"", []string{"APPROVE", "CHANGES"}, false, true},
		{"any", []string{"CHANGES"}, false, false},
		{"all", []string{"CHANGES"}, false, false},
		{"any", []string{"APPROVE"}, false, true},
		{"all", []string{"APPROVE"}, false, true},
		{"any", []string{"APPROVE", "APPROVE"}, true, false},
		{"all", []string{"APPROVE", "APPROVE"}, true, false},
		{"always", []string{"APPROVE", "APPROVE"}, true, false},
		{"all", []string{"APPROVE", "UNCLEAR"}, false, false},
	} {
		t.Run(fmt.Sprintf("%s-%v-verifyFail=%t", tc.when, tc.verdicts, tc.verifyFails), func(t *testing.T) {
			eng, fake, _ := newEngine(t, passing)
			if len(tc.verdicts) > 1 {
				twoReviewers(eng)
			}
			eng.Config.MaxRounds, eng.Config.GateWhen = 1, tc.when
			eng.Config.SkipBaseline = true
			if tc.verifyFails {
				eng.Verify = func(context.Context, string) (VerifyResult, error) { return VerifyResult{ExitCode: 1}, nil }
			}
			reviewVerdicts(fake, tc.verdicts...)
			fake.errFor = func(s Step) error {
				if s.Name == "review" && s.ChairName == "reviewer-2" && tc.verdicts[1] == "FAILED" {
					return errors.New("review unavailable")
				}
				return nil
			}
			run(t, eng)
			got := false
			for _, s := range fake.steps {
				if s.Name == "gate" {
					got = true
				}
			}
			if got != tc.gate {
				t.Fatalf("gate ran=%t want %t: %v", got, tc.gate, fake.names())
			}
		})
	}
}

func TestTheGateSortsFindingsIntoBlockingAndNitsAndApprovesWhenNothingBlocks(t *testing.T) {
	eng, fake, _ := newEngine(t, passing)
	run(t, eng)
	prompt := stepNamed(t, fake, "gate", 1).Prompt
	for _, phrase := range []string{"blocking", "nit", "each with a reason", "nothing is blocking"} {
		if !strings.Contains(prompt, phrase) {
			t.Errorf("missing %q: %s", phrase, prompt)
		}
	}
	if strings.Contains(prompt, "nothing the review raised remains unresolved") {
		t.Error("gate still requires resolving nits")
	}
	prompt = stepNamed(t, fake, "classify-gate", 1).Prompt
	if !strings.Contains(prompt, "nits or non-blocking are not required changes") {
		t.Fatal(prompt)
	}
}

func TestAFailedReviewerIsRecordedAsFailedNamedToTheGateAndTheRoundContinues(t *testing.T) {
	for _, missing := range []bool{false, true} {
		t.Run(fmt.Sprint("missing=", missing), func(t *testing.T) {
			eng, fake, records := newEngine(t, passing)
			twoReviewers(eng)
			fake.actions["review"] = func(s Step) {
				if s.ChairName == "reviewer-1" {
					fake.write(s.ReplyPath, "approve")
				}
			}
			fake.actions["review-nudge"] = func(Step) {}
			if !missing {
				fake.errFor = func(s Step) error {
					if s.Name == "review" && s.ChairName == "reviewer-2" {
						return errors.New("unavailable")
					}
					return nil
				}
			}
			if res := run(t, eng); res.Outcome != Approved {
				t.Fatal(res)
			}
			got := false
			for _, r := range recordsOf(t, records, "verdict-review") {
				if r.ChairName == "reviewer-2" && r.Verdict == "FAILED" && r.Error != "" {
					got = true
				}
			}
			if !got {
				t.Fatal(records.String())
			}
			gate := stepNamed(t, fake, "gate", 1)
			if !strings.Contains(gate.Prompt, "reviewer-2") || !strings.Contains(gate.Prompt, "q:y") {
				t.Fatal(gate.Prompt)
			}
			n := 0
			for _, s := range fake.steps {
				if s.Name == "classify-review" {
					n++
					if s.ChairName != "reviewer-1" {
						t.Fatalf("failed review was classified: %+v", s)
					}
				}
			}
			if n != 1 {
				t.Fatalf("classifiers=%d", n)
			}
			if _, err := os.Stat(filepath.Join(fake.dir, eng.rel("review-1-2.verdict"))); !os.IsNotExist(err) {
				t.Fatalf("failed review's verdict file: %v", err)
			}
		})
	}
}

func TestTheRunFailsWhenEveryReviewerFails(t *testing.T) {
	for _, several := range []bool{false, true} {
		t.Run(fmt.Sprint(several), func(t *testing.T) {
			eng, fake, _ := newEngine(t, passing)
			if several {
				twoReviewers(eng)
			}
			fake.errFor = func(s Step) error {
				if s.Name == "review" {
					return errors.New("unavailable")
				}
				return nil
			}
			res := run(t, eng)
			if res.Outcome != Failed || !strings.Contains(res.Reason, "unavailable") {
				t.Fatal(res)
			}
			if several && (!strings.Contains(res.Reason, "reviewer-1") || !strings.Contains(res.Reason, "reviewer-2")) {
				t.Fatal(res)
			}
			for _, s := range fake.steps {
				if s.Name == "gate" || s.Name == "classify-review" {
					t.Fatal(fake.names())
				}
			}
		})
	}
}

func TestAReviewerThatModifiesTheWorktreeFailsTheRunAndTheErrorNamesTheGroup(t *testing.T) {
	eng, fake, records := newEngine(t, passing)
	twoReviewers(eng)
	fake.actions["review"] = func(s Step) {
		fake.write(s.ReplyPath, "approved")
		if s.ChairName == "reviewer-2" {
			fake.write("main.go", "package main\n")
		}
	}
	fake.errFor = func(s Step) error {
		if s.Name == "review" && s.ChairName == "reviewer-2" {
			return errors.New("step also failed")
		}
		return nil
	}
	res := run(t, eng)
	for _, text := range []string{"modified the worktree", "reviewer-1", "reviewer-2"} {
		if res.Outcome != Failed || !strings.Contains(res.Reason, text) {
			t.Fatal(res)
		}
	}
	for _, r := range recordsOf(t, records, "verdict-review") {
		if r.Verdict == VerdictFailed {
			t.Fatal(records.String())
		}
	}
	for _, s := range fake.steps {
		if s.Name == "gate" || (s.Name == "classify-review" && s.ChairName == "reviewer-2") {
			t.Fatal(fake.names())
		}
	}
}

func TestFeedbackAfterAnUnapprovedRoundNamesEveryReviewAndTheGate(t *testing.T) {
	for _, gated := range []bool{false, true} {
		t.Run(fmt.Sprint(gated), func(t *testing.T) {
			eng, fake, _ := newEngine(t, passing)
			twoReviewers(eng)
			eng.Config.MaxRounds = 2
			eng.Config.GateWhen = "all"
			if gated {
				reviewVerdicts(fake, "APPROVE", "APPROVE")
			} else {
				reviewVerdicts(fake, "CHANGES", "APPROVE")
			}
			fake.verdicts["gate"] = []string{"CHANGES"}
			run(t, eng)
			p := stepNamed(t, fake, "implement", 2).Prompt
			for _, file := range []string{"review-1-1.md", "review-1-2.md", "verify-1.txt"} {
				if !strings.Contains(p, file) {
					t.Errorf("missing %s: %s", file, p)
				}
			}
			if strings.Contains(p, "gate-1.md") != gated {
				t.Fatal(p)
			}
		})
	}
}

func TestEachReviewerIsResolvedAgainstTheReviewStepSeparately(t *testing.T) {
	eng, fake, records := tieredEngine(t, SizingOn)
	pinned := claudeChair
	pinned.Tier = "large"
	eng.Config.Chairs.Reviewers = []Chair{pinned, claudeChair}
	planSays(fake, "Size: small — one fix")
	run(t, eng)
	for _, r := range recordsOf(t, records, "review") {
		model, source := "claude-opus-5-5", "pinned"
		if r.ChairName == "reviewer-2" {
			model, source = "claude-haiku-5-5", "sized"
		}
		if r.Chair.Model != model || r.TierSource != source {
			t.Fatal(r)
		}
	}
}

func TestImplementationFeedbackUsesTheGatesBlockingFindingsInsteadOfDowngradedReviewerDemands(t *testing.T) {
	for _, gated := range []bool{false, true} {
		t.Run(fmt.Sprint("gateRan=", gated), func(t *testing.T) {
			eng, fake, _ := newEngine(t, passing)
			twoReviewers(eng)
			eng.Config.MaxRounds = 2
			if gated {
				eng.Config.GateWhen = "always"
			}
			fake.actions["review"] = func(s Step) {
				fake.write(s.ReplyPath, "CHANGES\nRequired: fix the incorrect result.\nRequired: rename the local variable.\n")
			}
			reviewVerdicts(fake, "CHANGES", "CHANGES")
			fake.actions["gate"] = func(s Step) {
				fake.write(s.ReplyPath, "CHANGES\nBlocking: fix the incorrect result because it breaks callers.\nNit: rename the local variable; its meaning is already clear.\n")
			}
			fake.verdicts["gate"] = []string{"CHANGES", "CHANGES"}
			if res := run(t, eng); res.Outcome != RoundsExhausted {
				t.Fatal(res)
			}
			prompt := stepNamed(t, fake, "implement", 2).Prompt
			for _, path := range []string{"review-1-1.md", "review-1-2.md", "verify-1.txt"} {
				if !strings.Contains(prompt, path) {
					t.Errorf("feedback lacks %s: %s", path, prompt)
				}
			}
			if gated {
				for _, phrase := range []string{"gate-1.md", "blocking findings", "authoritative", "supersede reviewer requests", "nits need no change"} {
					if !strings.Contains(prompt, phrase) {
						t.Errorf("feedback lacks %q: %s", phrase, prompt)
					}
				}
				if strings.Contains(prompt, "address every required change") {
					t.Error("feedback still asks to implement every reviewer demand")
				}
			} else {
				if !strings.Contains(prompt, "address every required change") || strings.Contains(prompt, "authoritative") || strings.Contains(prompt, "gate-1.md") {
					t.Fatalf("ungated feedback changed: %s", prompt)
				}
			}
		})
	}
}

func TestImplementationFeedbackUsesOnlyThePreviousRoundsGateDecision(t *testing.T) {
	calls := 0
	eng, fake, _ := newEngine(t, func(context.Context, string) (VerifyResult, error) {
		calls++
		if calls == 3 {
			return VerifyResult{ExitCode: 1}, nil
		}
		return VerifyResult{}, nil
	})
	fake.verdicts["gate"] = []string{"CHANGES"}
	if res := run(t, eng); res.Outcome != Approved || res.Rounds != 3 {
		t.Fatal(res)
	}
	second := stepNamed(t, fake, "implement", 2).Prompt
	if !strings.Contains(second, "gate-1.md") || !strings.Contains(second, "authoritative") {
		t.Errorf("missing gate authority after gated round: %s", second)
	}
	third := stepNamed(t, fake, "implement", 3).Prompt
	if !strings.Contains(third, "review-2.md") || !strings.Contains(third, "verify-2.txt") || !strings.Contains(third, "address every required change") || strings.Contains(third, "authoritative") || strings.Contains(third, "gate-1.md") {
		t.Errorf("stale gate authority after ungated round: %s", third)
	}
}

func TestAReviewIsClassifiedAsSoonAsItLandsWhileAnotherReviewerIsStillRunning(t *testing.T) {
	eng, fake, records := newEngine(t, passing)
	twoReviewers(eng)
	secondStarted, classified := make(chan struct{}), make(chan struct{})
	order := make(chan string, 2)
	fake.actions["review"] = func(s Step) {
		if s.ChairName == "reviewer-2" {
			close(secondStarted)
			select {
			case <-classified:
			case <-time.After(2 * time.Second):
				t.Error("reviewer-1 was not classified while reviewer-2 was running")
			}
		}
		fake.write(s.ReplyPath, "looks good")
	}
	fake.actions["classify-review"] = func(s Step) {
		if s.ChairName == "reviewer-1" {
			<-secondStarted
		}
		fake.write(s.ReplyPath, "APPROVE")
		order <- s.ChairName
		if s.ChairName == "reviewer-1" {
			close(classified)
		}
	}
	if res := run(t, eng); res.Outcome != Approved {
		t.Fatal(res)
	}
	recs := recordsOf(t, records, "classify-review")
	if len(recs) != 2 {
		t.Fatalf("classifications: %+v", recs)
	}
	if first, second := <-order, <-order; first != "reviewer-1" || second != "reviewer-2" {
		t.Fatalf("classification order: %s, %s", first, second)
	}
}

func TestAReviewerThatChangesTheTreeFailsTheRoundNamingItEvenAfterAnotherReviewWasClassified(t *testing.T) {
	eng, fake, records := newEngine(t, passing)
	twoReviewers(eng)
	classified := make(chan struct{})
	var once sync.Once
	fake.actions["review"] = func(s Step) {
		if s.ChairName == "reviewer-2" {
			select {
			case <-classified:
			case <-time.After(2 * time.Second):
				t.Error("reviewer-1 was not classified before the tree changed")
			}
			fake.write("main.go", "package main\n")
		}
		fake.write(s.ReplyPath, "looks good")
	}
	fake.actions["classify-review"] = func(s Step) {
		fake.write(s.ReplyPath, "APPROVE")
		if s.ChairName == "reviewer-1" {
			once.Do(func() { close(classified) })
		}
	}
	res := run(t, eng)
	if res.Outcome != Failed || !strings.Contains(res.Reason, "reviewer-2") || !strings.Contains(res.Reason, "modified the worktree") {
		t.Fatal(res)
	}
	if recs := recordsOf(t, records, "verdict-review"); len(recs) != 4 {
		t.Fatalf("completed verdicts: %+v", recs)
	}
	changed := false
	for _, r := range recordsOf(t, records, "review") {
		changed = changed || r.TreeChanged
	}
	if !changed {
		t.Fatal("tree change was not recorded")
	}
	for _, s := range fake.steps {
		if s.Name == "gate" {
			t.Fatal("gate ran after a tree change")
		}
	}
}

func TestCancellingTheContextMidRoundCancelsReviewsAndClassificationsAndLeavesNoStepRunning(t *testing.T) {
	eng, fake, _ := newEngine(t, passing)
	twoReviewers(eng)
	reviewing, classifying := make(chan struct{}), make(chan struct{})
	fake.contextActions = map[string]func(context.Context, Step) error{
		"review": func(ctx context.Context, s Step) error {
			if s.ChairName == "reviewer-1" {
				fake.write(s.ReplyPath, "looks good")
				return nil
			}
			close(reviewing)
			<-ctx.Done()
			return ctx.Err()
		},
		"classify-review": func(ctx context.Context, s Step) error {
			close(classifying)
			<-ctx.Done()
			return ctx.Err()
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan Result, 1)
	go func() {
		res, err := eng.Run(ctx)
		if err != nil {
			t.Error(err)
		}
		done <- res
	}()
	for _, started := range []chan struct{}{reviewing, classifying} {
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Error("review and classification did not both start before cancellation")
		}
	}
	cancel()
	select {
	case res := <-done:
		if res.Outcome != Failed || !strings.Contains(res.Reason, "context canceled") {
			t.Fatal(res)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("round did not join its cancelled steps")
	}
	if n := fake.inFlight(); n != 0 {
		t.Fatalf("%d steps still running", n)
	}
}

func checkVerdictRecord(t *testing.T, records *bytes.Buffer, subject, chair, verdict, path, source string) {
	t.Helper()
	for _, r := range recordsOf(t, records, "verdict-"+subject) {
		if r.ChairName == chair && r.Reply == path {
			if r.Verdict != verdict || r.VerdictSource != source {
				t.Fatalf("verdict: %+v", r)
			}
			return
		}
	}
	t.Fatalf("missing verdict-%s for %s at %s: %s", subject, chair, path, records)
}

func TestABareVerdictLastLineOnAReviewSkipsTheClassifierAndRecordsTheSource(t *testing.T) {
	for _, verdict := range []string{"APPROVE", "CHANGES"} {
		t.Run(verdict, func(t *testing.T) {
			eng, fake, records := newEngine(t, passing)
			twoReviewers(eng)
			eng.Config.MaxRounds = 1
			var progress bytes.Buffer
			eng.Progress = &progress
			fake.actions["review"] = func(s Step) {
				fake.write(s.ReplyPath, "Findings above.\n  "+verdict+"  \n\n \n")
			}
			want := Approved
			if verdict == "CHANGES" {
				want = RoundsExhausted
			}
			if res := run(t, eng); res.Outcome != want {
				t.Fatal(res)
			}
			for i := 1; i <= 2; i++ {
				chair := fmt.Sprintf("reviewer-%d", i)
				path := eng.rel(fmt.Sprintf("review-1-%d.md", i))
				checkVerdictRecord(t, records, "review", chair, verdict, path, "reply")
				line := chair + " verdict: " + verdict + " (from the review's last line)"
				if !strings.Contains(progress.String(), line) {
					t.Errorf("progress lacks %q: %s", line, &progress)
				}
			}
			for _, s := range fake.steps {
				if s.Name == "classify-review" {
					t.Fatal("bare verdict was classified")
				}
			}
		})
	}
}

func TestABareVerdictLastLineOnTheGateSkipsTheClassifierAndRecordsTheSource(t *testing.T) {
	for _, verdict := range []string{"APPROVE", "CHANGES"} {
		t.Run(verdict, func(t *testing.T) {
			eng, fake, records := newEngine(t, passing)
			eng.Config.MaxRounds = 2
			var progress bytes.Buffer
			eng.Progress = &progress
			fake.actions["gate"] = func(s Step) {
				v := verdict
				if s.Round == 2 {
					v = "APPROVE"
				}
				fake.write(s.ReplyPath, "Findings above.\n  "+v+"  \n\n \n")
			}
			res := run(t, eng)
			wantRounds := 1
			if verdict == "CHANGES" {
				wantRounds = 2
				if p := stepNamed(t, fake, "implement", 2).Prompt; !strings.Contains(p, "gate-1.md") || !strings.Contains(p, "blocking findings") {
					t.Fatal(p)
				}
			}
			if res.Outcome != Approved || res.Rounds != wantRounds {
				t.Fatal(res)
			}
			checkVerdictRecord(t, records, "gate", "", verdict, eng.rel("gate-1.md"), "reply")
			if !strings.Contains(progress.String(), "gate verdict: "+verdict+" (from the gate's last line)") {
				t.Fatal(progress.String())
			}
			for _, s := range fake.steps {
				if s.Name == "classify-gate" {
					t.Fatal("bare verdict was classified")
				}
			}
		})
	}
}

func TestAReplyWithoutABareVerdictLastLineIsStillClassified(t *testing.T) {
	for _, reply := range []string{"I APPROVE.", "APPROVE\nFindings below.", "approve", "UNCLEAR", "CHANGES required"} {
		t.Run(reply, func(t *testing.T) {
			eng, fake, records := newEngine(t, passing)
			for _, subject := range []string{"review", "gate"} {
				fake.actions[subject] = func(s Step) { fake.write(s.ReplyPath, reply) }
			}
			if res := run(t, eng); res.Outcome != Approved {
				t.Fatal(res)
			}
			for _, subject := range []string{"review", "gate"} {
				stepNamed(t, fake, "classify-"+subject, 1)
				checkVerdictRecord(t, records, subject, "", "APPROVE", eng.rel(subject+"-1.md"), "")
			}
		})
	}
}

func TestTheReviewAndGatePromptsAskForTheVerdictAloneOnTheLastLine(t *testing.T) {
	eng, fake, _ := newEngine(t, passing)
	run(t, eng)
	for _, subject := range []string{"review", "gate"} {
		prompt := stepNamed(t, fake, subject, 1).Prompt
		if !strings.Contains(prompt, "Put APPROVE or CHANGES alone on the last line of") {
			t.Errorf("%s prompt lacks the bare last line request: %s", subject, prompt)
		}
	}
}

func TestASingleReviewerBehavesAsBeforeExceptForTheBareLineShortcut(t *testing.T) {
	for _, bare := range []bool{false, true} {
		t.Run(fmt.Sprint(bare), func(t *testing.T) {
			eng, fake, records := newEngine(t, passing)
			var progress bytes.Buffer
			eng.Progress = &progress
			if bare {
				fake.actions["review"] = func(s Step) { fake.write(s.ReplyPath, "APPROVE\n") }
			}
			if res := run(t, eng); res.Outcome != Approved {
				t.Fatal(res)
			}
			s := stepNamed(t, fake, "review", 1)
			if s.ChairName != "" || s.Label != "km r1 · reviewer" || s.ReplyPath != eng.rel("review-1.md") {
				t.Fatal(s)
			}
			source := ""
			if bare {
				source = "reply"
				if !strings.Contains(progress.String(), "review verdict: APPROVE (from the review's last line)") {
					t.Fatal(progress.String())
				}
			}
			checkVerdictRecord(t, records, "review", "", "APPROVE", s.ReplyPath, source)
			classified := len(recordsOf(t, records, "classify-review"))
			if (bare && classified != 0) || (!bare && classified != 1) {
				t.Fatalf("bare=%t, classifications=%d", bare, classified)
			}
			if bytes.Contains(records.Bytes(), []byte(`"chair_name"`)) {
				t.Fatal(records.String())
			}
		})
	}
}
