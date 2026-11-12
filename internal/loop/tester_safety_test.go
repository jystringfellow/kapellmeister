package loop

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestTheTestAndTestPlanPromptsNameTheAbsoluteScratchDirectoryAsTheOnlyScratchPlace(t *testing.T) {
	e, f := testerEngine(t)
	stateDir := t.TempDir()
	e.Config.ScratchDir = filepath.Join(stateDir, "scratch", e.Config.RunID)
	run(t, e)
	for _, name := range []string{"test-plan", "test"} {
		p := testerSteps(f, name)[0].Prompt
		want := "Everything you create stays under " + e.Config.ScratchDir + "."
		if !filepath.IsAbs(e.Config.ScratchDir) || !strings.Contains(p, want) {
			t.Errorf("%s prompt lacks scratch rule %q: %s", name, want, p)
		}
		if strings.Contains(p, filepath.Join(stateDir, "runs", e.Config.RunID, "scratch")) {
			t.Errorf("%s prompt names scratch in the run records: %s", name, p)
		}
		for _, forbidden := range []string{"scratch repositories", "server state", "including processes and sessions"} {
			if strings.Contains(p, forbidden) {
				t.Errorf("%s contains %q", name, forbidden)
			}
		}
		if name == "test" && !strings.Contains(p, "Before finishing, clean up anything you started. Touch nothing you did not create.") {
			t.Error("tester cleanup rule missing")
		}
		if name == "test" && !strings.Contains(p, filepath.Join(e.Config.ScratchDir, "pids")) {
			t.Errorf("test prompt lacks scratch pids path: %s", p)
		}
		for _, line := range strings.Split(p, "\n") {
			if strings.Contains(line, "scratch") && strings.Contains(line, RunDirRel(e.Config.RunID)) {
				t.Errorf("worktree scratch: %s", line)
			}
		}
	}
	for _, name := range []string{"plan", "implement", "review", "gate"} {
		for _, s := range testerSteps(f, name) {
			if strings.Contains(s.Prompt, e.Config.ScratchDir) {
				t.Errorf("%s mentions scratch", name)
			}
		}
	}
}

func TestTheTestPromptForbidsWritingGitCommandsOutsideARepositoryTheTesterCreatedInScratch(t *testing.T) {
	e, f := testerEngine(t)
	run(t, e)
	p := testerSteps(f, "test")[0].Prompt
	for _, want := range []string{"Never run a git command that writes", "init", "add", "commit", "checkout", "reset", "stash", "except a repository you created inside " + e.Config.ScratchDir, "git -C <that repository>"} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt lacks %q: %s", want, p)
		}
	}
}

func TestTheTestPromptAsksForEveryLongRunningPidInScratchPidsAndForThemToBeStopped(t *testing.T) {
	e, f := testerEngine(t)
	run(t, e)
	p := testerSteps(f, "test")[0].Prompt
	for _, want := range []string{"Append the PID of every long-running process you start to " + filepath.Join(e.Config.ScratchDir, "pids"), "one per line", "stop them all before finishing"} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt lacks %q: %s", want, p)
		}
	}
}

func TestLiveProcessesAreReportedAfterEveryTestStepIncludingFailureTimeoutInterruptAndTreeChangesWithoutStoppingThem(t *testing.T) {
	if _, err := exec.Command("ps", "-o", "command=", "-p", fmt.Sprint(os.Getpid())).Output(); err != nil {
		t.Skipf("ps is unavailable in this environment: %v", err)
	}
	for _, outcome := range []string{"passed", "failed", "timeout", "interrupted", "tree changed", "two rounds"} {
		t.Run(outcome, func(t *testing.T) {
			e, f := testerEngine(t)
			var progress bytes.Buffer
			e.Progress = &progress
			var processes []*exec.Cmd
			var contents string
			for range 2 {
				process := exec.Command("sleep", "60")
				if err := process.Start(); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = process.Process.Kill(); _ = process.Wait() })
				processes = append(processes, process)
				contents += fmt.Sprintf("%d\n", process.Process.Pid)
			}
			f.actions["test"] = func(s Step) {
				if err := os.WriteFile(filepath.Join(e.Config.ScratchDir, "pids"), []byte(contents), 0600); err != nil {
					t.Error(err)
				}
				if outcome == "tree changed" {
					f.write("extra.txt", "changed")
				}
				f.write(s.ReplyPath, "test reply")
			}
			f.errFor = func(s Step) error {
				if s.Name != "test" {
					return nil
				}
				switch outcome {
				case "failed":
					return errors.New("failed test")
				case "timeout":
					return context.DeadlineExceeded
				case "interrupted":
					return context.Canceled
				}
				return nil
			}
			if outcome == "two rounds" {
				f.verdicts["test"] = []string{"CHANGES", "APPROVE"}
			}
			res := run(t, e)
			rounds := 1
			if outcome == "two rounds" {
				rounds = 2
			}
			wantOutcome := Failed
			if outcome == "passed" || outcome == "two rounds" {
				wantOutcome = Approved
			}
			if outcome == "timeout" {
				wantOutcome = Untested
			}
			if res.Outcome != wantOutcome {
				t.Fatalf("result: %+v", res)
			}
			recs := recordsOf(t, e.Records.(*bytes.Buffer), "left-running")
			if len(recs) != rounds*len(processes) {
				t.Errorf("left-running records = %d, want %d", len(recs), rounds*len(processes))
			}
			for _, process := range processes {
				pid := process.Process.Pid
				cmd, err := exec.Command("ps", "-o", "command=", "-p", fmt.Sprint(pid)).Output()
				if err != nil {
					t.Fatalf("process was stopped: %v", err)
				}
				command := strings.TrimSpace(string(cmd))
				line := fmt.Sprintf("  left running: pid %d (%s)\n", pid, command)
				if n := strings.Count(progress.String(), line); n != rounds {
					t.Errorf("line occurs %d times, want %d: %s", n, rounds, progress.String())
				}
				for round := 1; round <= rounds; round++ {
					count := 0
					for _, rec := range recs {
						if rec.PID == pid && rec.Command == command && rec.Round == round && rec.RunID == e.Config.RunID {
							count++
						}
					}
					if count != 1 {
						t.Errorf("missing telemetry for pid %d round %d: %+v", pid, round, recs)
					}
				}
			}
			data, err := os.ReadFile(filepath.Join(e.Config.ScratchDir, "pids"))
			if err != nil || string(data) != contents {
				t.Fatalf("pids changed: %q %v", data, err)
			}
		})
	}
}

