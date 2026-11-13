package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"
	"unicode"
	"unicode/utf16"

	"github.com/jystringfellow/kapellmeister/internal/loop"
	"golang.org/x/sys/unix"
)

type reportRun struct {
	Retries    int         `json:"retries,omitempty"`
	Restores   int         `json:"restores,omitempty"`
	Command    string      `json:"command,omitempty"`
	RunID      string      `json:"run_id"`
	Path       string      `json:"path"`
	Started    *time.Time  `json:"started,omitempty"`
	Repository string      `json:"repository,omitempty"`
	Ensemble   string      `json:"ensemble,omitempty"`
	Outcome    string      `json:"outcome,omitempty"`
	Rounds     *int        `json:"rounds,omitempty"`
	DurationMS *int64      `json:"duration_ms,omitempty"`
	Reason     string      `json:"reason,omitempty"`
	Usage      reportUsage `json:"usage"`
	Conflict   bool        `json:"conflict,omitempty"`
	quality    int
	state      *runState
	roughStart bool
	hasResult  bool
	hasStarted bool
	parts      *reportPartRun
}

type reportTotals struct {
	Retries         int                         `json:"retries,omitempty"`
	Restores        int                         `json:"restores,omitempty"`
	Usage           reportUsageTotal            `json:"usage"`
	EnsembleUsage   map[string]reportUsageTotal `json:"ensemble_usage"`
	RepositoryUsage map[string]reportUsageTotal `json:"repository_usage"`
	Runs            int                         `json:"runs"`
	Outcomes        map[string]int              `json:"outcomes"`
	Repositories    map[string]int              `json:"repositories"`
	Ensembles       map[string]int              `json:"ensembles"`
}

type runReport struct {
	SkippedDirs      []string            `json:"skipped_dirs,omitempty"`
	Searched         []string            `json:"searched,omitempty"`
	Parts            *reportParts        `json:"parts,omitempty"`
	NotSearched      []reportNotSearched `json:"not_searched"`
	NotSearchedTotal int                 `json:"not_searched_total"`
	Runs             []reportRun         `json:"runs"`
	Totals           reportTotals        `json:"totals"`
}

