package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/jystringfellow/kapellmeister/internal/loop"
	"golang.org/x/sys/unix"
)

type exitStatusError struct {
	code int
	err  error
}

func (e *exitStatusError) Error() string {
	if e.err != nil {
		return e.err.Error()
	}
	return fmt.Sprintf("exit status %d", e.code)
}
func (e *exitStatusError) Unwrap() error { return e.err }

type waitResult struct {
	RunID      string           `json:"run_id"`
	State      string           `json:"state"`
	ExitStatus int              `json:"exit_status"`
	Reason     string           `json:"reason"`
	Result     *ensembleSummary `json:"result,omitempty"`
}

var waitPollInterval = time.Second

func waitCommand(args []string, stdout, stderr io.Writer) error {
	return waitAt(nil, args, stdout, stderr)
}

func waitAt(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("wait", flag.ContinueOnError)
	fs.SetOutput(stderr)
	timeout := fs.Duration("timeout", 0, "maximum wait; 0 waits until the run finishes")
	asJSON := fs.Bool("json", false, "print the result and exit status as JSON")
	maxDepth := fs.Int("max-depth", 64, "maximum directory levels below each root")
	maxEntries := fs.Int("max-entries", 1000000, "maximum directory entries read (at most 16777216)")
	options, positional := splitRecordArgs(args, "timeout", "max-depth", "max-entries")
	if err := fs.Parse(options); err != nil {
		return err
	}
	if len(positional) == 0 {
		return errors.New("usage: km wait RUN [-timeout D] [-json] [DIR...]")
	}
	if *timeout < 0 {
		return errors.New("wait: -timeout must not be negative")
	}
	if *maxDepth < 1 || *maxEntries < 1 || *maxEntries > 1<<24 {
		return errors.New("wait: -max-depth must be at least 1; -max-entries must be between 1 and 16777216")
	}
	id := positional[0]
	if strings.TrimSpace(id) == "" {
		return errors.New("wait: RUN must not be empty")
	}
	run, err := findWaitRun(id, positional[1:], *maxDepth, *maxEntries, stderr)
	if err != nil {
		return err
	}

	dir, err := os.OpenFile(run.Path, os.O_RDONLY|unix.O_NOFOLLOW|unix.O_DIRECTORY, 0)
	if err != nil {
		return fmt.Errorf("wait: %s", plain(err.Error()))
	}
	defer dir.Close()
	if ctx == nil {
		var stop context.CancelFunc
		ctx, stop = loop.SignalContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
		defer stop()
	}
	if *timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, *timeout)
		defer cancel()
	}
	emit := func(state string, code int, reason string, sum *ensembleSummary) error {
		if *asJSON {
			if err := printReportJSON(stdout, waitResult{RunID: run.RunID, State: state, ExitStatus: code, Reason: reason, Result: sum}); err != nil {
				return err
			}
			if code != 0 {
				return &exitStatusError{code: code}
			}
			return nil
		}
		if sum != nil {
			return finishRun(stdout, *sum)
		}
		return &exitStatusError{code: code, err: errors.New(reason)}
	}
	invalid := 0
	for {
		sum, resultErr := readWaitResult(dir)
		if resultErr == nil {
			code := 0
			if sum.Outcome != loop.Approved {
				code = 1
			}
			return emit("finished", code, sum.Reason, &sum)
		}
		if !errors.Is(resultErr, os.ErrNotExist) {
			invalid++
			if invalid >= 4 {
				return fmt.Errorf("wait: reading result for %s: %w", plain(run.RunID), resultErr)
			}
		} else {
			invalid = 0
			var state runState
			data, stateErr := readReportFile(dir, "current.json", false)
			live := "unknown"
			if stateErr == nil && json.Unmarshal(data, &state) == nil {
				live = runLiveness(state)
			}
			if live != "alive" {
				// A result can be published between checking it and checking the process.
				if sum, err := readWaitResult(dir); err == nil {
					code := 0
					if sum.Outcome != loop.Approved {
						code = 1
					}
					return emit("finished", code, sum.Reason, &sum)
				}
				reason := fmt.Sprintf("run %s is unfinished: its process is gone and no result was recorded", plain(run.RunID))
				if live == "unknown" {
					reason = fmt.Sprintf("run %s is unfinished: liveness cannot be checked and no result was recorded", plain(run.RunID))
				}
				return emit("unfinished", 4, reason, nil)
			}
		}
		timer := time.NewTimer(waitPollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return emit("timeout", 3, fmt.Sprintf("timed out waiting for run %s", plain(run.RunID)), nil)
			}
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func resolveWaitRun(id string, found []reportRun) (reportRun, error) {
	var matches []reportRun
	for _, run := range found {
		if run.RunID == id {
			if run.Conflict {
				return reportRun{}, fmt.Errorf("wait: run %q has conflicting records", plain(id))
			}
			return run, nil
		}
		if strings.HasPrefix(run.RunID, id) {
			matches = append(matches, run)
		}
	}
	if len([]rune(id)) < 2 {
		return reportRun{}, errors.New("wait: run prefix must be at least 2 characters; use a full run id")
	}
	if len(matches) == 1 {
		if matches[0].Conflict {
			return reportRun{}, fmt.Errorf("wait: run %q has conflicting records", plain(matches[0].RunID))
		}
		return matches[0], nil
	}
	if len(matches) == 0 {
		return reportRun{}, fmt.Errorf("wait: unknown run %q", plain(id))
	}
	ids := make([]string, 0, len(matches))
	for _, run := range matches {
		ids = append(ids, plain(run.RunID))
	}
	sort.Strings(ids)
	return reportRun{}, fmt.Errorf("wait: ambiguous run %q: %s", plain(id), strings.Join(ids, ", "))
}

func readWaitResult(dir *os.File) (ensembleSummary, error) {
	var sum ensembleSummary
	data, err := readReportFile(dir, "result.json", false)
	if err != nil {
		return sum, err
	}
	if err := json.Unmarshal(data, &sum); err != nil {
		return sum, err
	}
	if sum.RunID == "" || sum.Outcome == "" {
		return sum, errors.New("result.json has no run_id or outcome")
	}
	return sum, nil
}

func findWaitRun(id string, named []string, maxDepth, maxEntries int, stderr io.Writer) (reportRun, error) {
	if strings.TrimSpace(id) == "" {
		return reportRun{}, errors.New("wait: RUN must not be empty")
	}
	dirs, configured, source, err := recordDirs(named)
	if err != nil {
		return reportRun{}, fmt.Errorf("wait: %s", plain(err.Error()))
	}
	found, scan, err := findRuns(dirs, len(named) == 0 && !configured, maxDepth, maxEntries, true, false)
	if err != nil {
		if configured {
			err = fmt.Errorf("%w (from [report] dirs in %s)", err, source)
		}
		return reportRun{}, fmt.Errorf("wait: %s", plain(err.Error()))
	}
	run, err := resolveWaitRun(id, found)
	if err != nil {
		return reportRun{}, err
	}
	for _, diagnostic := range scan.diagnostics {
		if strings.Contains(diagnostic, plain(filepath.Join(run.Path, "result.json"))+":") {
			continue
		}
		fmt.Fprintln(stderr, strings.Replace(diagnostic, "km: report:", "km: wait:", 1))
	}
	return run, nil
}
