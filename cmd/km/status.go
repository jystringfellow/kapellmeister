package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/jystringfellow/kapellmeister/internal/loop"
)

type statusStep struct {
	Step      string    `json:"step"`
	Chair     string    `json:"chair"`
	Started   time.Time `json:"started"`
	ElapsedMS int64     `json:"elapsed_ms"`
}

type statusRun struct {
	RunID      string       `json:"run_id"`
	Path       string       `json:"path"`
	Started    *time.Time   `json:"started"`
	Repository string       `json:"repository"`
	Ensemble   string       `json:"ensemble"`
	Command    string       `json:"command"`
	Round      int          `json:"round"`
	MaxRounds  int          `json:"max_rounds"`
	Playing    []statusStep `json:"playing"`
	Updated    *time.Time   `json:"updated"`
	Liveness   string       `json:"liveness"`
}

func statusCommand(args []string, stdout, stderr io.Writer) error {
	return statusAt(args, stdout, stderr, time.Now())
}

func statusAt(args []string, stdout, stderr io.Writer, now time.Time) error {
	flags := flag.NewFlagSet("status", flag.ContinueOnError)
	flags.SetOutput(stderr)
	asJSON := flags.Bool("json", false, "print runs in progress as JSON")
	maxDepth := flags.Int("max-depth", 64, "maximum directory levels below each root")
	maxEntries := flags.Int("max-entries", 1000000, "maximum directory entries read (at most 16777216)")
	options, dirs := splitRecordArgs(args, "max-depth", "max-entries")
	if err := flags.Parse(options); err != nil {
		return err
	}
	if *maxDepth < 1 || *maxEntries < 1 || *maxEntries > 1<<24 {
		return errors.New("status: -max-depth must be at least 1; -max-entries must be between 1 and 16777216")
	}
	defaultDir := len(dirs) == 0
	if defaultDir {
		dirs = []string{filepath.Join(loop.StateDir(), "runs")}
	}
	found, scan, err := findRuns(dirs, defaultDir, *maxDepth, *maxEntries, true)
	if err != nil {
		return fmt.Errorf("status: %s", plain(err.Error()))
	}
	for _, diagnostic := range scan.diagnostics {
		fmt.Fprintln(stderr, strings.Replace(diagnostic, "km: report:", "km: status:", 1))
	}
	runs := []statusRun{}
	for _, recorded := range unfinishedRuns(found) {
		run := statusRun{RunID: recorded.RunID, Path: recorded.Path, Started: recorded.Started, Repository: recorded.Repository, Ensemble: recorded.Ensemble, Playing: []statusStep{}, Liveness: "unknown"}
		if state := recorded.state; state != nil {
			run.Command, run.Round, run.MaxRounds = state.Command, state.Round, state.MaxRounds
			if !state.Updated.IsZero() {
				run.Updated = &state.Updated
			}
			run.Liveness = runLiveness(*state)
			for _, active := range state.Playing {
				run.Playing = append(run.Playing, statusStep{Step: active.Step, Chair: firstNonEmpty(active.Chair, active.ChairName), Started: active.Started, ElapsedMS: elapsedSince(now, active.Started).Milliseconds()})
			}
			sort.SliceStable(run.Playing, func(i, j int) bool { return run.Playing[i].Started.Before(run.Playing[j].Started) })
		}
		runs = append(runs, run)
	}
	sort.Slice(runs, func(i, j int) bool {
		a, b := runs[i], runs[j]
		if a.Started == nil && b.Started != nil {
			return false
		}
		if a.Started != nil && b.Started == nil {
			return true
		}
		if a.Started != nil && !a.Started.Equal(*b.Started) {
			return a.Started.Before(*b.Started)
		}
		return a.RunID < b.RunID
	})
	if *asJSON {
		return printReportJSON(stdout, struct {
			Runs []statusRun `json:"runs"`
		}{runs})
	}
	if len(runs) == 0 && scan.notSearchedTotal > 0 {
		_, err := fmt.Fprintf(stdout, "no runs in progress found; %d directories were not searched\n", scan.notSearchedTotal)
		return err
	}
	return printStatus(stdout, runs, now)
}

func runLiveness(state runState) string {
	host, err := os.Hostname()
	if err != nil || state.Host == "" || state.Host != host || state.PID <= 0 || state.ProcessStart == "" {
		return "unknown"
	}
	token, err := processStart(state.PID)
	if errors.Is(err, os.ErrNotExist) {
		return "abandoned"
	}
	if err != nil || token == "" {
		return "unknown"
	}
	if token != state.ProcessStart {
		return "abandoned"
	}
	return "alive"
}

func elapsedSince(now, started time.Time) time.Duration {
	if started.IsZero() || now.Before(started) {
		return 0
	}
	return now.Sub(started)
}

func printStatus(stdout io.Writer, runs []statusRun, now time.Time) error {
	if len(runs) == 0 {
		_, err := fmt.Fprintln(stdout, "no runs in progress")
		return err
	}
	w := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "RUN\tSTARTED\tREPOSITORY\tENSEMBLE\tROUND\tSTEP\tCHAIR\tFOR\tLIVENESS")
	for _, run := range runs {
		started, round, step, chair, elapsed := "unknown", "-", "unknown", "-", "unknown"
		if run.Started != nil {
			started = run.Started.UTC().Format("2006-01-02 15:04:05")
		}
		if run.Round > 0 {
			round = fmt.Sprintf("%d/%d", run.Round, run.MaxRounds)
		}
		if run.Updated != nil {
			step = "between steps"
			elapsed = elapsedSince(now, *run.Updated).Round(time.Second).String()
		}
		if len(run.Playing) > 0 {
			active := run.Playing[0]
			step, chair = reportKnown(active.Step), firstNonEmpty(active.Chair, "-")
			if len(run.Playing) > 1 {
				step += fmt.Sprintf(" +%d", len(run.Playing)-1)
			}
			elapsed = elapsedSince(now, active.Started).Round(time.Second).String()
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", plain(run.RunID), plain(started), plain(reportKnown(run.Repository)), plain(reportKnown(run.Ensemble)), plain(round), plain(step), plain(chair), plain(elapsed), plain(run.Liveness))
	}
	return w.Flush()
}

func unfinishedRuns(found []reportRun) []reportRun {
	finished := map[string]bool{}
	for _, recorded := range found {
		finished[recorded.RunID] = finished[recorded.RunID] || recorded.hasResult
	}
	var runs []reportRun
	for _, recorded := range found {
		if !finished[recorded.RunID] && recorded.hasStarted {
			runs = append(runs, recorded)
		}
	}
	return runs
}
