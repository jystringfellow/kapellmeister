package loop

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

type leftProcess struct {
	PID     int
	Command string
}

func leftRunning(scratch string) []leftProcess {
	if scratch == "" {
		return nil
	}
	file, err := os.OpenFile(filepath.Join(scratch, "pids"), os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil
	}
	defer file.Close()
	const maxPidsBytes = 1 << 20
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxPidsBytes {
		return nil
	}
	data, err := io.ReadAll(io.LimitReader(file, maxPidsBytes+1))
	if err != nil || len(data) > maxPidsBytes {
		return nil
	}
	var processes []leftProcess
	seen := make(map[int]bool)
	for _, line := range strings.Split(string(data), "\n") {
		pid, err := strconv.Atoi(strings.TrimSpace(line))
		if err != nil || pid <= 0 || seen[pid] {
			continue
		}
		seen[pid] = true
		output, err := exec.Command("ps", "-o", "command=", "-p", strconv.Itoa(pid)).Output()
		command := strings.TrimSpace(string(output))
		if err != nil || command == "" {
			continue
		}
		processes = append(processes, leftProcess{PID: pid, Command: command})
	}
	return processes
}

func (e *Engine) reportLeftRunning(round int) {
	for _, process := range leftRunning(e.Config.ScratchDir) {
		e.progress("  left running: pid %d (%s)", process.PID, process.Command)
		e.record(Record{RunID: e.Config.RunID, Step: "left-running", Round: round, PID: process.PID, Command: process.Command})
	}
}
