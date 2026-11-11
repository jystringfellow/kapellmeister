package loop

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const learnedDir = ".kapellmeister/learned/"

func writeLesson(t *testing.T, e *Engine, name, text string) {
	t.Helper()
	path := filepath.Join(e.Config.Worktree, learnedDir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0644); err != nil {
		t.Fatal(err)
	}
}

func commitLessons(t *testing.T, e *Engine) string {
	t.Helper()
	for _, args := range [][]string{{"add", "--", strings.TrimSuffix(learnedDir, "/")}, {"-c", "commit.gpgsign=false", "-c", "user.name=t", "-c", "user.email=t@example.invalid", "commit", "-qm", "lessons"}} {
		if err := e.git(args...); err != nil {
			t.Fatal(err)
		}
	}
	base, err := e.gitOutput("rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(base)
}

func requireLessonsApproved(t *testing.T, e *Engine) {
	t.Helper()
	if res := run(t, e); res.Outcome != Approved {
		t.Fatalf("run = %+v", res)
	}
}

func TestAPartFileReachesEveryChairOfThatPartAndNoOther(t *testing.T) {
	e, f, _ := newEngine(t, passing)
	e.Config.Chairs.Reviewers = append(e.Config.Chairs.Reviewers, e.Config.Chairs.Reviewers[0])
	tester := e.Config.Chairs.Planner
	e.Config.Chairs.Tester = &tester
	parts := map[string]string{"plan": "planner", "implement": "implementer", "review": "reviewer", "gate": "gate", "test-plan": "tester", "test": "tester", "classify-review": "classifier", "classify-gate": "classifier", "classify-test": "classifier"}
	for _, part := range AllRoles() {
		writeLesson(t, e, part+".md", "lesson-for-"+part+"\n")
	}
	commitLessons(t, e)
	requireLessonsApproved(t, e)
	seen := map[string]int{}
	for _, s := range f.steps {
		seen[s.Name]++
		if strings.HasPrefix(s.Name, "classify-") {
			if !strings.HasPrefix(s.Prompt, "<lessons>\n") {
				t.Errorf("%s does not start with lessons", s.Name)
			}
		} else if !strings.Contains(s.Prompt, "</task>\n<lessons>\n") {
			t.Errorf("%s lessons do not follow task", s.Name)
		}
		for _, part := range AllRoles() {
			if got, want := strings.Contains(s.Prompt, "lesson-for-"+part), parts[s.Name] == part; got != want {
				t.Errorf("%s has %s lesson = %v, want %v", s.Name, part, got, want)
			}
		}
	}
	for name := range parts {
		if seen[name] == 0 {
			t.Errorf("no %s step", name)
		}
	}
	if seen["review"] != 2 {
		t.Errorf("reviews = %d", seen["review"])
	}
}

func TestAChairFileReachesOnlyItsChairAfterThePartFile(t *testing.T) {
	e, f, _ := newEngine(t, passing)
	a, b := e.Config.Chairs.Reviewers[0], e.Config.Chairs.Reviewers[0]
	a.Address, b.Address = "review.security@x", "review.style@x"
	e.Config.Chairs.Reviewers = []Chair{a, b}
	writeLesson(t, e, "reviewer.md", "PART-LESSON\n")
	writeLesson(t, e, "review.security.md", "SECURITY-LESSON\n")
	commitLessons(t, e)
	requireLessonsApproved(t, e)
	for _, s := range f.steps {
		got := strings.Index(s.Prompt, "SECURITY-LESSON")
		want := s.Name == "review" && s.Chair.Address == a.Address
		if (got >= 0) != want {
			t.Errorf("%s %s chair lesson = %v", s.Name, s.Chair.Address, got >= 0)
		}
		if want && strings.Index(s.Prompt, "PART-LESSON") >= got {
			t.Error("chair lesson precedes part lesson")
		}
	}
}

func TestALessonFileWithoutAnAddressedChairIsNeverReadAsAChairFile(t *testing.T) {
	e, f, records := newEngine(t, passing)
	writeLesson(t, e, "planner.planner.md", "UNADDRESSED-LESSON\n")
	commitLessons(t, e)
	requireLessonsApproved(t, e)
	for _, s := range f.steps {
		if strings.Contains(s.Prompt, "UNADDRESSED-LESSON") {
			t.Error("unaddressed chair got chair lesson")
		}
	}
	if len(recordsOf(t, records, "lessons")) != 0 {
		t.Error("unaddressed chair lesson was recorded")
	}
}

func TestLessonsAreReadFromTheBaseCommitNotTheWorkingFiles(t *testing.T) {
	e, f, _ := newEngine(t, passing)
	writeLesson(t, e, "planner.md", "COMMITTED-LESSON\n")
	commitLessons(t, e)
	writeLesson(t, e, "planner.md", "UNCOMMITTED-LESSON\n")
	writeLesson(t, e, "gate.md", "UNTRACKED-LESSON\n")
	requireLessonsApproved(t, e)
	if !strings.Contains(f.steps[0].Prompt, "COMMITTED-LESSON") {
		t.Error("missing committed lesson")
	}
	for _, s := range f.steps {
		if strings.Contains(s.Prompt, "UNCOMMITTED-LESSON") || strings.Contains(s.Prompt, "UNTRACKED-LESSON") {
			t.Errorf("%s read working files", s.Name)
		}
	}
}

func TestALessonWrittenByAStepIsNotSeenByALaterStepOfTheSameRun(t *testing.T) {
	for _, commit := range []bool{false, true} {
		t.Run(fmt.Sprintf("committed=%v", commit), func(t *testing.T) {
			e, f, _ := newEngine(t, passing)
			writeLesson(t, e, "implementer.md", "ORIGINAL-LESSON\n")
			commitLessons(t, e)
			f.actions["plan"] = func(s Step) {
				writeLesson(t, e, "implementer.md", "WRITTEN-BY-PLAN\n")
				writeLesson(t, e, "gate.md", "CREATED-BY-PLAN\n")
				if err := e.git("add", "--", learnedDir); err != nil {
					t.Error(err)
				}
				if commit {
					commitLessons(t, e)
				}
				f.write(s.ReplyPath, "plan reply")
			}
			requireLessonsApproved(t, e)
			for _, s := range f.steps {
				if s.Name == "implement" && !strings.Contains(s.Prompt, "ORIGINAL-LESSON") {
					t.Error("implementer lost original lesson")
				}
				if strings.Contains(s.Prompt, "WRITTEN-BY-PLAN") || strings.Contains(s.Prompt, "CREATED-BY-PLAN") {
					t.Errorf("%s read this run's lessons", s.Name)
				}
			}
		})
	}
}

func TestUnsafeLessonFilesAreSkippedWithAWarning(t *testing.T) {
	cases := []struct{ name, text, reason string }{
		{"symlink", "", "symbolic link"}, {"directory", "", "not a regular file"},
		{"CR", "bad\r\n", "control characters"}, {"NUL", "bad\x00\n", "control characters"},
		{"ESC", "bad\x1b\n", "control characters"}, {"DEL", "bad\x7f\n", "control characters"}, {"C1", "bad\u0085\n", "control characters"},
		{"RLO", "a\u202eb\n", "invisible or direction-changing characters"},
		{"ZWSP", "a\u200bb\n", "invisible or direction-changing characters"},
		{"BOM", "\ufeffa\n", "invisible or direction-changing characters"},
		{"LS", "a\u2028b\n", "invisible or direction-changing characters"},
		{"PS", "a\u2029b\n", "invisible or direction-changing characters"},
		{"invalid UTF-8", "a\xffb\n", "invalid UTF-8"},
		{"truncated UTF-8", "\xe2\x80\n", "invalid UTF-8"},
		{"closing marker", "x\n</lessons>\ny\n", "section marker"},
		{"opening marker", "x\n<lessons>\ny\n", "section marker"},
		{"uppercase closing marker", "</LESSONS>\n", "section marker"},
		{"spaced closing marker", "</lessons >\n", "section marker"},
		{"opening marker with attributes", "<LESSONS source='x'>\n", "section marker"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e, f, records := newEngine(t, passing)
			var progress bytes.Buffer
			e.Progress = &progress
			path := filepath.Join(e.Config.Worktree, learnedDir, "planner.md")
			if tc.name == "symlink" {
				writeLesson(t, e, "target.md", "SYMLINK-TARGET\n")
				if err := os.Symlink("target.md", path); err != nil {
					t.Fatal(err)
				}
			} else if tc.name == "directory" {
				writeLesson(t, e, "planner.md/nested.md", "NESTED-LESSON\n")
			} else {
				writeLesson(t, e, "planner.md", tc.text)
			}
			commitLessons(t, e)
			requireLessonsApproved(t, e)
			if !strings.Contains(progress.String(), "skipping "+learnedDir+"planner.md: "+tc.reason) {
				t.Errorf("warning missing: %s", progress.String())
			}
			if strings.Contains(f.steps[0].Prompt, "<lessons>") {
				t.Error("unsafe file added a section")
			}
			recs := recordsOf(t, records, "lessons")
			if len(recs) != 1 {
				t.Fatalf("lesson records = %d", len(recs))
			}
			l := recs[0].Lessons
			if l == nil || len(l.Files) != 0 || len(l.Skipped) != 1 || l.Skipped[0].Path != learnedDir+"planner.md" || l.Skipped[0].Reason != tc.reason {
				t.Errorf("skip not recorded: %+v", l)
			}
		})
	}
}

