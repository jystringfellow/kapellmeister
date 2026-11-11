package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/jystringfellow/kapellmeister/internal/ensemble"
	"github.com/jystringfellow/kapellmeister/internal/loop"
)

func safetyStanding(t *testing.T) ensemble.Standing {
	t.Helper()
	s := playStanding(t)
	s.Chairs = append(s.Chairs, ensemble.StandingChair{Address: "leads.tonmeister@score", Section: "leads", Name: "tonmeister", Part: "tester", TerminalID: "terminal-tonmeister", Briefed: true, Binding: loop.Chair{Provider: "p", Profile: "tester"}})
	savePlayStanding(t, s)
	return s
}

func onlyRunResult(t *testing.T) (string, map[string]any) {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(loop.StateDir(), "runs", "*", "result.json"))
	if err != nil || len(paths) != 1 {
		t.Fatalf("result paths = %v: %v", paths, err)
	}
	data, err := os.ReadFile(paths[0])
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	return filepath.Dir(paths[0]), result
}

func safetyGit(t *testing.T, worktree string, args ...string) string {
	t.Helper()
	data, err := exec.Command("git", append([]string{"-C", worktree}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, data)
	}
	return strings.TrimSpace(string(data))
}

func TestRunAndPlayCreateTheScratchDirectoryUnderStateScratchOutsideTheRunRecords(t *testing.T) {
	for _, command := range []string{"run", "play"} {
		t.Run(command, func(t *testing.T) {
			var wt string
			var fake *playCAO
			var runErr error
			var out string
			if command == "run" {
				f := &runCAO{}
				out, _ = captureStdout(t, func() error { wt, runErr = runSessionFixture(t, f); return runErr })
				fake = &f.fake
			} else {
				s := safetyStanding(t)
				wt = s.Worktree
				fake = &playCAO{}
				out, runErr = capturePlay(t, []string{s.Session, "-cao", fake.serve(t).URL})
			}
			if runErr != nil {
				t.Fatal(runErr)
			}
			if strings.Contains(out, "HEAD moved:") {
				t.Errorf("unchanged HEAD reported moved: %s", out)
			}
			dir, result := onlyRunResult(t)
			scratch := filepath.Join(loop.StateDir(), "scratch", filepath.Base(dir))
			info, err := os.Stat(scratch)
			if err != nil || !info.IsDir() {
				t.Fatalf("scratch missing: %v", err)
			}
			if _, err := os.Stat(filepath.Join(dir, "scratch")); !os.IsNotExist(err) {
				t.Errorf("run records contain scratch: %v", err)
			}
			entries, err := os.ReadDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			var names []string
			for _, entry := range entries {
				names = append(names, entry.Name())
			}
			for _, record := range []string{"config.json", "replies", "result.json", "telemetry.jsonl"} {
				if !slices.Contains(names, record) {
					t.Errorf("run records = %s, missing %s", strings.Join(names, ","), record)
				}
			}
			if !filepath.IsAbs(scratch) || strings.HasPrefix(scratch, wt+string(os.PathSeparator)) {
				t.Fatalf("scratch is not external and absolute: %s", scratch)
			}
			if result["base"] != safetyGit(t, wt, "rev-parse", "HEAD") {
				t.Errorf("base = %v", result["base"])
			}
			if _, present := result["head"]; present {
				t.Errorf("unchanged HEAD recorded: %v", result)
			}
			if _, present := result["head_moved"]; present {
				t.Errorf("unchanged HEAD marked moved: %v", result)
			}
			data, err := os.ReadFile(filepath.Join(dir, "config.json"))
			if err != nil {
				t.Fatal(err)
			}
			var cfg loop.Config
			if err := json.Unmarshal(data, &cfg); err != nil {
				t.Fatal(err)
			}
			if cfg.ScratchDir != scratch {
				t.Errorf("config scratch = %s, want %s", cfg.ScratchDir, scratch)
			}
			found := 0
			for i, name := range fake.replies {
				if strings.Contains(fake.requests[i].Prompt, filepath.Join(loop.StateDir(), "runs")+string(os.PathSeparator)) {
					t.Errorf("%s prompt names the run records: %s", name, fake.requests[i].Prompt)
				}
				if name == "test-1.md" && !strings.Contains(fake.requests[i].Prompt, filepath.Join(scratch, "pids")) {
					t.Errorf("test prompt lacks scratch pids path: %s", fake.requests[i].Prompt)
				}
				if name == "test-plan.md" || name == "test-1.md" {
					found++
					if !strings.Contains(fake.requests[i].Prompt, scratch) {
						t.Errorf("%s lacks scratch: %s", name, fake.requests[i].Prompt)
					}
				}
			}
			if found != 2 {
				t.Fatalf("tester replies: %v", fake.replies)
			}
		})
	}
}

