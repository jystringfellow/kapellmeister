package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"
	"unicode"
	"unicode/utf16"

	"github.com/jystringfellow/kapellmeister/internal/loop"
	"golang.org/x/sys/unix"
)

type reportRun struct {
	RunID      string     `json:"run_id"`
	Path       string     `json:"path"`
	Started    *time.Time `json:"started,omitempty"`
	Repository string     `json:"repository,omitempty"`
	Ensemble   string     `json:"ensemble,omitempty"`
	Outcome    string     `json:"outcome,omitempty"`
	Rounds     *int       `json:"rounds,omitempty"`
	DurationMS *int64     `json:"duration_ms,omitempty"`
	Reason     string     `json:"reason,omitempty"`
	quality    int
	state      *runState
	hasResult  bool
	hasStarted bool
}

type reportTotals struct {
	Runs         int            `json:"runs"`
	Outcomes     map[string]int `json:"outcomes"`
	Repositories map[string]int `json:"repositories"`
	Ensembles    map[string]int `json:"ensembles"`
}

type runReport struct {
	Runs   []reportRun  `json:"runs"`
	Totals reportTotals `json:"totals"`
}

func reportCommand(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("report", flag.ContinueOnError)
	flags.SetOutput(stderr)
	asJSON := flags.Bool("json", false, "print runs and totals as JSON")
	sinceText := flags.String("since", "", "keep runs started on or after a UTC date or RFC3339 timestamp")
	repository := flags.String("repository", "", "keep runs from this exact repository")
	options, dirs := splitRecordArgs(args, "since", "repository")
	if err := flags.Parse(options); err != nil {
		return err
	}
	set := flagsSet(flags)
	var since time.Time
	if set["since"] {
		var err error
		since, err = time.Parse("2006-01-02", *sinceText)
		if err != nil {
			since, err = time.Parse(time.RFC3339, *sinceText)
		}
		if err != nil {
			return fmt.Errorf("report: -since must be a date (YYYY-MM-DD) or RFC3339 timestamp: %s", plain(*sinceText))
		}
	}
	defaultDir := len(dirs) == 0
	if defaultDir {
		dirs = []string{filepath.Join(loop.StateDir(), "runs")}
	}
	runs, diagnostics, err := findRuns(dirs, defaultDir)
	if err != nil {
		return fmt.Errorf("report: %s", plain(err.Error()))
	}
	for _, diagnostic := range diagnostics {
		fmt.Fprintln(stderr, diagnostic)
	}
	report := runReport{
		Runs:   []reportRun{},
		Totals: reportTotals{Outcomes: map[string]int{}, Repositories: map[string]int{}, Ensembles: map[string]int{}},
	}
	for _, run := range runs {
		if set["since"] && (run.Started == nil || run.Started.Before(since)) {
			continue
		}
		if set["repository"] && (run.Repository == "" || run.Repository != *repository) {
			continue
		}
		report.Runs = append(report.Runs, run)
		report.Totals.Runs++
		report.Totals.Outcomes[reportKnown(run.Outcome)]++
		report.Totals.Repositories[reportKnown(run.Repository)]++
		report.Totals.Ensembles[reportKnown(run.Ensemble)]++
	}
	sort.Slice(report.Runs, func(i, j int) bool {
		a, b := report.Runs[i], report.Runs[j]
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
		return printReportJSON(stdout, report)
	}
	return printReport(stdout, report)
}

func splitRecordArgs(args []string, valueFlags ...string) (options, dirs []string) {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			dirs = append(dirs, args[i+1:]...)
			break
		}
		if arg == "-" || !strings.HasPrefix(arg, "-") {
			dirs = append(dirs, arg)
			continue
		}
		options = append(options, arg)
		name := strings.TrimLeft(arg, "-")
		if slices.Contains(valueFlags, name) && i+1 < len(args) {
			i++
			options = append(options, args[i])
		}
	}
	return options, dirs
}

func findRuns(dirs []string, defaultDir bool, noSymlinks ...bool) (map[string]reportRun, []string, error) {
	runs := map[string]reportRun{}
	var diagnostics []string
	for _, dir := range dirs {
		var file *os.File
		var err error
		if len(noSymlinks) > 0 && noSymlinks[0] {
			file, err = openRecordRoot(dir)
		} else {
			var root string
			root, err = filepath.EvalSymlinks(dir)
			if err == nil {
				file, err = os.OpenFile(root, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_DIRECTORY, 0)
			}
		}
		if defaultDir && errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, nil, err
		}
		err = scanReportDirectory(file, runs, &diagnostics)
		file.Close()
		if err != nil {
			return nil, nil, err
		}
	}
	return runs, diagnostics, nil
}

