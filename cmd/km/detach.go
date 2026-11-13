package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/jystringfellow/kapellmeister/internal/loop"
)

type detachedLaunchRecord struct {
	Command         string `json:"command,omitempty"`
	Checkout        string `json:"checkout,omitempty"`
	RunID           string `json:"run_id"`
	Worktree        string `json:"worktree"`
	BaseBranch      string `json:"base_branch"`
	CreatedWorktree string `json:"created_worktree"`
	Task            string `json:"task"`
}

var detachedLaunchTimeout = 30 * time.Second

type detachedHandle struct {
	RunID    string `json:"run_id"`
	PID      int    `json:"pid"`
	Worktree string `json:"worktree"`
	Output   string `json:"output"`
}

func startDetached(ctx context.Context, args []string, dir string, launch detachedLaunchRecord, opening []byte) (detachedHandle, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return detachedHandle{}, err
	}
	path := filepath.Join(dir, "launch.json")
	if err := writeJSON(path, launch); err != nil {
		return detachedHandle{}, err
	}
	logPath := filepath.Join(dir, "output.log")
	log, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return detachedHandle{}, err
	}
	defer log.Close()
	if _, err := log.Write(opening); err != nil {
		return detachedHandle{}, err
	}
	executable, err := os.Executable()
	if err != nil {
		return detachedHandle{}, err
	}
	child := strings.Fields(firstNonEmpty(launch.Command, "run"))
	child = append(child, "-detached-launch", path)
	cmd := exec.Command(executable, append(child, args...)...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Stdout, cmd.Stderr = log, log
	if err := cmd.Start(); err != nil {
		return detachedHandle{}, err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	deadline := time.NewTimer(detachedLaunchTimeout)
	defer deadline.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	ready := func() bool {
		_, err := os.Stat(filepath.Join(dir, "current.json"))
		return err == nil
	}
	handle := detachedHandle{RunID: launch.RunID, PID: cmd.Process.Pid, Worktree: launch.Worktree, Output: logPath}
	for {
		if ready() {
			return handle, nil
		}
		select {
		case err := <-done:
			if ready() {
				return handle, nil
			}
			data, _ := os.ReadFile(logPath)
			if len(data) > 65536 {
				data = data[len(data)-65536:]
			}
			return detachedHandle{}, fmt.Errorf("detached run exited before recording its state: %v\n%s", err, data)
		case <-ctx.Done():
			_ = cmd.Process.Signal(syscall.SIGTERM)
			return detachedHandle{}, ctx.Err()
		case <-deadline.C:
			_ = cmd.Process.Signal(syscall.SIGTERM)
			return detachedHandle{}, fmt.Errorf("detached run did not record its state within %s; output: %s", detachedLaunchTimeout, logPath)
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
	if launch.Command == "tune verify" {
		sum.Command = launch.Command
		sum.Tune = &tuneSummary{}
	}
	if err := writeJSON(filepath.Join(dir, "result.json"), sum); err != nil {
		fmt.Fprintln(os.Stderr, "km: writing result.json:", err)
		return
	}
	_ = finishRun(os.Stdout, sum)
}
