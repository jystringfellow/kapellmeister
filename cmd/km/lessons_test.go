package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jystringfellow/kapellmeister/internal/loop"
)

type lessonsReplyingStepper struct {
	gateChanges bool
	answer      string
	failure     string
	prompts     []string
	reply       func(loop.Step) error
}

func (f *lessonsReplyingStepper) Run(ctx context.Context, s loop.Step) (loop.StepResult, error) {
	f.prompts = append(f.prompts, s.Prompt)
	if s.Name == "classify-gate" && f.gateChanges {
		err := os.WriteFile(filepath.Join(s.Dir, s.ReplyPath), []byte("CHANGES"), 0600)
		return loop.StepResult{Status: "completed"}, err
	}
	if s.Name != "lessons" {
		return (replyingStepper{}).Run(ctx, s)
	}
	if f.reply != nil {
		return loop.StepResult{Status: "completed"}, f.reply(s)
	}
	switch f.failure {
	case "error":
		return loop.StepResult{}, errors.New("unavailable")
	case "timeout":
		<-ctx.Done()
		return loop.StepResult{}, ctx.Err()
	}
	err := os.WriteFile(filepath.Join(s.Dir, s.ReplyPath), []byte(f.answer), 0600)
	return loop.StepResult{TerminalID: "lessons", Status: "completed"}, err
}
func (*lessonsReplyingStepper) Release(context.Context, string) error { return nil }

func recordLessons(t *testing.T, wt, dir string, f *lessonsReplyingStepper, enabled bool) ensembleSummary {
	t.Helper()
	sum, err := runEnsemble(context.Background(), ensembleRun{RunID: "lesson-run", StateDir: dir, Ensemble: "score", Stepper: f, Progress: io.Discard,
		Config: loop.Config{RunID: "lesson-run", Task: "probe", Worktree: wt, Chairs: plainChairs(), MaxRounds: 1, Lessons: enabled, LessonsTimeout: time.Second}})
	if err != nil {
		t.Fatal(err)
	}
	return sum
}