func TestRunAndPlayRefuseAStateDirInsideAGitRepositoryNamingBothPathsBeforeAnyCAOCall(t *testing.T) {
	for _, command := range []string{"run", "play"} {
		t.Run(command, func(t *testing.T) {
			f := &runCAO{}
			var wt string
			var args []string
			if command == "run" {
				wt, args = runSessionArgs(t, f)
			} else {
				s := playStanding(t)
				wt = s.Worktree
				args = []string{s.Session, "-cao", f.serve(t).URL}
				t.Setenv(loop.StateEnv, filepath.Join(wt, "state"))
				savePlayStanding(t, s)
			}
			t.Setenv(loop.StateEnv, filepath.Join(wt, "state"))
			out, err := captureStdout(t, func() error {
				if command == "run" {
					return runCommand(args)
				}
				return playCommand(args)
			})
			top := safetyGit(t, wt, "rev-parse", "--show-toplevel")
			if err == nil || !strings.Contains(err.Error(), top) || !strings.Contains(err.Error(), filepath.Join(loop.StateDir(), "scratch")+string(os.PathSeparator)) {
				t.Fatalf("refusal: %v\n%s", err, out)
			}
			if len(f.created) != 0 || len(f.fake.requests) != 0 || len(f.fake.checked) != 0 || len(f.fake.unexpected) != 0 {
				t.Errorf("CAO called before refusal: %+v", f)
			}
			for _, parent := range []string{"runs", "scratch"} {
				dirs, globErr := filepath.Glob(filepath.Join(loop.StateDir(), parent, "*"))
				if globErr != nil || len(dirs) != 0 {
					t.Errorf("refused run left %s directories: %v %v", parent, dirs, globErr)
				}
			}
		})
	}
}

func TestACommitDuringTheRunKeepsCommittedAndUntrackedChangesVisibleAndReportsTheMovedHead(t *testing.T) {
	for _, step := range []string{"implement-1.md", "test-1.md"} {
		t.Run(step, func(t *testing.T) {
			s := safetyStanding(t)
			base := safetyGit(t, s.Worktree, "rev-parse", "HEAD")
			fake := &playCAO{afterReply: func(wt, name string) {
				if name != step {
					return
				}
				writeFile(t, filepath.Join(wt, "new.go"), "package example\n")
				safetyGit(t, wt, "add", "new.go")
				safetyGit(t, wt, "-c", "commit.gpgsign=false", "commit", "-qm", "test created commit")
				writeFile(t, filepath.Join(wt, "extra.txt"), "untracked\n")
			}}
			out, runErr := capturePlay(t, []string{s.Session, "-cao", fake.serve(t).URL})
			wantOutcome := loop.Approved
			if step == "test-1.md" {
				wantOutcome = loop.Failed
				if runErr == nil {
					t.Error("modified worktree must fail the run")
				}
			} else if runErr != nil {
				t.Fatal(runErr)
			}
			_, result := onlyRunResult(t)
			head := safetyGit(t, s.Worktree, "rev-parse", "HEAD")
			if result["base"] != base || result["head"] != head || result["head_moved"] != true {
				t.Errorf("commit result: %v", result)
			}
			if result["outcome"] != wantOutcome || (wantOutcome == loop.Failed && !strings.Contains(fmt.Sprint(result["reason"]), "modified the worktree")) {
				t.Errorf("tree check changed: %v", result)
			}
			for _, file := range []string{"new.go", "extra.txt"} {
				if !strings.Contains(fmt.Sprint(result["diff_stat"]), file) {
					t.Errorf("missing %s in diff_stat: %v", file, result["diff_stat"])
				}
			}
			line := fmt.Sprintf("  HEAD moved: %s → %s %q — the run's changes are in that commit\n", safetyGit(t, s.Worktree, "rev-parse", "--short", base), safetyGit(t, s.Worktree, "rev-parse", "--short", head), "test created commit")
			if strings.Count(out, line) != 1 {
				t.Errorf("missing HEAD moved line %q: %s", line, out)
			}
			if safetyGit(t, s.Worktree, "rev-parse", "HEAD") != head {
				t.Error("HEAD was rewritten")
			}

		})
	}
}

