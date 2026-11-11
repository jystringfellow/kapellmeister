package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/jystringfellow/kapellmeister/internal/cao"
	"github.com/jystringfellow/kapellmeister/internal/ensemble"
	"github.com/jystringfellow/kapellmeister/internal/loop"
)

func commitChairLessons(t *testing.T, worktree string, files map[string]string) {
	t.Helper()
	for path, text := range files {
		writeFile(t, filepath.Join(worktree, ".kapellmeister/learned", path), text)
	}
	for _, args := range [][]string{{"add", "-f", "--", ".kapellmeister/learned"}, {"-c", "commit.gpgsign=false", "commit", "-qm", "reviewed lessons"}} {
		if out, err := exec.Command("git", append([]string{"-C", worktree}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
}

func requireLessonRecordAddress(t *testing.T, address string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(onlyRunRecord(t), "telemetry.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	lines := bufio.NewScanner(bytes.NewReader(data))
	for lines.Scan() {
		var r struct {
			Step    string
			Lessons struct{ Address string }
		}
		if err := json.Unmarshal(lines.Bytes(), &r); err != nil {
			t.Fatal(err)
		}
		if r.Step == "lessons" && r.Lessons.Address == address {
			return
		}
	}
	t.Fatalf("no lessons record for %s", address)
}

func requireChairLessonRequests(t *testing.T, requests []cao.StepRequest, plannerFile, reviewerFile string) {
	t.Helper()
	seenPlanner, seenReview := false, false
	for _, r := range requests {
		paths := playReplyPath.FindAllString(r.Prompt, -1)
		if len(paths) == 0 {
			t.Fatalf("no reply path in %s", r.Prompt)
		}
		reply := filepath.Base(paths[len(paths)-1])
		part := playReplyPart(reply)
		if part == "planner" {
			seenPlanner = true
			if !strings.Contains(r.Prompt, plannerFile) || !strings.Contains(r.Prompt, "PLANNER-KEPT\n") || strings.Contains(r.Prompt, "PLANNER-CUT") {
				t.Errorf("planner budget was not applied: %s", r.Prompt)
			}
		} else if strings.Contains(r.Prompt, "PLANNER-KEPT") {
			t.Errorf("planner lesson reached %s", part)
		}
		wantSecurity := part == "reviewer" && reply == "review-1-1.md"
		if got := strings.Contains(r.Prompt, "SECURITY-LESSON"); got != wantSecurity {
			t.Errorf("security lesson in %s = %v, want %v", reply, got, wantSecurity)
		}
		if wantSecurity {
			seenReview = true
			if !strings.Contains(r.Prompt, reviewerFile) {
				t.Error("security file missing from prompt")
			}
		}
		if strings.Contains(r.Prompt, "UNCOMMITTED-LESSON") {
			t.Error("read working file")
		}
	}
	if !seenPlanner || !seenReview {
		t.Fatalf("missing lessons: planner %v, reviewer %v (%s, %s)", seenPlanner, seenReview, plannerFile, reviewerFile)
	}
}

func TestAnEnsembleFileRunBriefsEachChairWithItsSectionChairFile(t *testing.T) {
	fake := &runCAO{}
	wt, args := runSessionArgs(t, fake)
	writeFile(t, filepath.Join(loop.Home(), ensemble.UserDir, "thorough.toml"), recordedEnsemble)
	commitChairLessons(t, wt, map[string]string{"leads.planner.md": "PLANNER-KEPT\nPLANNER-CUT\n", "review.security.md": "SECURITY-LESSON\n"})
	writeFile(t, filepath.Join(wt, loop.ConfigFile), "[test]\nverify = 'true'\n[run]\npreflight_timeout = '0'\nreply_grace = '1ms'\nlearned_budget = 16\n[sizing]\nmode = 'off'\n")
	if out, err := captureStdout(t, func() error { return runCommand(append(args[:6:6], "-ensemble", "thorough")) }); err != nil {
		t.Fatalf("run: %v\n%s", err, out)
	}
	requireChairLessonRequests(t, fake.fake.requests, "leads.planner.md", "review.security.md")
	requireLessonRecordAddress(t, "leads.planner@thorough")
	requireLessonRecordAddress(t, "review.security@thorough")
}

func TestPlayBriefsStandingChairsWithLessonsFromTheBaseCommit(t *testing.T) {
	s := playStanding(t)
	planner, reviewer := s.Chairs[0], s.Chairs[2]
	plannerFile, reviewerFile := planner.Section+"."+planner.Name+".md", reviewer.Section+"."+reviewer.Name+".md"
	commitChairLessons(t, s.Worktree, map[string]string{plannerFile: "PLANNER-KEPT\nPLANNER-CUT\n", reviewerFile: "SECURITY-LESSON\n"})
	writeFile(t, filepath.Join(s.Worktree, ".kapellmeister/learned", plannerFile), "UNCOMMITTED-LESSON\n")
	writeFile(t, filepath.Join(s.Worktree, loop.ConfigFile), "[test]\nverify = 'true'\n[run]\nreply_grace = '1ms'\nlearned_budget = 16\n")
	fake := &playCAO{}
	srv := fake.serve(t)
	out, err := capturePlay(t, []string{s.Session, "-cao", srv.URL})
	if err != nil {
		t.Fatalf("play: %v\n%s", err, out)
	}
	requireChairLessonRequests(t, fake.requests, plannerFile, reviewerFile)
	requireLessonRecordAddress(t, planner.Address)
	requireLessonRecordAddress(t, reviewer.Address)
	requirePlayStillStanding(t, s, fake, out)
}

func TestConfigShowsTheLearnedBudgetInTheRunSettings(t *testing.T) {
	t.Setenv(loop.HomeEnv, t.TempDir())
	t.Setenv(loop.StateEnv, t.TempDir())
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, loop.ConfigFile), "[run]\nlearned_budget = 123\n")
	srv := permissionCAO(t, map[string]cao.ProfilePermissions{}, nil)
	out, err := captureStdout(t, func() error { return configCommand([]string{"-worktree", repo, "-cao", srv.URL}) })
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "run    ") && strings.Contains(line, "learned budget 123 bytes") {
			found = true
		}
	}
	if !found {
		t.Errorf("budget missing from run settings: %s", out)
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

func TestTheConductorsGitOutputIgnoresWarningsOnStderr(t *testing.T) {
	dir := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"config", "user.name", "Test"}, {"config", "user.email", "test@example.invalid"}} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	commitChairLessons(t, dir, map[string]string{"planner.md": "Reviewed\n"})
	want, err := gitOutput(dir, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	gitWithStderrWarning(t)
	got, err := gitOutput(dir, "rev-parse", "HEAD")
	if err != nil || got != want {
		t.Errorf("stdout = %q, %v; want %q", got, err, want)
	}
	if _, err := gitOutput(dir, "not-a-git-command"); err == nil || !strings.Contains(err.Error(), "warning: noisy") || !strings.Contains(err.Error(), "not-a-git-command") {
		t.Errorf("stderr missing from error: %v", err)
	}
}