func TestLessonsJSONInTheStateDirCarriesEachLessonsRunRepositoryAndEnsemble(t *testing.T) {
	s := playStanding(t)
	setOrigin(t, s.Worktree, "https://example.test/owner/repo.git")
	dir := filepath.Join(loop.StateDir(), "runs", "lesson-run")
	sum := recordLessons(t, s.Worktree, dir, &lessonsReplyingStepper{answer: `[{"part":"gate","lesson":"Check focused tests.","evidence":"gate-1: missing tests."}]`}, true)
	data, err := os.ReadFile(filepath.Join(dir, "lessons.json"))
	if err != nil {
		t.Fatal(err)
	}
	var lessons []map[string]string
	if err := json.Unmarshal(data, &lessons); err != nil {
		t.Fatal(err)
	}
	if len(lessons) != 1 {
		t.Fatalf("%s", data)
	}
	for key, want := range map[string]string{"run_id": "lesson-run", "repository": "example.test/owner/repo", "ensemble": "score", "part": "gate", "lesson": "Check focused tests.", "evidence": "gate-1: missing tests."} {
		if lessons[0][key] != want {
			t.Errorf("%s = %q", key, lessons[0][key])
		}
	}
	result := recordedResult(t, dir)
	report, ok := result["lessons"].(map[string]any)
	if !ok || report["proposed"] != float64(1) || sum.Lessons.Proposed != 1 {
		t.Fatalf("%v %+v", result, sum)
	}
	if _, err := os.Stat(filepath.Join(s.Worktree, "lessons.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("lessons in worktree: %v", err)
	}
	status, err := exec.Command("git", "-C", s.Worktree, "status", "--porcelain").Output()
	if err != nil || len(status) != 0 {
		t.Fatalf("status %s: %v", status, err)
	}
}

func TestAZeroLessonAnswerWritesAnEmptyLessonsFile(t *testing.T) {
	for _, answer := range []string{"[]", "unparseable", `[{"part":"gate","lesson":"x","evidence":"y","run_id":"forged"}]`} {
		t.Run(answer, func(t *testing.T) {
			s := playStanding(t)
			dir := filepath.Join(loop.StateDir(), "runs", "lesson-run")
			sum := recordLessons(t, s.Worktree, dir, &lessonsReplyingStepper{answer: answer}, true)
			data, err := os.ReadFile(filepath.Join(dir, "lessons.json"))
			if err != nil || strings.TrimSpace(string(data)) != "[]" || sum.Lessons.Proposed != 0 {
				t.Fatalf("%s %v %+v", data, err, sum)
			}
		})
	}
}

func TestKmNeverReadsLessonsJSONBack(t *testing.T) {
	s := playStanding(t)
	dir := filepath.Join(loop.StateDir(), "runs", "lesson-run")
	hostile := "HOSTILE_LESSON_DO_NOT_READ"
	writeFile(t, filepath.Join(dir, "lessons.json"), hostile)
	f := &lessonsReplyingStepper{answer: "[]"}
	sum := recordLessons(t, s.Worktree, dir, f, true)
	if sum.Outcome != loop.Approved {
		t.Fatal(sum)
	}
	for _, prompt := range f.prompts {
		if strings.Contains(prompt, hostile) {
			t.Fatal("old lesson injected")
		}
	}
}

func TestAFailedLessonsWriteDoesNotFailTheRun(t *testing.T) {
	s := playStanding(t)
	dir := filepath.Join(loop.StateDir(), "runs", "lesson-run")
	if err := os.MkdirAll(filepath.Join(dir, "lessons.json"), 0700); err != nil {
		t.Fatal(err)
	}
	var sum ensembleSummary
	stderr := captureRunStderr(t, func() {
		sum = recordLessons(t, s.Worktree, dir, &lessonsReplyingStepper{answer: `[{"part":"gate","lesson":"Check tests.","evidence":"gate-1: tests."}]`}, true)
	})
	if sum.Outcome != loop.Approved || finishRun(io.Discard, sum) != nil || sum.Lessons.Proposed != 0 || !strings.Contains(sum.Lessons.Reason, "writing lessons.json:") || !strings.Contains(stderr, "km: writing lessons.json:") {
		t.Fatalf("%+v %+v %s", sum, sum.Lessons, stderr)
	}
	report := recordedResult(t, dir)["lessons"].(map[string]any)
	if report["proposed"] != float64(0) {
		t.Fatal(report)
	}
}

func TestNoLessonsFileOrResultKeyIsWrittenWhenLessonsAreOff(t *testing.T) {
	s := playStanding(t)
	dir := filepath.Join(loop.StateDir(), "runs", "lesson-run")
	recordLessons(t, s.Worktree, dir, &lessonsReplyingStepper{answer: "[]"}, false)
	if _, ok := recordedResult(t, dir)["lessons"]; ok {
		t.Fatal("lessons result key")
	}
	if _, err := os.Stat(filepath.Join(dir, "lessons.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
}

func TestKmConfigShowsWhetherLessonsAreOn(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		s := playStanding(t)
		value, want := "false", "lessons off"
		if enabled {
			value, want = "true", "lessons on, limit 5m0s"
		}
		writeFile(t, filepath.Join(s.Worktree, loop.ConfigFile), "[run]\nlessons = "+value+"\n")
		out, err := captureStdout(t, func() error { return configCommand([]string{"-worktree", s.Worktree, "-cao", unreachableCAO}) })
		if err != nil || !strings.Contains(out, want) {
			t.Fatalf("%s %v", out, err)
		}
	}
}

func TestARunCommandKeepsItsExitCodeWhenTheLessonsStepFailsOrIsOff(t *testing.T) {
	for _, changes := range []bool{false, true} {
		for _, mode := range []string{"on", "off", "error", "timeout"} {
			t.Run(mode+map[bool]string{true: " changes", false: " approved"}[changes], func(t *testing.T) {
				f := &runCAO{}
				f.fake.changes = changes
				wt, args := runSessionArgs(t, f)
				value := "true"
				if mode == "off" {
					value = "false"
				}
				if mode == "error" {
					f.fake.failPart = "lessons"
				}
				if mode == "timeout" {
					f.fake.noReplyPart = "lessons"
				}
				grace := "1ms"
				if mode == "timeout" {
					grace = "1h"
				}
				writeFile(t, filepath.Join(wt, loop.ConfigFile), "[test]\nverify = 'true'\n[run]\nlessons = "+value+"\nlessons_timeout = '50ms'\npreflight_timeout = '0'\nreply_grace = '"+grace+"'\nrounds = 1\n[sizing]\nmode = 'off'\n")
				_, err := captureStdout(t, func() error { return runCommand(args) })
				want := error(nil)
				if changes {
					want = errNotApproved
				}
				if !errors.Is(err, want) {
					t.Fatalf("%v, want %v", err, want)
				}
				result := recordedResult(t, onlyRunRecord(t))
				if mode == "off" {
					if _, ok := result["lessons"]; ok {
						t.Fatal(result)
					}
				} else {
					report, ok := result["lessons"].(map[string]any)
					if !ok {
						t.Fatal(result)
					}
					reason, _ := report["reason"].(string)
					if mode == "timeout" && !strings.Contains(reason, "timed out after 50ms") {
						t.Fatal(report)
					}
					if mode == "error" && !strings.HasPrefix(reason, "lessons step failed:") {
						t.Fatal(report)
					}
				}
			})
		}
	}
}

func TestAnUnsafeLessonsReplyIsNotCopiedIntoTheStateRecord(t *testing.T) {
	for _, kind := range []string{"oversized", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			s := playStanding(t)
			dir := filepath.Join(loop.StateDir(), "runs", "lesson-run")
			f := &lessonsReplyingStepper{answer: strings.Repeat("x", 32*1024+1)}
			if kind == "symlink" {
				f.reply = func(step loop.Step) error {
					target := filepath.Join(t.TempDir(), "answer")
					writeFile(t, target, "[]")
					return os.Symlink(target, filepath.Join(step.Dir, step.ReplyPath))
				}
			}
			sum := recordLessons(t, s.Worktree, dir, f, true)
			if sum.Outcome != loop.Approved || sum.Lessons.Proposed != 0 {
				t.Fatal(sum)
			}
			if _, err := os.Lstat(filepath.Join(dir, "replies", "lessons-reply.json")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("unsafe raw reply copied: %v", err)
			}
			if _, err := os.Stat(filepath.Join(dir, "replies", "plan.md")); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestLessonsAreWrittenWhenTheGateDidNotApproveTheRun(t *testing.T) {
	s := playStanding(t)
	dir := filepath.Join(loop.StateDir(), "runs", "lesson-run")
	sum := recordLessons(t, s.Worktree, dir, &lessonsReplyingStepper{gateChanges: true, answer: `[{"part":"reviewer","lesson":"Check focused tests.","evidence":"gate-1: missing tests."}]`}, true)
	if sum.Outcome != loop.RoundsExhausted || sum.Lessons.Proposed != 1 || !errors.Is(finishRun(io.Discard, sum), errNotApproved) {
		t.Fatalf("%+v", sum)
	}
	data, err := os.ReadFile(filepath.Join(dir, "lessons.json"))
	if err != nil {
		t.Fatal(err)
	}
	var lessons []recordedLesson
	if err := json.Unmarshal(data, &lessons); err != nil || len(lessons) != 1 || lessons[0].RunID != "lesson-run" {
		t.Fatalf("%s: %v", data, err)
	}
	if result := recordedResult(t, dir); result["outcome"] != loop.RoundsExhausted {
		t.Fatal(result)
	}
}
