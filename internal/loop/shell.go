package loop

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"syscall"
	"time"
)

type ShellRun struct {
	ExitCode int
	TimedOut bool
	Orphaned bool
	Err      error
}

func RunShell(parent context.Context, dir, command string, env []string, limit time.Duration, out io.Writer) ShellRun {
	ctx := parent
	if limit > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(parent, limit)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, "sh", "-c", "unset "+verifySkipEnv+"\n"+command)
	cmd.Dir = dir
	cmd.Env = env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stopGroup := func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	cmd.Cancel = stopGroup
	cmd.WaitDelay = 200 * time.Millisecond
	cmd.Stdout, cmd.Stderr = out, out
	err := cmd.Run()
	if cmd.Process != nil {
		_ = stopGroup()
	}
	switch {
	case parent.Err() != nil:
		return ShellRun{ExitCode: -1, Err: parent.Err()}
	case ctx.Err() != nil:
		return ShellRun{ExitCode: -1, TimedOut: true}
	}
	var exit *exec.ExitError
	switch {
	case err == nil:
		return ShellRun{}
	case errors.Is(err, exec.ErrWaitDelay):
		return ShellRun{Orphaned: true}
	case errors.As(err, &exit):
		return ShellRun{ExitCode: exit.ExitCode()}
	}
	return ShellRun{ExitCode: -1, Err: err}
}