func TestRunAndPlayExitZeroOnlyWhenApprovedAndUsageErrorsExitTwo(t *testing.T) {
	for _, command := range []string{"run", "play"} {
		for _, outcome := range []string{"approved", "failed", "rounds_exhausted", "interrupted"} {
			t.Run(command+"/"+outcome, func(t *testing.T) {
				f := &runCAO{interrupt: outcome == "interrupted"}
				if outcome == "failed" {
					f.fake.failPart = "planner"
				}
				f.fake.changes = outcome == "rounds_exhausted"
				var args []string
				var wt string
				if command == "run" {
					wt, args = runSessionArgs(t, f)
				} else {
					s := playStanding(t)
					wt = s.Worktree
					args = []string{s.Session, "-cao", f.serve(t).URL}
				}
				writeFile(t, filepath.Join(wt, "visible.txt"), "keep this change visible\n")
				var stderr bytes.Buffer
				var code int
				out, _ := captureStdout(t, func() error { code = execute(append([]string{command}, args...), &stderr); return nil })
				want := 1
				if outcome == "approved" {
					want = 0
				}
				if code != want {
					t.Errorf("exit = %d, want %d: %s\n%s", code, want, out, &stderr)
				}
				printedOutcome := outcome
				if outcome == "interrupted" {
					printedOutcome = "failed"
				}
				if !strings.Contains(out, printedOutcome+" after ") {
					t.Errorf("missing outcome: %s", out)
				}
				if !strings.Contains(out, "visible.txt") {
					t.Errorf("missing diff stat: %s", out)
				}
				if stderr.Len() != 0 {
					t.Errorf("unexpected diagnostic: %s", &stderr)
				}
			})
		}
	}
	for _, args := range [][]string{nil, {"bogus"}} {
		var stderr bytes.Buffer
		if code := execute(args, &stderr); code != 2 || !strings.Contains(stderr.String(), "usage:") {
			t.Errorf("usage %v: exit %d, %s", args, code, &stderr)
		}
	}
}

func TestScratchPathsAreAbsoluteEvenWhenTheStatePathIsRelative(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	stateDir := t.TempDir()
	relative, err := filepath.Rel(cwd, stateDir)
	if err != nil {
		t.Fatal(err)
	}
	scratch, err := prepareScratch(relative, "relative")
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(scratch) || scratch != filepath.Join(stateDir, "scratch", "relative") {
		t.Fatalf("scratch = %q", scratch)
	}
	if info, err := os.Stat(scratch); err != nil || !info.IsDir() {
		t.Fatalf("scratch missing: %v", err)
	}
}

func TestReportingTheRunDiffLeavesTheWorktreeIndexUntouched(t *testing.T) {
	f := &runCAO{}
	f.fake.failPart = "planner"
	wt, args := runSessionArgs(t, f)
	writeFile(t, filepath.Join(wt, "tracked.txt"), "staged version\n")
	safetyGit(t, wt, "add", "tracked.txt")
	writeFile(t, filepath.Join(wt, "tracked.txt"), "working version\n")
	writeFile(t, filepath.Join(wt, "untracked.txt"), "untracked version\n")
	writeFile(t, filepath.Join(wt, ".kapellmeister", "forced.txt"), "tracked despite ignore rule\n")
	safetyGit(t, wt, "add", "--force", ".kapellmeister/forced.txt")
	indexPath := safetyGit(t, wt, "rev-parse", "--path-format=absolute", "--git-path", "index")
	before, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	_, runErr := captureStdout(t, func() error { return runCommand(args) })
	if runErr == nil {
		t.Fatal("planner failure must fail the run")
	}
	after, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Error("reporting modified the worktree index")
	}
	_, result := onlyRunResult(t)
	for _, name := range []string{"tracked.txt", "untracked.txt", ".kapellmeister/forced.txt"} {
		if !strings.Contains(fmt.Sprint(result["diff_stat"]), name) {
			t.Errorf("diff_stat lacks %s: %v", name, result["diff_stat"])
		}
	}
	if staged := safetyGit(t, wt, "show", ":tracked.txt"); staged != "staged version" {
		t.Errorf("staged contents changed: %q", staged)
	}
}

func TestPlayValidationFailuresLeaveNoScratchDirectory(t *testing.T) {
	for _, failure := range []string{"unknown session", "missing standing file", "unbriefed chair", "invalid chairs", "invalid settings"} {
		t.Run(failure, func(t *testing.T) {
			s := playStanding(t)
			session := s.Session
			switch failure {
			case "unknown session":
				session = "unknown"
			case "missing standing file":
				if err := os.Remove(filepath.Join(loop.StateDir(), "ensembles", s.Session+".json")); err != nil {
					t.Fatal(err)
				}
			case "unbriefed chair":
				s.Chairs[0].Briefed = false
				savePlayStanding(t, s)
			case "invalid chairs":
				s.Chairs = nil
				savePlayStanding(t, s)
			case "invalid settings":
				writeFile(t, filepath.Join(s.Worktree, loop.ConfigFile), "[invalid syntax\n")
			}
			fake := &playCAO{}
			_, err := capturePlay(t, []string{session, "-cao", fake.serve(t).URL})
			if err == nil {
				t.Fatal("invalid standing session was accepted")
			}
			for _, parent := range []string{"runs", "scratch"} {
				if _, err := os.Stat(filepath.Join(loop.StateDir(), parent)); !os.IsNotExist(err) {
					t.Errorf("validation created %s state: %v", parent, err)
				}
			}
			if len(fake.requests) != 0 || len(fake.checked) != 0 || len(fake.unexpected) != 0 {
				t.Errorf("CAO contacted before validation: %+v", fake)
			}
		})
	}
}