func reportCommand(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("report", flag.ContinueOnError)
	flags.SetOutput(stderr)
	withParts := flags.Bool("parts", false, "append steps and work grouped by part, provider and model")
	asJSON := flags.Bool("json", false, "print runs and totals as JSON")
	sinceText := flags.String("since", "", "keep runs started on or after a UTC date or RFC3339 timestamp")
	repository := flags.String("repository", "", "keep runs from this exact repository")
	maxDepth := flags.Int("max-depth", 64, "maximum directory levels below each root")
	maxEntries := flags.Int("max-entries", 1000000, "maximum directory entries read (at most 16777216)")
	options, dirs := splitRecordArgs(args, "since", "repository", "max-depth", "max-entries")
	if err := flags.Parse(options); err != nil {
		return err
	}
	if *maxDepth < 1 || *maxEntries < 1 || *maxEntries > 1<<24 {
		return errors.New("report: -max-depth must be at least 1; -max-entries must be between 1 and 16777216")
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
	dirs, origin, source, err := recordDirs(dirs)
	if err != nil {
		return fmt.Errorf("report: %s", plain(err.Error()))
	}
	runs, scan, err := findRuns(dirs, origin, source, *maxDepth, *maxEntries, false, *withParts)
	if err != nil {
		return fmt.Errorf("report: %s", plain(err.Error()))
	}
	for _, diagnostic := range scan.diagnostics {
		fmt.Fprintln(stderr, diagnostic)
	}
	report := runReport{
		Runs:        []reportRun{},
		SkippedDirs: scan.missingDirs,
		NotSearched: scan.notSearched, NotSearchedTotal: scan.notSearchedTotal,
		Totals: reportTotals{EnsembleUsage: map[string]reportUsageTotal{}, RepositoryUsage: map[string]reportUsageTotal{}, Outcomes: map[string]int{}, Repositories: map[string]int{}, Ensembles: map[string]int{}},
	}
	if origin == configuredRecordDirs {
		report.Searched = scan.searched
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
		if run.Restores > 0 {
			report.Totals.Restores++
		}
		if run.Retries > 0 {
			report.Totals.Retries++
		}
		report.Totals.Usage.add(run.Usage)
		addReportGroupUsage(report.Totals.EnsembleUsage, reportKnown(run.Ensemble), run.Usage)
		addReportGroupUsage(report.Totals.RepositoryUsage, reportKnown(run.Repository), run.Usage)
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
		if a.RunID != b.RunID {
			return a.RunID < b.RunID
		}
		return a.Path < b.Path
	})
	if *withParts {
		report.Parts = mergeReportParts(report.Runs)
	}
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

func readReportRun(dir *os.File, hasResult, hasTelemetry, hasCurrent, hasConfig bool) (reportRun, []string) {
	run := reportRun{RunID: filepath.Base(dir.Name()), Path: dir.Name(), Outcome: "unreadable", hasResult: hasResult, hasStarted: hasTelemetry || hasCurrent}
	var diagnostics []string
	hasRecordedID := false
	diagnose := func(path string, err error) {
		diagnostics = append(diagnostics, "km: report: "+plain(path)+": "+plain(err.Error()))
	}
	if hasResult {
		path := filepath.Join(dir.Name(), "result.json")
		data, err := readReportFile(dir, "result.json", false)
		var recorded *struct {
			Restores   []loop.Restore  `json:"restores"`
			Command    string          `json:"command"`
			RunID      string          `json:"run_id"`
			Repository string          `json:"repository"`
			Ensemble   string          `json:"ensemble"`
			Outcome    string          `json:"outcome"`
			Reason     string          `json:"reason"`
			Usage      json.RawMessage `json:"usage"`
			Rounds     *int            `json:"rounds"`
			DurationMS *int64          `json:"duration_ms"`
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
			hasRecordedID = true
		}
		run.Repository, run.Ensemble = recorded.Repository, recorded.Ensemble
		run.Command = recorded.Command
		run.Restores = len(recorded.Restores)
		for _, restore := range recorded.Restores {
			if restore.Outcome == "retried" {
				run.Retries++
			}
		}
		run.Outcome, run.Reason = recorded.Outcome, recorded.Reason
		run.Rounds, run.DurationMS = recorded.Rounds, recorded.DurationMS
		run.Usage = readReportUsage(recorded.Usage)
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
			if state.Command == "tune verify" {
				run.Command = state.Command
			}
			run.Repository, run.Ensemble = state.Repository, state.Ensemble
			started := state.Started.UTC()
			run.Started, run.roughStart = &started, true
			if state.RunID != "" {
				run.RunID = state.RunID
				hasRecordedID = true
			}
			run.Outcome, run.quality = "unfinished", 1
		}
	}
	emptyTelemetry := false
	if hasTelemetry && run.state == nil {
		path := filepath.Join(dir.Name(), "telemetry.jsonl")
		data, err := readReportFile(dir, "telemetry.jsonl", true)
		var record loop.Record
		if err == nil {
			emptyTelemetry = len(data) == 0
			if !emptyTelemetry {
				err = json.Unmarshal(data, &record)
				if err == nil && record.At.IsZero() {
					err = errors.New("first telemetry record has no start time")
				}
			}
		}
		if err != nil {
			diagnose(path, err)
			return run, diagnostics
		}
		if !emptyTelemetry {
			started := record.At.UTC()
			run.Started = &started
		}
		if !hasResult {
			run.Outcome, run.quality = "unfinished", 1
		}
	}
	if hasConfig && !hasRecordedID {
		data, err := readReportFile(dir, "config.json", false)
		var config struct{ RunID string }
		if err == nil {
			err = json.Unmarshal(data, &config)
		}
		if err != nil {
			diagnose(filepath.Join(dir.Name(), "config.json"), err)
		} else if config.RunID != "" {
			run.RunID = config.RunID
		}
	}
	if emptyTelemetry && run.Started == nil {
		run.Started, run.roughStart = startFromRunID(run.RunID), true
	}
	return run, diagnostics
}