func TestTheRunRecordNamesEachLessonFileAndItsHash(t *testing.T) {
	e, _, records := newEngine(t, passing)
	e.Config.Chairs.Planner.Address = "leads.planner@x"
	contents := []string{"Part\tlesson\n", "Chair lesson"}
	paths := []string{"planner.md", "leads.planner.md"}
	for i, p := range paths {
		writeLesson(t, e, p, contents[i])
	}
	base := commitLessons(t, e)
	requireLessonsApproved(t, e)
	recs := recordsOf(t, records, "lessons")
	if len(recs) != 1 {
		t.Fatalf("lesson records = %d", len(recs))
	}
	l := recs[0].Lessons
	if l == nil {
		t.Fatal("missing lessons record")
	}
	if l.Part != "planner" || l.Address != "leads.planner@x" || l.Base != base {
		t.Errorf("identity = %+v", l)
	}
	if len(l.Files) != 2 {
		t.Fatalf("files = %+v", l.Files)
	}
	for i, file := range l.Files {
		if file.Path != learnedDir+paths[i] || file.SHA256 != fmt.Sprintf("%x", sha256.Sum256([]byte(contents[i]))) || file.Bytes != len(contents[i]) {
			t.Errorf("file %d = %+v", i, file)
		}
	}
}

func TestNoLessonFilesMeansNoSectionNoRecordAndUnchangedPrompts(t *testing.T) {
	e, f, records := newEngine(t, passing)
	requireLessonsApproved(t, e)
	for _, s := range f.steps {
		if strings.Contains(s.Prompt, "<lessons>") {
			t.Error("unexpected lessons section")
		}
	}
	if len(recordsOf(t, records, "lessons")) != 0 {
		t.Error("unexpected lessons record")
	}
	if f.steps[0].Prompt != planPrompt(e.Config.Task, e.rel("plan.md"), false, "") {
		t.Error("empty lessons changed the planner prompt")
	}
}