func TestDeadMalformedAndBlankPidsAndAMissingPidsFileReportNothing(t *testing.T) {
	for _, missing := range []bool{false, true} {
		t.Run(fmt.Sprint(missing), func(t *testing.T) {
			e, _ := testerEngine(t)
			var progress bytes.Buffer
			e.Progress = &progress
			if !missing {
				dead := exec.Command("true")
				if err := dead.Run(); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(e.Config.ScratchDir, "pids"), []byte(fmt.Sprintf("\nabc\n0\n-5\n%d\n", dead.Process.Pid)), 0600); err != nil {
					t.Fatal(err)
				}
			}
			run(t, e)
			if strings.Contains(progress.String(), "left running") || len(recordsOf(t, e.Records.(*bytes.Buffer), "left-running")) != 0 {
				t.Fatalf("unexpected report: %s\n%s", &progress, e.Records)
			}
		})
	}
}

func TestAMissingPsSilentlySkipsLeftRunningProcesses(t *testing.T) {
	scratch := t.TempDir()
	if err := os.WriteFile(filepath.Join(scratch, "pids"), []byte(fmt.Sprintf("%d\n", os.Getpid())), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", t.TempDir())
	if processes := leftRunning(scratch); len(processes) != 0 {
		t.Fatalf("processes = %+v", processes)
	}
}

func TestDuplicatePidsAreReportedOnceAfterEachTestStep(t *testing.T) {
	bin := t.TempDir()
	ps := filepath.Join(bin, "ps")
	if err := os.WriteFile(ps, []byte("#!/bin/sh\n[ \"$1 $2 $3 $4\" = '-o command= -p 123' ] || exit 1\necho 'sleep 60'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	originalPath := os.Getenv("PATH")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+originalPath)
	e, f := testerEngine(t)
	var progress bytes.Buffer
	e.Progress = &progress
	f.actions["test"] = func(s Step) {
		if err := os.WriteFile(filepath.Join(e.Config.ScratchDir, "pids"), []byte("123\n 123 \n00123\n"), 0600); err != nil {
			t.Error(err)
		}
		f.write(s.ReplyPath, "test reply")
	}
	f.verdicts["test"] = []string{"CHANGES", "APPROVE"}
	if res := run(t, e); res.Outcome != Approved || res.Rounds != 2 {
		t.Fatalf("result: %+v", res)
	}
	if got := strings.Count(progress.String(), "  left running: pid 123 (sleep 60)\n"); got != 2 {
		t.Errorf("left-running lines = %d, want 2: %s", got, &progress)
	}
	recs := recordsOf(t, e.Records.(*bytes.Buffer), "left-running")
	if len(recs) != 2 {
		t.Fatalf("records: %+v", recs)
	}
	for i, rec := range recs {
		if rec.PID != 123 || rec.Command != "sleep 60" || rec.Round != i+1 {
			t.Errorf("record: %+v", rec)
		}
	}
}

func TestAFifoPidsFileDoesNotBlockTheScoreOrProduceLeftRunningReports(t *testing.T) {
	for _, symlink := range []bool{false, true} {
		t.Run(fmt.Sprintf("symlink=%t", symlink), func(t *testing.T) {
			e, f := testerEngine(t)
			var progress bytes.Buffer
			e.Progress = &progress
			fifo := filepath.Join(e.Config.ScratchDir, "pids")
			if symlink {
				fifo = filepath.Join(e.Config.ScratchDir, "fifo")
			}
			if err := syscall.Mkfifo(fifo, 0600); err != nil {
				t.Fatal(err)
			}
			if symlink {
				if err := os.Symlink(fifo, filepath.Join(e.Config.ScratchDir, "pids")); err != nil {
					t.Fatal(err)
				}
			}
			testFinished := make(chan struct{})
			f.actions["test"] = func(s Step) {
				f.write(s.ReplyPath, "test reply")
				close(testFinished)
			}
			done := make(chan struct{})
			var result Result
			var runErr error
			go func() {
				result, runErr = e.Run(context.Background())
				close(done)
			}()
			<-testFinished
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				writer, err := os.OpenFile(fifo, os.O_RDWR|syscall.O_NONBLOCK, 0)
				if err != nil {
					t.Fatal(err)
				}
				writer.Close()
				select {
				case <-done:
				case <-time.After(5 * time.Second):
					t.Fatal("score stayed blocked after releasing the FIFO")
				}
				t.Fatal("score blocked reading a FIFO pids file after the test step finished")
			}
			if runErr != nil || result.Outcome != Approved {
				t.Fatalf("result: %+v, error: %v", result, runErr)
			}
			if strings.Contains(progress.String(), "left running") || len(recordsOf(t, e.Records.(*bytes.Buffer), "left-running")) != 0 {
				t.Fatalf("unexpected report: %s\n%s", &progress, e.Records)
			}
		})
	}
}

func TestPidsFilesLargerThanOneMiBAreSkipped(t *testing.T) {
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "ps"), []byte("#!/bin/sh\necho 'sleep 60'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	for _, size := range []int{1 << 20, (1 << 20) + 1} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			scratch := t.TempDir()
			data := "123\n" + strings.Repeat("\n", size-4)
			if err := os.WriteFile(filepath.Join(scratch, "pids"), []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			processes := leftRunning(scratch)
			if size > 1<<20 {
				if len(processes) != 0 {
					t.Fatalf("oversized pids file produced reports: %+v", processes)
				}
			} else if len(processes) != 1 || processes[0].PID != 123 || processes[0].Command != "sleep 60" {
				t.Fatalf("pids file at the size limit was not reported: %+v", processes)
			}
		})
	}
}

