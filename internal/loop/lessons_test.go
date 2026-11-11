package loop

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode"
)

const lessonAnswer = `[{"part":"implementer","lesson":"Run focused tests first.","evidence":"review-1.md: missing regression test."}]`

func lessonsEngine(t *testing.T, answer string) (*Engine, *fakeStepper, *bytes.Buffer) {
	t.Helper()
	e, f, records := newEngine(t, passing)
	e.Config.Lessons = true
	e.Config.LessonsTimeout = time.Second
	f.actions["lessons"] = func(s Step) { f.write(s.ReplyPath, answer) }
	return e, f, records
}

func TestTheGateIsAskedForLessonsAfterAnApprovedRunAndTheyAreReturnedWithTheirCount(t *testing.T) {
	e, f, _ := lessonsEngine(t, lessonAnswer)
	res := run(t, e)
	if res.Outcome != Approved || res.Lessons == nil || res.Lessons.Proposed != 1 || len(res.Proposed) != 1 {
		t.Fatalf("result = %+v", res)
	}
	if f.steps[len(f.steps)-1].Name != "lessons" {
		t.Fatal(f.names())
	}
	if f.released[len(f.released)-2] != "t2" {
		t.Fatalf("implementer must be released before lessons: %v", f.released)
	}
}

func TestTheGateIsAskedForLessonsAfterARunThatWasNotApproved(t *testing.T) {
	e, f, _ := lessonsEngine(t, lessonAnswer)
	f.verdicts["gate"] = []string{"CHANGES", "CHANGES", "CHANGES"}
	res := run(t, e)
	if res.Outcome != RoundsExhausted || res.Lessons == nil || res.Lessons.Proposed != 1 {
		t.Fatalf("result = %+v", res)
	}
}

func TestZeroLessonsIsAcceptedAndReportedAsZeroProposed(t *testing.T) {
	e, _, _ := lessonsEngine(t, "[]")
	res := run(t, e)
	if res.Lessons == nil || res.Lessons.Proposed != 0 || res.Lessons.Reason != "" {
		t.Fatalf("report = %+v", res.Lessons)
	}
}

func TestAnAnswerThatIsNotAJSONArrayOfLessonsProposesNoneAndNamesTheReasonWithoutEchoingIt(t *testing.T) {
	for _, answer := range []string{"hostile secret", "null", `{}`, `[null]`, `[{}] trailing`, `[{"part":"gate","lesson":"x","evidence":"y","run_id":"hostile"}]`} {
		t.Run(answer, func(t *testing.T) {
			e, _, _ := lessonsEngine(t, answer)
			res := run(t, e)
			if res.Outcome != Approved || res.Lessons.Proposed != 0 || res.Lessons.Reason != "the answer was not a JSON array of lessons" {
				t.Fatalf("result = %+v, report = %+v", res, res.Lessons)
			}
		})
	}
}

type lessonsCleanupStepper struct {
	*fakeStepper
	terminalID string
}

func (f *lessonsCleanupStepper) Run(ctx context.Context, step Step) (StepResult, error) {
	out, err := f.fakeStepper.Run(ctx, step)
	if step.Name == "lessons" {
		if out.TerminalID == "" {
			out.TerminalID = "lessons-terminal"
		}
		f.terminalID = out.TerminalID
	}
	return out, err
}

func (f *lessonsCleanupStepper) Release(ctx context.Context, terminalID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return f.fakeStepper.Release(ctx, terminalID)
}

func (f *lessonsCleanupStepper) Screen(ctx context.Context, terminalID string) (string, string, error) {
	if err := ctx.Err(); err != nil {
		return "", "", err
	}
	if terminalID == f.terminalID {
		return "cao-lessons", "lessons diagnostic screen", nil
	}
	return f.fakeStepper.Screen(ctx, terminalID)
}