func TestLessonsSectionIsMarkedAsReviewedGuidanceFromEarlierRuns(t *testing.T) {
	e, f, _ := newEngine(t, passing)
	writeLesson(t, e, "planner.md", "CHECKED-LESSON\n")
	commitLessons(t, e)
	requireLessonsApproved(t, e)
	p := f.steps[0].Prompt
	for _, text := range []string{"<lessons>", "</lessons>", "earlier runs", "reviewed by a person", "agent instruction files take precedence", "CHECKED-LESSON\n"} {
		if !strings.Contains(p, text) {
			t.Errorf("prompt lacks %q", text)
		}
	}
	if strings.Index(p, "<lessons>") < strings.Index(p, "</task>") {
		t.Error("lessons precede task")
	}
}

func TestKmNeverWritesUnderTheLearnedDirectory(t *testing.T) {
	e, _, _ := newEngine(t, passing)
	writeLesson(t, e, "planner.md", "Reviewed\n")
	commitLessons(t, e)
	before, err := e.gitOutput("ls-tree", "-r", "HEAD", "--", learnedDir)
	if err != nil {
		t.Fatal(err)
	}
	requireLessonsApproved(t, e)
	after, err := e.gitOutput("ls-tree", "-r", "HEAD", "--", learnedDir)
	if err != nil {
		t.Fatal(err)
	}
	status, err := e.gitOutput("status", "--porcelain", "--", learnedDir)
	if err != nil || before != after || status != "" {
		t.Fatalf("learned directory changed: %q, %v", status, err)
	}
}