func openRecordRoot(path string) (*os.File, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	dir, err := os.OpenFile("/", os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	for _, name := range strings.Split(strings.TrimPrefix(absolute, "/"), "/") {
		if name == "" {
			continue
		}
		child, err := openReportEntry(dir, name, unix.O_DIRECTORY)
		dir.Close()
		if err != nil {
			return nil, err
		}
		dir = child
	}
	return dir, nil
}

func scanReportDirectory(dir *os.File, runs map[string]reportRun, diagnostics *[]string) error {
	entries, err := dir.ReadDir(-1)
	if err != nil {
		return err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	result, telemetry, current := false, false, false
	for _, entry := range entries {
		result = result || entry.Name() == "result.json"
		telemetry = telemetry || entry.Name() == "telemetry.jsonl"
		current = current || entry.Name() == "current.json"
	}
	if result || telemetry || current {
		run, messages := readReportRun(dir, result, telemetry, current)
		*diagnostics = append(*diagnostics, messages...)
		keepReportRun(runs, run)
		return nil
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		path := filepath.Join(dir.Name(), entry.Name())
		child, err := openReportEntry(dir, entry.Name(), unix.O_DIRECTORY)
		if err == nil {
			err = scanReportDirectory(child, runs, diagnostics)
			child.Close()
		}
		if err != nil {
			*diagnostics = append(*diagnostics, "km: report: "+plain(path)+": "+plain(err.Error()))
			keepReportRun(runs, reportRun{RunID: entry.Name(), Path: path, Outcome: "unreadable"})
		}
	}
	return nil
}

func keepReportRun(runs map[string]reportRun, run reportRun) {
	if previous, exists := runs[run.RunID]; !exists || run.quality > previous.quality {
		runs[run.RunID] = run
	}
}

func openReportEntry(dir *os.File, name string, flags int) (*os.File, error) {
	path := filepath.Join(dir.Name(), name)
	// Resolve one entry against its open parent so renames cannot redirect through symlinks.
	fd, err := unix.Openat(int(dir.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC|flags, 0)
	runtime.KeepAlive(dir)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	return os.NewFile(uintptr(fd), path), nil
}

func readReportRun(dir *os.File, hasResult, hasTelemetry, hasCurrent bool) (reportRun, []string) {
	run := reportRun{RunID: filepath.Base(dir.Name()), Path: dir.Name(), Outcome: "unreadable", hasResult: hasResult, hasStarted: hasTelemetry || hasCurrent}
	var diagnostics []string
	diagnose := func(path string, err error) {
		diagnostics = append(diagnostics, "km: report: "+plain(path)+": "+plain(err.Error()))
	}
	if hasResult {
		path := filepath.Join(dir.Name(), "result.json")
		data, err := readReportFile(dir, "result.json", false)
		var recorded *struct {
			ensembleSummary
			Rounds     *int   `json:"rounds"`
			DurationMS *int64 `json:"duration_ms"`
		}
		if err == nil {
			err = json.Unmarshal(data, &recorded)
			if err == nil && recorded == nil {
				err = errors.New("expected a result object")
			}
		}
		if err != nil {
			diagnose(path, err)
			return run, diagnostics
		}
		if recorded.RunID != "" {
			run.RunID = recorded.RunID
		}
		run.Repository, run.Ensemble = recorded.Repository, recorded.Ensemble
		run.Outcome, run.Reason = recorded.Outcome, recorded.Reason
		run.Rounds, run.DurationMS = recorded.Rounds, recorded.DurationMS
		run.quality = 2
	}
	if !hasResult && hasCurrent {
		data, err := readReportFile(dir, "current.json", false)
		var state *runState
		if err == nil {
			err = json.Unmarshal(data, &state)
			if err == nil && (state == nil || state.Started.IsZero()) {
				err = errors.New("expected a current state with a start time")
			}
		}
		if err != nil {
			diagnose(filepath.Join(dir.Name(), "current.json"), err)
		} else {
			run.state = state
			run.Repository, run.Ensemble = state.Repository, state.Ensemble
			started := state.Started.UTC()
			run.Started = &started
			if state.RunID != "" {
				run.RunID = state.RunID
			}
			run.Outcome, run.quality = "unfinished", 1
		}
	}
	if hasTelemetry && run.state == nil {
		path := filepath.Join(dir.Name(), "telemetry.jsonl")
		data, err := readReportFile(dir, "telemetry.jsonl", true)
		var record loop.Record
		if err == nil {
			err = json.Unmarshal(data, &record)
			if err == nil && record.At.IsZero() {
				err = errors.New("first telemetry record has no start time")
			}
		}
		if err != nil {
			diagnose(path, err)
			return run, diagnostics
		}
		started := record.At.UTC()
		run.Started = &started
		if !hasResult {
			run.Outcome, run.quality = "unfinished", 1
		}
	}
	return run, diagnostics
}

const reportFileLimit = 1 << 20

func readReportFile(dir *os.File, name string, firstLine bool) ([]byte, error) {
	var stat unix.Stat_t
	err := unix.Fstatat(int(dir.Fd()), name, &stat, unix.AT_SYMLINK_NOFOLLOW)
	if err != nil {
		return nil, err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG {
		return nil, errors.New("not a regular file")
	}
	file, err := openReportEntry(dir, name, 0)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("not a regular file")
	}
	reader := io.LimitReader(file, reportFileLimit+1)
	var data []byte
	if firstLine {
		data, err = bufio.NewReader(reader).ReadBytes('\n')
		if errors.Is(err, io.EOF) {
			err = nil
		}
	} else {
		data, err = io.ReadAll(reader)
	}
	if err != nil {
		return nil, err
	}
	if len(data) > reportFileLimit {
		return nil, errors.New("record exceeds 1 MiB")
	}
	return data, nil
}

func reportKnown(value string) string {
	if value == "" {
		return "unknown"
	}
	return value
}

func plain(value string) string {
	var out strings.Builder
	for _, r := range value {
		if unicode.IsPrint(r) {
			out.WriteRune(r)
		} else {
			quoted := strconv.QuoteRune(r)
			out.WriteString(quoted[1 : len(quoted)-1])
		}
	}
	return out.String()
}

func printReportJSON(stdout io.Writer, report any) error {
	data, err := json.Marshal(report)
	if err != nil {
		return err
	}
	var out strings.Builder
	for _, r := range string(data) {
		if unicode.IsPrint(r) {
			out.WriteRune(r)
		} else if r <= 0xffff {
			fmt.Fprintf(&out, "\\u%04x", r)
		} else {
			hi, lo := utf16.EncodeRune(r)
			fmt.Fprintf(&out, "\\u%04x\\u%04x", hi, lo)
		}
	}
	_, err = fmt.Fprintln(stdout, out.String())
	return err
}

func printReport(stdout io.Writer, report runReport) error {
	if len(report.Runs) == 0 {
		_, err := fmt.Fprintln(stdout, "no runs found")
		return err
	}
	w := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "STARTED\tREPOSITORY\tENSEMBLE\tOUTCOME\tROUNDS\tTOOK")
	for _, run := range report.Runs {
		started, rounds, took := "unknown", "unknown", "unknown"
		if run.Started != nil {
			started = run.Started.UTC().Format("2006-01-02 15:04:05")
		}
		if run.Rounds != nil {
			rounds = strconv.Itoa(*run.Rounds)
		}
		if run.DurationMS != nil {
			took = (time.Duration(*run.DurationMS) * time.Millisecond).Round(time.Second).String()
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", plain(started), plain(reportKnown(run.Repository)), plain(reportKnown(run.Ensemble)), plain(reportKnown(run.Outcome)), plain(rounds), plain(took))
	}
	if err := w.Flush(); err != nil {
		return err
	}
	fmt.Fprintf(w, "\nruns: %d\n", report.Totals.Runs)
	for _, group := range []struct {
		name   string
		counts map[string]int
	}{{"outcome", report.Totals.Outcomes}, {"repository", report.Totals.Repositories}, {"ensemble", report.Totals.Ensembles}} {
		fmt.Fprintf(w, "by %s\n", group.name)
		names := make([]string, 0, len(group.counts))
		for name := range group.counts {
			names = append(names, name)
		}
		sort.Slice(names, func(i, j int) bool {
			if group.counts[names[i]] != group.counts[names[j]] {
				return group.counts[names[i]] > group.counts[names[j]]
			}
			return names[i] < names[j]
		})
		for _, name := range names {
			fmt.Fprintf(w, "  %s\t%d\n", plain(name), group.counts[name])
		}
		if err := w.Flush(); err != nil {
			return err
		}
	}
	return nil
}