func TestTheTesterBriefingGivesTheConductorsPidOrchestratorURLAndStateDirectory(t *testing.T) {
	briefing := testerProcessBriefing(1234, "/s", "http://127.0.0.1:19889", "/conductor/state")
	for _, want := range []string{"process 1234", "conducting this run", "http://127.0.0.1:19889", "/conductor/state", "belong to that conductor", "none of them is part of what you are testing", "none may be signalled, stopped or changed", "signal only processes you started yourself and listed in /s/pids"} {
		if !strings.Contains(briefing, want) {
			t.Errorf("briefing lacks %q: %s", want, briefing)
		}
	}
	withoutURL := strings.ReplaceAll(briefing, "http://127.0.0.1:19889", "orchestrator")
	if lines := strings.Split(strings.TrimSpace(withoutURL), "\n"); len(lines) != 2 || strings.Count(withoutURL, ".") != 2 {
		t.Errorf("briefing must contain at most two sentences: %s", briefing)
	}
	e, f := testerEngine(t)
	e.Config.CAOURL = "http://127.0.0.1:19889"
	e.Config.StateDir = "/conductor/state"
	e.Config.Lessons = true
	run(t, e)
	for _, step := range f.steps {
		for _, value := range []string{fmt.Sprintf("process %d", os.Getpid()), e.Config.CAOURL, e.Config.StateDir, filepath.Join(e.Config.ScratchDir, "pids")} {
			if step.Name == "test-plan" || step.Name == "test" {
				if !strings.Contains(step.Prompt, value) {
					t.Errorf("%s lacks %q", step.Name, value)
				}
			} else if strings.Contains(step.Prompt, value) {
				t.Errorf("%s names tester briefing fact %q", step.Name, value)
			}
		}
	}
}

func TestTheTesterBriefingNamesNoToolsOrFileToSource(t *testing.T) {
	briefing := testerProcessBriefing(1234, "/s", "http://127.0.0.1:19889", "/conductor/state")
	for _, pattern := range []string{`(?i)\bkm\b`, `(?i)\bcao\b`, `(?i)cao-server|tmux|pgrep|pkill|killall|ps \| grep|source|sourcing|env\.sh|sh -c`} {
		if regexp.MustCompile(pattern).MatchString(briefing) {
			t.Errorf("briefing matches forbidden tool or setup instruction %q: %s", pattern, briefing)
		}
	}
}

func TestNoPromptKmGivesAChairNamesCaoServerTmuxPgrepPkillOrKillall(t *testing.T) {
	e, f := testerEngine(t)
	scratch, err := os.MkdirTemp("", "briefing-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(scratch) })
	e.Config.ScratchDir = scratch
	e.Config.Lessons = true
	e.Config.CAOURL = "http://127.0.0.1:19889"
	e.Config.StateDir = "/conductor/state"
	run(t, e)
	for _, step := range f.steps {
		for _, tool := range []string{"cao-server", "tmux", "pgrep", "pkill", "killall"} {
			if strings.Contains(strings.ToLower(step.Prompt), tool) {
				t.Errorf("%s prompt names %s", step.Name, tool)
			}
		}
	}
}
