package main

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

func processStart(pid int) (string, error) {
	if pid <= 0 {
		return "", fmt.Errorf("invalid process id")
	}
	processes, err := unix.SysctlKinfoProcSlice("kern.proc.pid", pid)
	if err != nil {
		return "", err
	}
	if len(processes) == 0 || processes[0].Proc.P_stat == 5 {
		return "", os.ErrNotExist
	}
	start := processes[0].Proc.P_starttime
	return fmt.Sprintf("%d:%d", start.Sec, start.Usec), nil
}
