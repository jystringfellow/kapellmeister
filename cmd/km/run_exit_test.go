package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jystringfellow/kapellmeister/internal/loop"
)

func failedStartedResult(t *testing.T, p ensembleRun, reason string) {
	t.Helper()
	result := recordedResult(t, p.StateDir)
	if result["run_id"] != p.RunID || result["outcome"] != loop.Failed || !strings.Contains(result["reason"].(string), reason) || result["base"] == "" || result["ensemble"] != p.Ensemble {
		t.Fatalf("result=%v", result)
	}
	out, diagnostics := captureReport(t, "-json", p.StateDir)
	var report runReport
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatal(err)
	}
	if diagnostics != "" || len(report.Runs) != 1 || report.Runs[0].Outcome != loop.Failed || report.Runs[0].Started == nil {
		t.Fatalf("report=%s\n%s", out, diagnostics)
	}
}

func startedEnsembleRun(t *testing.T) ensembleRun {
	t.Helper()
	s := playStanding(t)
	return ensembleRun{RunID: "failed", StateDir: filepath.Join(loop.StateDir(), "runs", "failed"), Ensemble: "quick", Stepper: replyingStepper{}, Progress: io.Discard, Config: loop.Config{RunID: "failed", Task: "probe", Worktree: s.Worktree, Chairs: plainChairs(), MaxRounds: 1}}
}

func TestARunWhoseEngineErrorsAfterTelemetryStartsStillWritesAFailedResultWithItsReason(t *testing.T) {
	p := startedEnsembleRun(t)
	writeFile(t, filepath.Join(p.Config.Worktree, ".kapellmeister", "runs"), "blocked")
	_, err := runEnsemble(context.Background(), p)
	if err == nil {
		t.Fatal("run succeeded")
	}
	failedStartedResult(t, p, err.Error())
}

func TestARunWhoseConfigRecordCannotBeWrittenStillWritesAFailedResult(t *testing.T) {
	p := startedEnsembleRun(t)
	if err := os.MkdirAll(filepath.Join(p.StateDir, "config.json"), 0700); err != nil {
		t.Fatal(err)
	}
	_, err := runEnsemble(context.Background(), p)
	if err == nil {
		t.Fatal("run succeeded")
	}
	failedStartedResult(t, p, err.Error())
}

func TestARunWhoseWorktreeGitBreaksDuringTheRunStillWritesAFailedResult(t *testing.T) {
	p := startedEnsembleRun(t)
	p.Config.VerifyCommand = "probe"
	p.Verify = func(context.Context, string) (loop.VerifyResult, error) {
		if err := os.RemoveAll(filepath.Join(p.Config.Worktree, ".git")); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command("git", "-C", p.Config.Worktree, "rev-parse", "HEAD").CombinedOutput(); err == nil {
			t.Fatalf("real git unexpectedly succeeded after .git removal: %s", out)
		}
		return loop.VerifyResult{ExitCode: 1}, nil
	}
	_, err := runEnsemble(context.Background(), p)
	if err == nil || !strings.Contains(err.Error(), "rev-parse") {
		t.Fatalf("err=%v", err)
	}
	failedStartedResult(t, p, err.Error())
}

func TestARunThatPanicsAfterTelemetryStartsWritesAFailedResultAndPanicsAgain(t *testing.T) {
	p := startedEnsembleRun(t)
	p.Config.VerifyCommand = "probe"
	p.Verify = func(context.Context, string) (loop.VerifyResult, error) { panic("broken verifier") }
	var caught any
	func() { defer func() { caught = recover() }(); runEnsemble(context.Background(), p) }()
	if caught != "broken verifier" {
		t.Fatalf("panic=%v", caught)
	}
	failedStartedResult(t, p, "panic: broken verifier")
}

func TestPlayAndRunBothLeaveAResultWhenTheRunFailsAfterItStarted(t *testing.T) {
	for _, command := range []string{"play", "run"} {
		t.Run(command, func(t *testing.T) {
			breakGit := func(wt, name string) {
				if name == "plan.md" {
					if err := os.RemoveAll(filepath.Join(wt, ".git")); err != nil {
						t.Error(err)
					}
				}
			}
			var err error
			if command == "play" {
				s := playStanding(t)
				f := &playCAO{afterReply: breakGit}
				_, err = capturePlay(t, []string{s.Session, "-cao", f.serve(t).URL})
			} else {
				f := &runCAO{fake: playCAO{afterReply: breakGit}}
				_, args := runSessionArgs(t, f)
				_, err = captureStdout(t, func() error { return runCommand(args) })
			}
			if err == nil {
				t.Fatal("command succeeded")
			}
			result := recordedResult(t, onlyRunRecord(t))
			if result["outcome"] != loop.Failed || !strings.Contains(result["reason"].(string), "rev-parse") {
				t.Fatal(result)
			}
			rows := reportRows(t, filepath.Join(loop.StateDir(), "runs"))
			if len(rows) != 1 || rows[0].(map[string]any)["outcome"] != loop.Failed {
				t.Fatal(rows)
			}
		})
	}
}

func TestARunWhoseDiffStatFailsStillWritesAFailedResult(t *testing.T) {
	p := startedEnsembleRun(t)
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	bin := recordRoot(t)
	writeFile(t, filepath.Join(bin, "git"), "#!/bin/sh\nfor arg in \"$@\"; do\n if [ \"$arg\" = '--stat' ]; then echo 'diff stat refused' >&2; exit 1; fi\ndone\nexec '"+strings.ReplaceAll(git, "'", "'\\''")+"' \"$@\"\n")
	if err := os.Chmod(filepath.Join(bin, "git"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	_, err = runEnsemble(context.Background(), p)
	if err == nil || !strings.Contains(err.Error(), "diff stat refused") {
		t.Fatalf("err=%v", err)
	}
	failedStartedResult(t, p, err.Error())
}