func TestLessonsOverBudgetKeepWholeLinesFromTheTopAndSayTheyWereCut(t *testing.T) {
	e, f, records := newEngine(t, passing)
	var progress bytes.Buffer
	e.Progress = &progress
	e.Config.Chairs.Planner.Address = "leads.planner@x"
	content := strings.Repeat("a", 3997) + "\n" + "too-long\n" + "x\n"
	writeLesson(t, e, "planner.md", content)
	writeLesson(t, e, "leads.planner.md", "CHAIR-AFTER-CUT\n")
	commitLessons(t, e)
	requireLessonsApproved(t, e)
	p := f.steps[0].Prompt
	if !strings.Contains(p, strings.Repeat("a", 3997)+"\n") || strings.Contains(p, "too-long") || strings.Contains(p, "\nx\n") || strings.Contains(p, "CHAIR-AFTER-CUT") {
		t.Error("budget did not preserve the leading whole lines")
	}
	if !strings.Contains(p, "Cut to the lesson budget") {
		t.Error("prompt does not mention cut")
	}
	if !strings.Contains(progress.String(), "lessons: leads.planner@x cut to 3998 of 4025 bytes (kept 1 of 4 lines)") {
		t.Errorf("cut missing: %s", progress.String())
	}
	recs := recordsOf(t, records, "lessons")
	if len(recs) != 1 {
		t.Fatalf("records = %d", len(recs))
	}
	l := recs[0].Lessons
	if l == nil || !l.Cut || l.Bytes != 3998 || len(l.Files) != 2 || l.Files[0].SHA256 != fmt.Sprintf("%x", sha256.Sum256([]byte(content))) {
		t.Errorf("cut or full-file hash missing: %+v", l)
	}
}

func TestAFirstLineLargerThanTheBudgetLeavesNoSectionButIsRecordedAsCut(t *testing.T) {
	e, f, records := newEngine(t, passing)
	writeLesson(t, e, "planner.md", strings.Repeat("x", 4001)+"\nshort\n")
	commitLessons(t, e)
	requireLessonsApproved(t, e)
	if strings.Contains(f.steps[0].Prompt, "<lessons>") {
		t.Error("zero kept lines produced a section")
	}
	recs := recordsOf(t, records, "lessons")
	if len(recs) != 1 {
		t.Fatalf("records = %d", len(recs))
	}
	if l := recs[0].Lessons; l == nil || !l.Cut || l.Bytes != 0 {
		t.Errorf("cut missing: %+v", l)
	}
}

func TestAnUnreadableBaseCommitWarnsAndRunsWithoutLessons(t *testing.T) {
	e, f, _ := newEngine(t, passing)
	var progress bytes.Buffer
	e.Progress = &progress
	gitDir := filepath.Join(e.Config.Worktree, ".git")
	held := filepath.Join(t.TempDir(), "git")
	if err := os.Rename(gitDir, held); err != nil {
		t.Fatal(err)
	}
	f.actions["plan"] = func(s Step) {
		if err := os.Rename(held, gitDir); err != nil {
			t.Error(err)
		}
		f.write(s.ReplyPath, "plan reply")
	}
	requireLessonsApproved(t, e)
	if !strings.Contains(progress.String(), "warning: lessons:") {
		t.Errorf("warning missing: %s", progress.String())
	}
	if strings.Contains(f.steps[0].Prompt, "<lessons>") {
		t.Error("unreadable base produced lessons")
	}
}