func TestALessonsStepThatTimesOutProposesNoneAndNeverBlocksTheRunPastItsLimit(t *testing.T) {
	for _, missingReply := range []bool{false, true} {
		t.Run(map[bool]string{false: "step", true: "reply wait"}[missingReply], func(t *testing.T) {
			e, f, records := lessonsEngine(t, "[]")
			cleanup := &lessonsCleanupStepper{fakeStepper: f}
			e.Stepper = cleanup
			e.Config.LessonsTimeout = 15 * time.Millisecond
			e.Config.ReplyGrace = time.Hour
			f.contextActions = map[string]func(context.Context, Step) error{"lessons": func(ctx context.Context, s Step) error {
				if missingReply {
					return nil
				}
				<-ctx.Done()
				return ctx.Err()
			}}
			start := time.Now()
			res := run(t, e)
			if time.Since(start) > 2*time.Second || res.Outcome != Approved || res.Lessons.Proposed != 0 || !strings.Contains(res.Lessons.Reason, "timed out after 15ms") {
				t.Fatalf("result = %+v, report = %+v", res, res.Lessons)
			}
			for _, s := range f.steps {
				if s.Name == "lessons" && s.Timeout != 15*time.Millisecond {
					t.Fatal(s.Timeout)
				}
			}
			if cleanup.terminalID == "" || !slices.Contains(f.released, cleanup.terminalID) {
				t.Errorf("timed-out lessons terminal %q was not released: %v", cleanup.terminalID, f.released)
			}
			var screen string
			for _, line := range bytes.Split(bytes.TrimSpace(records.Bytes()), []byte("\n")) {
				var rec Record
				if err := json.Unmarshal(line, &rec); err != nil {
					t.Fatal(err)
				}
				if rec.Step == "lessons" {
					screen = rec.Screen
				}
			}
			if screen != "lessons diagnostic screen" {
				t.Errorf("lessons screen = %q", screen)
			}

		})
	}
}

func TestALessonsStepThatErrorsProposesNone(t *testing.T) {
	e, f, _ := lessonsEngine(t, "[]")
	f.errFor = func(s Step) error {
		if s.Name == "lessons" {
			return errors.New("gate unavailable")
		}
		return nil
	}
	res := run(t, e)
	if res.Outcome != Approved || res.Lessons.Proposed != 0 || !strings.Contains(res.Lessons.Reason, "gate unavailable") {
		t.Fatalf("result = %+v", res)
	}
}

func TestMoreThanFiveLessonsKeepsTheFirstFive(t *testing.T) {
	var answers []Lesson
	for i := 0; i < 7; i++ {
		answers = append(answers, Lesson{Part: "gate", Lesson: "Check tests.", Evidence: "gate-1.md: missing test."})
	}
	data, _ := json.Marshal(answers)
	got, dropped, err := parseLessons(string(data))
	if err != nil || len(got) != 5 || len(dropped) != 2 {
		t.Fatalf("got %v, dropped %v, err %v", got, dropped, err)
	}
}

func TestALessonOrEvidenceOverItsLengthLimitIsDropped(t *testing.T) {
	for _, lesson := range []Lesson{
		{Part: "gate", Lesson: strings.Repeat("a", 401), Evidence: "gate-1"},
		{Part: "gate", Lesson: "Check.", Evidence: strings.Repeat("a", 301)},
		{Part: "gate", Lesson: " ", Evidence: "gate-1"},
		{Part: "gate", Lesson: "Check.", Evidence: ""},
	} {
		data, _ := json.Marshal([]Lesson{lesson})
		got, dropped, err := parseLessons(string(data))
		if err != nil || len(got) != 0 || len(dropped) != 1 {
			t.Fatalf("got %v, dropped %v, err %v", got, dropped, err)
		}
	}
}

func TestTheTotalSizeOfStoredLessonsIsBounded(t *testing.T) {
	lesson := Lesson{Part: "gate", Lesson: strings.Repeat("界", 400), Evidence: strings.Repeat("界", 300)}
	data, _ := json.Marshal([]Lesson{lesson, lesson, lesson, lesson, lesson})
	got, dropped, err := parseLessons(string(data))
	total := 0
	for _, l := range got {
		total += len(l.Part) + len(l.Lesson) + len(l.Evidence)
	}
	if err != nil || len(got) != 1 || len(dropped) != 4 || total > 4096 {
		t.Fatalf("got %d, dropped %v, bytes %d, err %v", len(got), dropped, total, err)
	}
}

func TestALessonForAPartThatIsNotOneOfKmsPartsIsDropped(t *testing.T) {
	for _, part := range append(AllRoles(), "intruder", strings.Repeat("g", 1000)) {
		data, _ := json.Marshal([]Lesson{{Part: part, Lesson: "Check.", Evidence: "review-1: missing test"}})
		got, dropped, err := parseLessons(string(data))
		want := 0
		if slices.Contains(AllRoles(), part) {
			want = 1
		}
		if err != nil || len(got) != want || len(dropped) != 1-want {
			t.Fatalf("part %q: %v %v %v", part, got, dropped, err)
		}
	}
}