func TestScratchInsideAGitWorktreeIsRemovedEvenWhenFindingTheTopLevelFails(t *testing.T) {
	bin := t.TempDir()
	writeFile(t, filepath.Join(bin, "git"), "#!/bin/sh\ncase \"$4\" in\n--is-inside-work-tree) echo true ;;\n--show-toplevel) echo 'top level unavailable' >&2; exit 1 ;;\n*) exit 2 ;;\nesac\n")
	if err := os.Chmod(filepath.Join(bin, "git"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	stateDir := t.TempDir()
	kept := []string{filepath.Join(stateDir, "runs", "refused", "config.json"), filepath.Join(stateDir, "scratch", "other", "keep.txt")}
	for _, path := range kept {
		writeFile(t, path, "keep\n")
	}
	_, err := prepareScratch(stateDir, "refused")
	if err == nil || !strings.Contains(err.Error(), "top level unavailable") {
		t.Fatalf("error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(stateDir, "scratch", "refused")); !os.IsNotExist(err) {
		t.Errorf("refused scratch directory remains: %v", err)
	}
	for _, path := range kept {
		if data, err := os.ReadFile(path); err != nil || string(data) != "keep\n" {
			t.Errorf("refusal changed %s: %q %v", path, data, err)
		}
	}
}

func TestARunDirectoryWrittenByAnOlderKmStillHoldingScratchStillReads(t *testing.T) {
	t.Setenv(loop.StateEnv, t.TempDir())
	dir := filepath.Join(loop.StateDir(), "runs", "older")
	scratch := filepath.Join(dir, "scratch")
	writeFile(t, filepath.Join(scratch, "fixture.txt"), "retained\n")
	if err := writeJSON(filepath.Join(dir, "config.json"), map[string]string{"RunID": "older", "ScratchDir": scratch}); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(filepath.Join(dir, "result.json"), map[string]any{"run_id": "older", "outcome": loop.Approved}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cfg loop.Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.RunID != "older" || cfg.ScratchDir != scratch {
		t.Errorf("older config = %+v", cfg)
	}
	gotDir, result := onlyRunResult(t)
	if gotDir != dir || result["run_id"] != "older" || result["outcome"] != loop.Approved {
		t.Errorf("older result = %s %v", gotDir, result)
	}
	if data, err := os.ReadFile(filepath.Join(cfg.ScratchDir, "fixture.txt")); err != nil || string(data) != "retained\n" {
		t.Errorf("older scratch = %q %v", data, err)
	}
}

func TestScratchIsLeftInPlaceAfterTheRunEnds(t *testing.T) {
	s := safetyStanding(t)
	fake := &playCAO{afterReply: func(wt, name string) {
		if name != "test-1.md" {
			return
		}
		paths, err := filepath.Glob(filepath.Join(loop.StateDir(), "runs", "*", "config.json"))
		if err != nil || len(paths) != 1 {
			t.Errorf("config paths = %v: %v", paths, err)
			return
		}
		scratch := filepath.Join(loop.StateDir(), "scratch", filepath.Base(filepath.Dir(paths[0])))
		if info, err := os.Stat(scratch); err != nil || !info.IsDir() {
			t.Errorf("scratch missing during the score: %v", err)
			return
		}
		writeFile(t, filepath.Join(scratch, "fixture.txt"), "retained\n")
		writeFile(t, filepath.Join(scratch, "pids"), "not-a-pid\n")
	}}
	if _, err := capturePlay(t, []string{s.Session, "-cao", fake.serve(t).URL}); err != nil {
		t.Fatal(err)
	}
	dir, _ := onlyRunResult(t)
	scratch := filepath.Join(loop.StateDir(), "scratch", filepath.Base(dir))
	for name, want := range map[string]string{"fixture.txt": "retained\n", "pids": "not-a-pid\n"} {
		if data, err := os.ReadFile(filepath.Join(scratch, name)); err != nil || string(data) != want {
			t.Errorf("scratch %s after the run = %q %v", name, data, err)
		}
	}
}