func TestLessonBudgetsCountBytesAndNormalizeFinalLines(t *testing.T) {
	for _, tc := range []struct {
		name, text, kept string
		budget           int
		cut              bool
	}{
		{"exact", "é\n", "é\n", 3, false},
		{"bytes", "é\nnext\n", "", 2, true},
		{"no-final-newline", "é", "é\n", 3, false},
		{"newline-counts", "é", "", 2, true},
		{"empty", "", "", 1, false},
		{"tabs-and-newlines", "a\tb\n\n", "a\tb\n\n", 5, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, f, records := newEngine(t, passing)
			e.Config.LearnedBudget = tc.budget
			writeLesson(t, e, "planner.md", tc.text)
			commitLessons(t, e)
			requireLessonsApproved(t, e)
			recs := recordsOf(t, records, "lessons")
			if len(recs) != 1 {
				t.Fatalf("records = %d", len(recs))
			}
			l := recs[0].Lessons
			if l == nil || l.Cut != tc.cut || l.Bytes != len(tc.kept) {
				t.Fatalf("budget result = %+v", l)
			}
			p := f.steps[0].Prompt
			if tc.kept == "" {
				if strings.Contains(p, "<lessons>") {
					t.Error("empty lesson added section")
				}
			} else if !strings.Contains(p, "\n"+tc.kept+"</lessons>\n") {
				t.Errorf("normalized lines missing: %s", p)
			}
		})
	}
}

func TestASymbolicLinkToTheLearnedDirectoryIsNeverFollowed(t *testing.T) {
	e, f, records := newEngine(t, passing)
	path := filepath.Join(e.Config.Worktree, "reviewed")
	if err := os.Mkdir(path, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "planner.md"), []byte("LINKED-DIRECTORY-LESSON\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(e.Config.Worktree, ".kapellmeister"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../reviewed", filepath.Join(e.Config.Worktree, learnedDir)); err != nil {
		t.Fatal(err)
	}
	if err := e.git("add", "--", "reviewed"); err != nil {
		t.Fatal(err)
	}
	commitLessons(t, e)
	requireLessonsApproved(t, e)
	if strings.Contains(f.steps[0].Prompt, "<lessons>") || len(recordsOf(t, records, "lessons")) != 0 {
		t.Error("followed symbolic link to learned directory")
	}
}

func TestMalformedChairAddressesCannotSelectLessonFiles(t *testing.T) {
	for _, address := range []string{"../review@x", "review.security.extra@x", "review/security@x", "review..security@x", "review.security", "review.security@"} {
		if file := chairFileName(address); file != "" {
			t.Errorf("%q selected %s", address, file)
		}
	}
}

func TestLessonsAreLoadedBeforeTheBaselineVerify(t *testing.T) {
	e, f, records := newEngine(t, nil)
	writeLesson(t, e, "planner.md", "BASE-LESSON\n")
	base := commitLessons(t, e)
	calls := 0
	e.Verify = func(ctx context.Context, dir string) (VerifyResult, error) {
		calls++
		if calls != 1 {
			return passing(ctx, dir)
		}
		writeLesson(t, e, "planner.md", "VERIFY-LESSON\n")
		commitLessons(t, e)
		return VerifyResult{}, nil
	}
	requireLessonsApproved(t, e)
	if !strings.Contains(f.steps[0].Prompt, "BASE-LESSON") || strings.Contains(f.steps[0].Prompt, "VERIFY-LESSON") {
		t.Error("lessons loaded after baseline")
	}
	recs := recordsOf(t, records, "lessons")
	if len(recs) != 1 || recs[0].Lessons == nil || recs[0].Lessons.Base != base {
		t.Fatalf("wrong lessons base: %+v", recs)
	}
}