func TestControlCharactersAreRemovedFromStoredLessons(t *testing.T) {
	data, _ := json.Marshal([]Lesson{{Part: "gate", Lesson: "Check\n\t\x1b\x00\u202e\u200b\u2028\u2029tests.", Evidence: "review-1:\n finding"}})
	got, _, err := parseLessons("```json\n" + string(data) + "\n```")
	if err != nil || len(got) != 1 || got[0].Lesson != "Check tests." || got[0].Evidence != "review-1: finding" {
		t.Fatalf("%+v %v", got, err)
	}
	for _, r := range got[0].Lesson + got[0].Evidence {
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp) {
			t.Fatalf("stored control %U", r)
		}
	}
}

func TestAnOversizedOrNonRegularReplyFileIsNotRead(t *testing.T) {
	for _, kind := range []string{"oversized", "symlink", "directory"} {
		t.Run(kind, func(t *testing.T) {
			e, f, _ := lessonsEngine(t, "[]")
			f.actions["lessons"] = func(s Step) {
				path := filepath.Join(f.dir, s.ReplyPath)
				switch kind {
				case "oversized":
					f.write(s.ReplyPath, strings.Repeat("x", 32*1024+1))
				case "symlink":
					target := filepath.Join(t.TempDir(), "answer")
					if err := os.WriteFile(target, []byte(lessonAnswer), 0600); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink(target, path); err != nil {
						t.Fatal(err)
					}
				case "directory":
					if err := os.Mkdir(path, 0700); err != nil {
						t.Fatal(err)
					}
				}
			}
			res := run(t, e)
			want := "the answer was not readable"
			if kind == "oversized" {
				want = "the answer was too large"
			}
			if res.Outcome != Approved || res.Lessons.Proposed != 0 || res.Lessons.Reason != want {
				t.Fatalf("%+v %+v", res, res.Lessons)
			}
		})
	}
}

func TestLessonsAreNotRequestedWhenTheSettingIsOff(t *testing.T) {
	e, f, _ := lessonsEngine(t, lessonAnswer)
	e.Config.Lessons = false
	res := run(t, e)
	if res.Lessons != nil {
		t.Fatal(res.Lessons)
	}
	equal(t, f.names(), []string{"plan", "implement[1]", "review[1]", "classify-review[1]", "gate[1]", "classify-gate[1]"})
}

func TestNoLessonsAreRequestedWhenTheGateNeverGaveAVerdict(t *testing.T) {
	e, f, _ := lessonsEngine(t, lessonAnswer)
	f.errFor = func(s Step) error {
		if s.Name == "plan" {
			return errors.New("plan failed")
		}
		return nil
	}
	res := run(t, e)
	if res.Lessons == nil || res.Lessons.Reason != "the gate gave no verdict" {
		t.Fatalf("%+v", res)
	}
	for _, s := range f.steps {
		if s.Name == "lessons" {
			t.Fatal("asked gate without verdict")
		}
	}
}

func TestNoLessonsAreRequestedAfterAnInterrupt(t *testing.T) {
	e, f, _ := lessonsEngine(t, lessonAnswer)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.actions["classify-gate"] = func(s Step) { f.write(s.ReplyPath, "APPROVE"); cancel() }
	res, err := e.Run(ctx)
	if err != nil || res.Lessons == nil || res.Lessons.Proposed != 0 || !strings.Contains(res.Lessons.Reason, "cancel") {
		t.Fatalf("%+v %v", res, err)
	}
	for _, s := range f.steps {
		if s.Name == "lessons" {
			t.Fatal("asked after interrupt")
		}
	}
}

func TestARunHasTheSameOutcomeRoundsAndReasonWithAndWithoutTheLessonsStep(t *testing.T) {
	for _, outcome := range []string{Approved, RoundsExhausted, Untested, Failed} {
		for _, failure := range []string{"none", "error", "timeout"} {
			t.Run(outcome+"/"+failure, func(t *testing.T) {
				var results []Result
				for _, enabled := range []bool{false, true} {
					e, f, _ := lessonsEngine(t, lessonAnswer)
					e.Config.Lessons = enabled
					e.Config.LessonsTimeout = time.Millisecond
					if outcome == RoundsExhausted || outcome == Failed {
						f.verdicts["gate"] = []string{"CHANGES", "CHANGES", "CHANGES"}
					}
					if outcome == Failed {
						f.errFor = func(s Step) error {
							if s.Name == "implement" && s.Round == 2 {
								return errors.New("implement failed")
							}
							return nil
						}
					}
					if outcome == Untested {
						chair := e.Config.Chairs.Gate
						e.Config.Chairs.Tester = &chair
						f.actions["test-plan"] = func(s Step) { f.write(s.ReplyPath, "plan") }
						f.contextActions = map[string]func(context.Context, Step) error{"test": func(context.Context, Step) error { return context.DeadlineExceeded }}
					}
					if failure != "none" {
						if f.contextActions == nil {
							f.contextActions = map[string]func(context.Context, Step) error{}
						}
						f.contextActions["lessons"] = func(ctx context.Context, _ Step) error {
							if failure == "timeout" {
								<-ctx.Done()
								return ctx.Err()
							}
							return errors.New("lessons failed")
						}
					}
					r := run(t, e)
					if r.Outcome != outcome {
						t.Fatalf("got %+v", r)
					}
					results = append(results, r)
				}
				a, b := results[0], results[1]
				if a.Outcome != b.Outcome || a.Rounds != b.Rounds || a.Reason != b.Reason {
					t.Fatalf("without %+v; with %+v", a, b)
				}
			})
		}
	}
}

