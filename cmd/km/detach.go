package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/jystringfellow/kapellmeister/internal/loop"
)

type detachedLaunchRecord struct {
	RunID           string `json:"run_id"`
	Worktree        string `json:"worktree"`
	BaseBranch      string `json:"base_branch"`
	CreatedWorktree string `json:"created_worktree"`
	Task            string `json:"task"`
}

var detachedLaunchTimeout = 30 * time.Second

func startDetached(ctx context.Context, args []string, dir string, launch detachedLaunchRecord, opening []byte, stdout io.Writer) error {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	path := filepath.Join(dir, "launch.json")
	if err := writeJSON(path, launch); err != nil {
		return err
	}
	logPath := filepath.Join(dir, "output.log")
	log, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return err
	}
	defer log.Close()
	if _, err := log.Write(opening); err != nil {
		return err
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(executable, append([]string{"run", "-detached-launch", path}, args...)...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Stdout, cmd.Stderr = log, log
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	deadline := time.NewTimer(detachedLaunchTimeout)
	defer deadline.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	ready := func() bool { _, err := os.Stat(filepath.Join(dir, "current.json")); return err == nil }
	handle := func() error {
		_, err := fmt.Fprintf(stdout, "run       %s\npid       %d\nworktree  %s\noutput    %s\n", launch.RunID, cmd.Process.Pid, launch.Worktree, logPath)
		return err
	}
	for {
		if ready() {
			return handle()
		}
		select {
		case err := <-done:
			if ready() {
				return handle()
			}
			data, _ := os.ReadFile(logPath)
			if len(data) > 65536 {
				data = data[len(data)-65536:]
			}
			return fmt.Errorf("detached run exited before recording its state: %v\n%s", err, data)
		case <-ctx.Done():
			_ = cmd.Process.Signal(syscall.SIGTERM)
			return ctx.Err()
		case <-deadline.C:
			_ = cmd.Process.Signal(syscall.SIGTERM)
			return fmt.Errorf("detached run did not record its state within %s; output: %s", detachedLaunchTimeout, logPath)
		case <-tick.C:
		}
	}
}

func recordDetachedFailure(ctx context.Context, dir string, launch detachedLaunchRecord, state runState, runErr error) {
	if runErr == nil || errors.Is(runErr, errNotApproved) {
		return
	}
	if _, err := os.Stat(filepath.Join(dir, "result.json")); err == nil {
		return
	}
	reason := runErr.Error()
	if sig := loop.SignalOf(ctx); sig != nil {
		reason = sig.Error()
	}
	base, _ := gitOutput(launch.Worktree, "rev-parse", "HEAD")
	sum := ensembleSummary{Result: loop.Result{RunID: launch.RunID, Worktree: launch.Worktree, RunDir: filepath.Join(launch.Worktree, loop.RunDirRel(launch.RunID)), Outcome: loop.Failed, Reason: reason}, Ensemble: state.Ensemble, Repository: state.Repository, BaseBranch: launch.BaseBranch, Base: base, DurationMS: time.Since(state.Started).Milliseconds()}
	if err := writeJSON(filepath.Join(dir, "result.json"), sum); err != nil {
		fmt.Fprintln(os.Stderr, "km: writing result.json:", err)
		return
	}
	_ = finishRun(os.Stdout, sum)
}