func gitWithStderrWarning(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("git shim requires a POSIX shell")
	}
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	quoted := "'" + strings.ReplaceAll(realGit, "'", "'\"'\"'") + "'"
	script := "#!/bin/sh\n" + quoted + " \"$@\"\nresult=$?\nprintf '%s\\n' 'warning: noisy' >&2\nexit \"$result\"\n"
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestAWarningGitPrintsToStderrWhileSucceedingNeverReachesLessonsOrTheirHash(t *testing.T) {
	e, f, records := newEngine(t, passing)
	text := "Reviewed café lesson\n"
	writeLesson(t, e, "planner.md", text)
	base := commitLessons(t, e)
	oid, err := e.gitOutput("rev-parse", "HEAD:"+learnedDir+"planner.md")
	if err != nil {
		t.Fatal(err)
	}
	gitWithStderrWarning(t)
	if blob := e.readLessonBlob("100644", strings.TrimSpace(oid)); blob.text != text || blob.reason != "" {
		t.Errorf("committed blob differs: %+v", blob)
	}
	requireLessonsApproved(t, e)
	if !strings.Contains(f.steps[0].Prompt, text) {
		t.Error("committed lesson missing")
	}
	for _, step := range f.steps {
		if strings.Contains(step.Prompt, "warning: noisy") {
			t.Errorf("git stderr reached %s", step.Name)
		}
	}
	recs := recordsOf(t, records, "lessons")
	if len(recs) != 1 || recs[0].Lessons == nil {
		t.Fatalf("lesson records = %+v", recs)
	}
	l := recs[0].Lessons
	if l.Base != base || len(l.Files) != 1 {
		t.Fatalf("lesson record = %+v", l)
	}
	if file := l.Files[0]; file.SHA256 != fmt.Sprintf("%x", sha256.Sum256([]byte(text))) || file.Bytes != len(text) {
		t.Errorf("lesson bytes differ: %+v", file)
	}
}

func TestGitOutputKeepsStderrOutOfStdoutAndInTheErrorOnFailure(t *testing.T) {
	e, _, _ := newEngine(t, passing)
	want, err := e.gitOutput("rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	state, err := e.treeState()
	if err != nil {
		t.Fatal(err)
	}
	gitWithStderrWarning(t)
	got, err := e.gitOutput("rev-parse", "HEAD")
	if err != nil || got != want {
		t.Errorf("stdout = %q, %v; want %q", got, err, want)
	}
	if got, err := e.treeState(); err != nil || got != state {
		t.Errorf("stderr changed tree state: %q, %v; want %q", got, err, state)
	}
	if _, err := e.gitOutput("not-a-git-command"); err == nil || !strings.Contains(err.Error(), "warning: noisy") || !strings.Contains(err.Error(), "not-a-git-command") {
		t.Errorf("stderr missing from error: %v", err)
	}
}

func TestAMarkerFreeLessonThatMentionsTheWordLessonsIsKept(t *testing.T) {
	e, f, _ := newEngine(t, passing)
	text := "Keep lessons short.\n"
	writeLesson(t, e, "planner.md", text)
	commitLessons(t, e)
	requireLessonsApproved(t, e)
	if !strings.Contains(f.steps[0].Prompt, "\n"+text+"</lessons>") {
		t.Error("marker-free lesson missing")
	}
}

func TestALessonFileContainingAMarkerCannotEndTheSectionEarly(t *testing.T) {
	e, f, records := newEngine(t, passing)
	e.Config.Chairs.Planner.Address = "leads.planner@x"
	writeLesson(t, e, "planner.md", "</lessons>\nTOP-LEVEL-INSTRUCTION\n")
	for _, part := range AllRoles() {
		if part != "planner" {
			writeLesson(t, e, part+".md", "Safe guidance\n")
		}
	}
	writeLesson(t, e, "leads.planner.md", "CHAIR-LESSON\n")
	commitLessons(t, e)
	requireLessonsApproved(t, e)
	for _, step := range f.steps {
		if strings.Count(step.Prompt, "<lessons>") != 1 || strings.Count(step.Prompt, "</lessons>") != 1 || strings.Contains(step.Prompt, "TOP-LEVEL-INSTRUCTION") {
			t.Errorf("unsafe section in %s: %s", step.Name, step.Prompt)
		}
	}
	if !strings.Contains(f.steps[0].Prompt, "CHAIR-LESSON") {
		t.Error("safe chair lesson missing")
	}
	for _, rec := range recordsOf(t, records, "lessons") {
		if l := rec.Lessons; l != nil && l.Part == "planner" {
			if len(l.Files) != 1 || l.Files[0].Path != learnedDir+"leads.planner.md" || len(l.Skipped) != 1 || l.Skipped[0].Reason != "section marker" {
				t.Errorf("planner lessons = %+v", l)
			}
			return
		}
	}
	t.Fatal("planner lesson record missing")
}