func TestALessonsStepThatModifiesTheWorktreeIsDiscardedAndDoesNotChangeTheOutcome(t *testing.T) {
	e, f, records := lessonsEngine(t, lessonAnswer)
	var progress bytes.Buffer
	e.Progress = &progress
	f.actions["lessons"] = func(s Step) { f.write(s.ReplyPath, lessonAnswer); f.write("unexpected.txt", "change") }
	res := run(t, e)
	if res.Outcome != Approved || res.Lessons.Proposed != 0 || res.Lessons.Reason != "lessons step modified the worktree" || !bytes.Contains(records.Bytes(), []byte(`"tree_changed":true`)) || !strings.Contains(progress.String(), "km: lessons step modified the worktree") {
		t.Fatalf("%+v %+v %s", res, res.Lessons, progress.String())
	}
}

func TestTheLessonsPromptListsTheRunsFilesAllowsZeroLessonsAndNamesAtMostFive(t *testing.T) {
	e, f, _ := lessonsEngine(t, lessonAnswer)
	f.actions["gate"] = func(s Step) {
		f.write(s.ReplyPath, "APPROVE")
		f.write(e.rel("lessons-old.md"), "hostile")
		f.write(e.rel("unrelated.txt"), "other")
	}
	run(t, e)
	prompt := f.steps[len(f.steps)-1].Prompt
	for _, want := range []string{"plan.md", "implement-1.md", "review-1.md", "gate-1.md", "verify-1.txt", "empty list is a good", "at most 5", "evidence", "approved", "1", "Do not modify any file other than"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt lacks %q: %s", want, prompt)
		}
	}
	for _, bad := range []string{"lessons-old.md", "unrelated.txt", ".verdict"} {
		if strings.Contains(prompt, bad) {
			t.Errorf("prompt includes %q", bad)
		}
	}
}

func TestTheLessonsStepIsRecordedLikeAnyOtherStepAndNamesItsChair(t *testing.T) {
	e, f, records := lessonsEngine(t, lessonAnswer)
	e.Config.Chairs.Gate.Address = "leads.gate@score"
	res := run(t, e)
	s := f.steps[len(f.steps)-1]
	if s.Label != "km r1 · gate" || s.Chair.Address != "leads.gate@score" || stepKey("lessons") != "gate" {
		t.Fatalf("%+v", s)
	}
	var names []string
	for _, line := range bytes.Split(bytes.TrimSpace(records.Bytes()), []byte("\n")) {
		var rec Record
		if err := json.Unmarshal(line, &rec); err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(rec.Step, "lessons") {
			names = append(names, rec.Step)
			if rec.Step == "lessons" && (rec.Chair == nil || rec.Chair.Address != s.Chair.Address || rec.ReplyChars == 0) {
				t.Fatalf("%+v", rec)
			}
		}
	}
	if !reflect.DeepEqual(names, []string{"lessons", "lessons-proposed"}) || res.Lessons.Proposed != 1 {
		t.Fatalf("%v %+v", names, res)
	}
}

func TestALessonsDeadlineReportedByTheStepperIsRecordedAsATimeout(t *testing.T) {
	e, f, _ := lessonsEngine(t, "[]")
	f.contextActions = map[string]func(context.Context, Step) error{"lessons": func(context.Context, Step) error { return context.DeadlineExceeded }}
	res := run(t, e)
	if res.Outcome != Approved || res.Lessons.Proposed != 0 || res.Lessons.Reason != "lessons step failed: timed out after 1s" {
		t.Fatalf("%+v %+v", res, res.Lessons)
	}
}