func startFromRunID(id string) *time.Time {
	if len(id) != 22 || id[15] != '-' {
		return nil
	}
	started, err := time.Parse("20060102-150405", id[:15])
	if err != nil {
		return nil
	}
	return &started
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
		lines := bufio.NewReader(reader)
		consumed := 0
		for {
			data, err = lines.ReadBytes('\n')
			consumed += len(data)
			if consumed > reportFileLimit {
				return nil, errors.New("record exceeds 1 MiB")
			}
			if err != nil && !errors.Is(err, io.EOF) {
				return nil, err
			}
			if len(bytes.TrimSpace(data)) > 0 {
				return data, nil
			}
			if errors.Is(err, io.EOF) {
				return nil, nil
			}
		}
	}
	data, err = io.ReadAll(reader)
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
		if _, err := fmt.Fprintln(stdout, "no runs found"); err != nil {
			return err
		}
		return printReportNotSearched(stdout, report)
	}
	w := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "STARTED\tREPOSITORY\tENSEMBLE\tOUTCOME\tROUNDS\tTOOK\tTOKENS")
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
		tokens := "unknown"
		if run.Usage.Known {
			tokens = compactTokens(run.Usage.InputTokens) + "/" + compactTokens(run.Usage.OutputTokens)
			if run.Usage.UnknownSteps > 0 {
				tokens = "≥" + tokens
			}
		}
		repoWidth, ensembleWidth := 20, 8
		if run.Command == "tune verify" {
			repoWidth, ensembleWidth = 17, 11
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", reportCell(started, 19), reportCell(reportKnown(run.Repository), repoWidth), reportCell(reportRunLabel(run.Ensemble, run.Command), ensembleWidth), reportCell(reportKnown(run.Outcome), 16), reportCell(rounds, 7), reportCell(took, 7), plain(tokens))
	}
	if err := w.Flush(); err != nil {
		return err
	}
	fmt.Fprintf(w, "\nruns: %d\n", report.Totals.Runs)
	if report.Totals.Restores > 0 {
		fmt.Fprintf(w, "restored: %d\n", report.Totals.Restores)
	}
	if report.Totals.Retries > 0 {
		fmt.Fprintf(w, "retried: %d\n", report.Totals.Retries)
	}
	conflicts := 0
	for _, run := range report.Runs {
		if run.Conflict {
			conflicts++
		}
	}
	if conflicts > 0 {
		fmt.Fprintf(w, "conflicts: %d\n", conflicts)
	}
	fmt.Fprintf(w, "usage: %s\n", plain(reportUsageText(report.Totals.Usage)))
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
			suffix := ""
			if group.name == "ensemble" {
				suffix = "  " + reportUsageText(report.Totals.EnsembleUsage[name])
			}
			if group.name == "repository" {
				suffix = "  " + reportUsageText(report.Totals.RepositoryUsage[name])
			}
			fmt.Fprintf(w, "  %s\t%d%s\n", plain(name), group.counts[name], plain(suffix))
		}
		if err := w.Flush(); err != nil {
			return err
		}
	}
	if report.Parts != nil {
		if err := printReportParts(stdout, report.Parts); err != nil {
			return err
		}
	}
	return printReportNotSearched(stdout, report)
}

func reportCell(text string, width int) string {
	value := []rune(plain(text))
	if len(value) > width {
		return string(value[:width-1]) + "…"
	}
	return string(value)
}

func printReportNotSearched(w io.Writer, report runReport) error {
	if err := printSearchedDirs(w, report.Searched); err != nil {
		return err
	}
	if report.NotSearchedTotal == 0 {
		return nil
	}
	_, err := fmt.Fprintf(w, "not searched: %d directories (see stderr)\n", report.NotSearchedTotal)
	return err
}

func reportRunLabel(ensemble, command string) string {
	if ensemble == "" && command == "tune verify" {
		return command
	}
	return reportKnown(ensemble)
}
