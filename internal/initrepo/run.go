package initrepo

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)

type VerifyResult struct {
	Command    string `json:"command"`
	Status     string `json:"status"`
	ExitCode   int    `json:"exit_code"`
	DurationMS int64  `json:"duration_ms"`
	Output     string `json:"output"`
}

type tailWriter struct {
	mu          sync.Mutex
	lines       []string
	current     []byte
	stepFailure string
}

func (w *tailWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, b := range p {
		if b == '\n' {
			line := string(w.current) + "\n"
			if strings.HasPrefix(line, "verify failed at step: ") {
				w.stepFailure = line
			}
			w.lines = append(w.lines, line)
			if len(w.lines) > 40 {
				w.lines = w.lines[1:]
			}
			w.current = nil
		} else if len(w.current) < 300 {
			w.current = append(w.current, b)
		}
	}
	return len(p), nil
}
func (w *tailWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	lines := w.lines
	if len(w.current) > 0 && len(lines) == 40 {
		lines = lines[1:]
	}
	if w.stepFailure != "" {
		found := false
		for _, line := range lines {
			if line == w.stepFailure {
				found = true
				break
			}
		}
		if !found {
			count := len(lines)
			if len(w.current) > 0 {
				count++
			}
			if count == 40 {
				lines = lines[1:]
			}
			return w.stepFailure + strings.Join(lines, "") + string(w.current)
		}
	}
	return strings.Join(lines, "") + string(w.current)
}

func Run(parent context.Context, root, command string, timeout time.Duration) VerifyResult {
	start := time.Now()
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	result := VerifyResult{Command: command, Status: "passed"}
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Dir = root
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	cmd.WaitDelay = 200 * time.Millisecond
	var tail tailWriter
	cmd.Stdout = &tail
	cmd.Stderr = &tail
	err := cmd.Run()
	if errors.Is(err, exec.ErrWaitDelay) && cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	result.Output = tail.String()
	result.DurationMS = time.Since(start).Milliseconds()
	if err != nil {
		result.Status = "failed"
		result.ExitCode = -1
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			result.ExitCode = exit.ExitCode()
		} else {
			_, _ = tail.Write([]byte("\n" + err.Error()))
			result.Output = tail.String()
		}
	}
	if ctx.Err() != nil {
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		result.Status = "timed_out"
		result.ExitCode = -1
	}
	return result
}
