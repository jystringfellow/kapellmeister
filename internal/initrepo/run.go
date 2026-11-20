package initrepo

import (
	"context"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/jystringfellow/kapellmeister/internal/loop"
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
	result := VerifyResult{Command: command, Status: "passed"}
	var tail tailWriter
	ran := loop.RunShell(parent, root, command, nil, timeout, &tail)
	result.ExitCode = ran.ExitCode
	switch {
	case ran.TimedOut || parent.Err() != nil:
		result.Status = "timed_out"
	case ran.Err != nil:
		result.Status = "failed"
		_, _ = tail.Write([]byte("\n" + ran.Err.Error()))
	case ran.Orphaned:
		result.Status, result.ExitCode = "failed", -1
		_, _ = tail.Write([]byte("\n" + exec.ErrWaitDelay.Error()))
	case ran.ExitCode != 0:
		result.Status = "failed"
	}
	result.Output = tail.String()
	result.DurationMS = time.Since(start).Milliseconds()
	return result
}
