package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/jystringfellow/kapellmeister/internal/loop"
	"golang.org/x/sys/unix"
)

const reportTelemetryLimit = 64 << 20

var reportPartOrder = []string{"planner", "implementer", "reviewer", "gate", "tester", "classifier"}

type reportPartKey struct{ part, provider, model string }
type reportVerdictKey struct {
	subject string
	round   int
	chair   string
}
type reportPartRun struct {
	outline              *loop.ScoreOutline
	partKinds            map[string]map[string]bool
	rows                 map[reportPartKey]*reportPartRow
	steps                map[reportVerdictKey]*reportPartKey
	verdicts             map[reportVerdictKey]string
	unattributedSteps    int
	unattributedVerdicts int
}
type reportParts struct {
	Rows                 []*reportPartRow `json:"rows"`
	UnattributedSteps    int              `json:"unattributed_steps"`
	UnattributedVerdicts int              `json:"unattributed_verdicts"`
	RunsWithoutSteps     int              `json:"runs_without_steps"`
}
type reportPartUsagePerStep struct {
	InputTokens  float64 `json:"input_tokens"`
	OutputTokens float64 `json:"output_tokens"`
}
type reportPartRow struct {
	workPart string
	*ReportCost
	UnpricedModels          []string                `json:"unpriced_models,omitempty"`
	Part                    string                  `json:"part"`
	Provider                string                  `json:"provider"`
	Model                   string                  `json:"model"`
	Runs                    int                     `json:"runs"`
	Steps                   int                     `json:"steps"`
	StepErrors              int                     `json:"step_errors"`
	DurationMS              float64                 `json:"duration_ms"`
	UnmeasuredDurationSteps int                     `json:"unmeasured_duration_steps"`
	DurationMSPerStep       *float64                `json:"duration_ms_per_step,omitempty"`
	Usage                   reportUsage             `json:"usage"`
	UsagePerStep            *reportPartUsagePerStep `json:"usage_per_step,omitempty"`
	Efforts                 map[string]int          `json:"efforts,omitempty"`
	Tiers                   map[string]int          `json:"tiers,omitempty"`
	Work                    map[string]float64      `json:"work,omitempty"`
}
type reportPartRecord struct {
	Part        string `json:"part"`
	BlockedStep string `json:"blocked_step"`
	Step        string `json:"step"`
	Round       int    `json:"round"`
	ChairName   string `json:"chair_name"`
	Chair       *struct {
		Address  string `json:"address"`
		Provider string `json:"provider"`
		Profile  string `json:"profile"`
		Model    string `json:"model"`
	} `json:"chair"`
	Model        string          `json:"model"`
	ModelHonored *bool           `json:"model_honored"`
	DurationMS   *int64          `json:"duration_ms"`
	Usage        json.RawMessage `json:"usage"`
	Effort       string          `json:"effort"`
	Tier         string          `json:"tier"`
	Verdict      string          `json:"verdict"`
	Error        string          `json:"error"`
	Lessons      json.RawMessage `json:"lessons"`
	TreeChanged  bool            `json:"tree_changed"`
}

func readReportParts(dir *os.File) (*reportPartRun, []string) {
	run := &reportPartRun{rows: map[reportPartKey]*reportPartRow{}, steps: map[reportVerdictKey]*reportPartKey{}, verdicts: map[reportVerdictKey]string{}}
	run.partKinds = map[string]map[string]bool{}
	if data, err := readReportFile(dir, "current.json", false); err == nil {
		var state struct {
			Score *loop.ScoreOutline `json:"score"`
		}
		if json.Unmarshal(data, &state) == nil {
			run.outline = state.Score
		}
	}
	diagnostics := readTelemetry(dir, func(line []byte) bool {
		var record *reportPartRecord
		if json.Unmarshal(line, &record) != nil || record == nil || record.Round < 0 {
			return false
		}
		run.addRecord(*record)
		return true
	})
	for key, row := range run.rows {
		row.setWorkPart(run.workPart(key.part))
	}
	run.addVerdicts()
	return run, diagnostics
}

func readTelemetry(dir *os.File, add func([]byte) bool) []string {
	path := filepath.Join(dir.Name(), "telemetry.jsonl")
	diagnose := func(err error) []string {
		return []string{"km: report: " + plain(path) + ": " + plain(err.Error())}
	}
	var stat unix.Stat_t
	if err := unix.Fstatat(int(dir.Fd()), "telemetry.jsonl", &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return diagnose(err)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG {
		return diagnose(errors.New("not a regular file"))
	}
	file, err := openReportEntry(dir, "telemetry.jsonl", 0)
	if err != nil {
		return diagnose(err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return diagnose(err)
	}
	if !info.Mode().IsRegular() {
		return diagnose(errors.New("not a regular file"))
	}
	reader := bufio.NewReader(io.LimitReader(file, reportTelemetryLimit+1))
	var line []byte
	consumed, lineBytes, skipped := 0, 0, 0
	var diagnostics []string
	for {
		chunk, err := reader.ReadSlice('\n')
		consumed += len(chunk)
		lineBytes += len(chunk)
		if consumed > reportTelemetryLimit {
			diagnostics = append(diagnostics, "km: report: "+plain(path)+": telemetry exceeds 64 MiB; remaining records were not read")
			break
		}
		if lineBytes <= reportFileLimit {
			line = append(line, chunk...)
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if err != nil && !errors.Is(err, io.EOF) {
			diagnostics = append(diagnostics, "km: report: "+plain(path)+": "+plain(err.Error()))
			break
		}
		if lineBytes > reportFileLimit {
			skipped++
		} else if len(bytes.TrimSpace(line)) > 0 {
			if !add(line) && !errors.Is(err, io.EOF) {
				skipped++
			}
		}
		line = line[:0]
		lineBytes = 0
		if errors.Is(err, io.EOF) {
			break
		}
	}
	if skipped > 0 {
		diagnostics = append(diagnostics, fmt.Sprintf("km: report: %s: %d telemetry lines could not be read and were skipped", plain(path), skipped))
	}
	return diagnostics
}

func reportStepPart(step string) string {
	step = strings.TrimSuffix(step, "-nudge")
	switch step {
	case "plan", "implement", "review", "gate", "test-plan", "test", "lessons", "classify-review", "classify-gate", "classify-test":
		return loop.PartOf(step)
	}
	return ""
}
func (run *reportPartRun) step(name string) (part, kind string) {
	name = strings.TrimSuffix(name, "-nudge")
	if run.outline != nil {
		for _, step := range run.outline.Steps {
			if step.Name == name {
				return step.Part, step.Kind
			}
		}
	}
	switch name {
	case "review":
		kind = "review"
	case "gate":
		kind = "decide"
	case "test":
		kind = "test"
	case "plan", "implement", "test-plan":
		kind = "produce"
	}
	if strings.HasPrefix(name, "classify-") {
		return "classifier", kind
	}
	return reportStepPart(name), kind
}

func (run *reportPartRun) recordPart(record reportPartRecord) (part, kind string) {
	if record.BlockedStep != "" {
		return "", ""
	}
	switch record.Step {
	case "reseat", "escalate", "blocked", "unblocked", "start", "verify", "strict":
		return "", ""
	}
	part, kind = run.step(record.Step)
	if kind == "verify" || kind == "strict" {
		return "", ""
	}
	known := part != "" || kind != ""
	if record.Part != "" && (known || (run.outline == nil && record.Chair != nil)) {
		part = record.Part
	}
	return part, kind
}

func (run *reportPartRun) workPart(part string) string {
	if part == "classifier" {
		return ""
	}
	if run.outline == nil {
		if part == "planner" {
			return "implementer"
		}
		return part
	}
	kinds := run.partKinds[part]
	available := map[string]bool{}
	for _, step := range run.outline.Steps {
		available[step.Kind] = true
	}
	switch {
	case kinds["test"]:
		if available["review"] && available["decide"] {
			return "tester"
		}
	case kinds["review"]:
		if available["decide"] && available["test"] {
			return "reviewer"
		}
	case kinds["decide"]:
		if available["test"] {
			return "gate"
		}
	case len(kinds) == 1 && kinds["produce"]:
		if run.outline.Rounds > 0 {
			return "implementer"
		}
	}
	return ""
}

func newReportPartRow(key reportPartKey) *reportPartRow {
	return &reportPartRow{Part: key.part, Provider: key.provider, Model: key.model, Efforts: map[string]int{}, Tiers: map[string]int{}}
}
func (row *reportPartRow) setWorkPart(part string) {
	row.workPart, row.Work = "", nil
	if columns := reportWorkColumns(part); len(columns) > 0 {
		row.workPart = part
		row.Work = map[string]float64{}
		for _, column := range columns {
			for _, key := range column.keys {
				row.Work[key] = 0
			}
		}
	}
}
func reportVerdictKind(kind string) bool {
	return kind == "review" || kind == "decide" || kind == "test"
}

func (run *reportPartRun) addRecord(record reportPartRecord) {
	subject := strings.TrimPrefix(record.Step, "verdict-")
	if subject != record.Step {
		_, kind := run.step(subject)
		if !reportVerdictKind(kind) {
			return
		}
		if record.Round > 0 && record.Verdict != "" {
			run.verdicts[reportVerdictKey{subject, record.Round, record.ChairName}] = record.Verdict
		} else {
			delete(run.verdicts, reportVerdictKey{subject, record.Round, record.ChairName})
			run.unattributedVerdicts++
		}
		return
	}
	part, kind := run.recordPart(record)
	if part == "" {
		return
	}
	verdictStep := record.Round > 0 && reportVerdictKind(kind) && !strings.HasSuffix(record.Step, "-nudge")
	if verdictStep {
		run.steps[reportVerdictKey{record.Step, record.Round, record.ChairName}] = nil
	}
	if record.Chair == nil {
		if record.Step != "lessons" || (len(record.Lessons) == 0 && !record.TreeChanged) {
			run.unattributedSteps++
		}
		return
	}
	if run.partKinds[part] == nil {
		run.partKinds[part] = map[string]bool{}
	}
	run.partKinds[part][kind] = true
	model := record.Chair.Model
	if record.ModelHonored != nil && !*record.ModelHonored && record.Model != "" {
		model = record.Model
	}
	if model == "" {
		model = record.Model
	}
	key := reportPartKey{part, reportKnown(record.Chair.Provider), reportKnown(model)}
	row := run.rows[key]
	if row == nil {
		row = newReportPartRow(key)
		run.rows[key] = row
	}
	row.Steps++
	if record.Error != "" {
		row.StepErrors++
	}
	if record.DurationMS != nil && *record.DurationMS >= 0 {
		row.DurationMS += float64(*record.DurationMS)
	} else {
		row.UnmeasuredDurationSteps++
	}
	usage := readReportUsage(record.Usage)
	if !usage.Known || usage.UnknownSteps > 0 {
		usage.UnknownSteps = 1
	}
	row.addUsage(usage)
	effort, tier := record.Effort, record.Tier
	if effort == "" {
		effort = "unset"
	}
	if tier == "" {
		tier = "unset"
	}
	row.Efforts[effort]++
	row.Tiers[tier]++
	if verdictStep {
		run.steps[reportVerdictKey{record.Step, record.Round, record.ChairName}] = &key
	}
}
func (row *reportPartRow) addUsage(usage reportUsage) {
	row.Usage.UnknownSteps += usage.UnknownSteps
	if !usage.Known {
		return
	}
	if row.Usage.reportTokens == nil {
		row.Usage.reportTokens = &reportTokens{}
	}
	row.Usage.models = mergeReportModels(row.Usage.models, usage.models)
	row.Usage.modelsIncomplete = row.Usage.modelsIncomplete || usage.modelsIncomplete
	row.Usage.Known = true
	row.Usage.InputTokens += usage.InputTokens
	row.Usage.OutputTokens += usage.OutputTokens
	row.Usage.CacheReadTokens += usage.CacheReadTokens
	row.Usage.CacheWriteTokens += usage.CacheWriteTokens
}

type reportRoundFacts struct {
	gate, test                  string
	gateRan, testRan            bool
	reviews, reviewsNotApproved int
}

func (run *reportPartRun) addVerdicts() {
	rounds := map[int]*reportRoundFacts{}
	for key, verdict := range run.verdicts {
		facts := rounds[key.round]
		if facts == nil {
			facts = &reportRoundFacts{}
			rounds[key.round] = facts
		}
		_, kind := run.step(key.subject)
		switch kind {
		case "review":
			facts.reviews++
			if verdict != "APPROVE" {
				facts.reviewsNotApproved++
			}
		case "decide":
			if key.chair == "" {
				facts.gate, facts.gateRan = verdict, true
			}
		case "test":
			if key.chair == "" {
				facts.test, facts.testRan = verdict, true
			}
		}
	}
	for key := range run.steps {
		_, kind := run.step(key.subject)
		if kind != "review" {
			continue
		}
		if _, recorded := run.verdicts[key]; recorded {
			continue
		}
		facts := rounds[key.round]
		if facts == nil {
			facts = &reportRoundFacts{}
			rounds[key.round] = facts
		}
		facts.reviewsNotApproved++
	}
	for key, verdict := range run.verdicts {
		rowKey := run.steps[key]
		if rowKey == nil {
			continue
		}
		work := run.rows[*rowKey].Work
		if work == nil {
			continue
		}
		_, kind := run.step(key.subject)
		facts := rounds[key.round]
		counter := "unclear"
		switch verdict {
		case "APPROVE":
			counter = "approve"
		case "CHANGES":
			counter = "changes"
		case "FAILED":
			if kind == "review" {
				counter = "failed"
			}
		}
		work[counter]++
		if verdict == "APPROVE" {
			if kind == "review" && facts.gateRan {
				work["approve_gate_ran"]++
				if facts.gate != "APPROVE" {
					work["approve_then_gate_not_approved"]++
				}
			}
			if (kind == "review" || kind == "decide") && facts.testRan {
				work["approve_test_ran"]++
				if facts.test != "APPROVE" {
					work["approve_then_test_not_approved"]++
				}
			}
		}
		if kind == "review" && verdict == "CHANGES" && facts.gateRan {
			work["changes_gate_ran"]++
			if facts.gate == "APPROVE" {
				work["changes_then_gate_approved"]++
			}
		}
		if kind == "test" && facts.gate == "APPROVE" && facts.reviews > 0 && facts.reviewsNotApproved == 0 {
			work["all_approved_rounds_tested"]++
			if verdict != "APPROVE" {
				work["not_approved_after_all_approved"]++
			}
		}
	}
}
func mergeReportParts(runs []reportRun) *reportParts {
	parts := &reportParts{Rows: []*reportPartRow{}}
	rows := map[reportPartKey]*reportPartRow{}
	for _, run := range runs {
		if run.parts == nil || len(run.parts.rows) == 0 {
			parts.RunsWithoutSteps++
		}
		if run.parts == nil {
			continue
		}
		parts.UnattributedSteps += run.parts.unattributedSteps
		parts.UnattributedVerdicts += run.parts.unattributedVerdicts
		for key := range run.parts.verdicts {
			if run.parts.steps[key] == nil {
				parts.UnattributedVerdicts++
			}
		}
		partModels := map[string]int{}
		for key := range run.parts.rows {
			partModels[key.part]++
		}
		for key, played := range run.parts.rows {
			row := rows[key]
			if row == nil {
				row = newReportPartRow(key)
				row.setWorkPart(played.workPart)
				rows[key] = row
				parts.Rows = append(parts.Rows, row)
			}
			row.Runs++
			row.Steps += played.Steps
			row.StepErrors += played.StepErrors
			row.DurationMS += played.DurationMS
			row.UnmeasuredDurationSteps += played.UnmeasuredDurationSteps
			row.addUsage(played.Usage)
			for effort, n := range played.Efforts {
				row.Efforts[effort] += n
			}
			for tier, n := range played.Tiers {
				row.Tiers[tier] += n
			}
			if row.workPart != played.workPart {
				row.setWorkPart("")
			}
			if row.Work == nil {
				continue
			}
			for counter, n := range played.Work {
				row.Work[counter] += n
			}
			if row.workPart == "implementer" {
				row.addRunWork(run, partModels[key.part] > 1)
			}
		}
	}
	for _, row := range parts.Rows {
		if len(row.Efforts) < 2 {
			row.Efforts = nil
		}
		if len(row.Tiers) < 2 {
			row.Tiers = nil
		}
		if row.UnmeasuredDurationSteps < row.Steps {
			average := row.DurationMS / float64(row.Steps)
			row.DurationMSPerStep = &average
		}
		if row.Usage.Known {
			row.UsagePerStep = &reportPartUsagePerStep{float64(row.Usage.InputTokens) / float64(row.Steps), float64(row.Usage.OutputTokens) / float64(row.Steps)}
		}
	}
	order := func(part string) int {
		for i, p := range reportPartOrder {
			if p == part {
				return i
			}
		}
		return len(reportPartOrder)
	}
	sort.Slice(parts.Rows, func(i, j int) bool {
		a, b := parts.Rows[i], parts.Rows[j]
		if a.Part != b.Part {
			if order(a.Part) != order(b.Part) {
				return order(a.Part) < order(b.Part)
			}
			return a.Part < b.Part
		}
		if a.Provider != b.Provider {
			return a.Provider < b.Provider
		}
		return a.Model < b.Model
	})
	return parts
}
func (row *reportPartRow) addRunWork(run reportRun, split bool) {
	work := row.Work
	switch {
	case split:
		work["split_runs"]++
	case !run.hasResult:
		work["unfinished"]++
	case run.quality < 2:
		work["unreadable_results"]++
	case run.Outcome != "approved":
		work["not_approved"]++
	case run.Rounds == nil || *run.Rounds < 1:
		work["approved_rounds_unknown"]++
	default:
		if *run.Rounds == 1 {
			work["approved_first_round"]++
		} else {
			work["approved_later_rounds"]++
		}
		work["rounds_in_approved_runs"] += float64(*run.Rounds)
	}
}

type reportWorkColumn struct {
	title string
	keys  []string
}

func reportWorkColumns(part string) []reportWorkColumn {
	verdicts := []reportWorkColumn{{"APPROVE", []string{"approve"}}, {"CHANGES", []string{"changes"}}, {"UNCLEAR", []string{"unclear"}}}
	switch part {
	case "planner", "implementer":
		return []reportWorkColumn{
			{"FIRST", []string{"approved_first_round"}}, {"LATER", []string{"approved_later_rounds"}}, {"ROUNDS?", []string{"approved_rounds_unknown"}}, {"NOT APPROVED", []string{"not_approved"}}, {"UNFINISHED", []string{"unfinished"}}, {"RESULT?", []string{"unreadable_results"}}, {"SPLIT", []string{"split_runs"}}, {"ROUNDS/APPROVED", []string{"rounds_in_approved_runs", "approved_first_round", "approved_later_rounds"}},
		}
	case "reviewer":
		return append(verdicts, reportWorkColumn{"FAILED", []string{"failed"}}, reportWorkColumn{"APPROVE→GATE!", []string{"approve_then_gate_not_approved", "approve_gate_ran"}}, reportWorkColumn{"APPROVE→TEST!", []string{"approve_then_test_not_approved", "approve_test_ran"}}, reportWorkColumn{"CHANGES→GATE+", []string{"changes_then_gate_approved", "changes_gate_ran"}})
	case "gate":
		return append(verdicts, reportWorkColumn{"APPROVE→TEST!", []string{"approve_then_test_not_approved", "approve_test_ran"}})
	case "tester":
		return append(verdicts, reportWorkColumn{"ALL+→TEST!", []string{"not_approved_after_all_approved", "all_approved_rounds_tested"}})
	}
	return nil
}
func reportPartTime(ms float64) string {
	if ms > float64(1<<63-1)/float64(time.Millisecond) {
		return strconv.FormatFloat(ms/1000, 'g', 6, 64) + "s"
	}
	return (time.Duration(ms) * time.Millisecond).Round(time.Millisecond).String()
}
func reportPartTokens(input, output float64, partial bool) string {
	bound := ""
	if partial {
		bound = "≥"
	}
	token := func(n float64) string {
		if n < 1000 {
			return strconv.FormatFloat(n, 'f', 1, 64)
		}
		return compactTokens(int64(n))
	}
	if input == float64(int64(input)) && output == float64(int64(output)) {
		return bound + compactTokens(int64(input)) + "/" + compactTokens(int64(output))
	}
	return bound + token(input) + "/" + token(output)
}
func printReportParts(stdout io.Writer, parts *reportParts) error {
	w := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "\nby part and model")
	header := "PART\tPROVIDER\tMODEL\tRUNS\tSTEPS\tERRORS\tTIME\tTIME/STEP\tTOKENS\tTOKENS/STEP"
	if len(parts.Rows) > 0 && parts.Rows[0].ReportCost != nil {
		header += "\tCOST"
	}
	fmt.Fprintln(w, header)
	for _, row := range parts.Rows {
		total, average, tokens, tokensAverage := "unknown", "unknown", "unknown", "unknown"
		if row.DurationMSPerStep != nil {
			bound := ""
			if row.UnmeasuredDurationSteps > 0 {
				bound = "≥"
			}
			total = bound + reportPartTime(row.DurationMS)
			average = bound + reportPartTime(*row.DurationMSPerStep)
		}
		if row.Usage.Known {
			tokens = reportPartTokens(float64(row.Usage.InputTokens), float64(row.Usage.OutputTokens), row.Usage.UnknownSteps > 0)
			tokensAverage = reportPartTokens(row.UsagePerStep.InputTokens, row.UsagePerStep.OutputTokens, row.Usage.UnknownSteps > 0)
		}
		costText := ""
		if row.ReportCost != nil {
			costText = "\t" + row.ReportCost.text()
			if row.CostUSD == nil && len(row.UnpricedModels) > 0 {
				costText = "\tcost unknown"
			}
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%d\t%d\t%s\t%s\t%s\t%s%s\n", plain(row.Part), reportCell(row.Provider, 20), reportCell(row.Model, 32), row.Runs, row.Steps, row.StepErrors, total, average, tokens, tokensAverage, costText)
	}
	if err := w.Flush(); err != nil {
		return err
	}
	seen := map[[2]string]bool{}
	for _, played := range parts.Rows {
		part := played.Part
		key := [2]string{part, played.workPart}
		if seen[key] {
			continue
		}
		seen[key] = true
		columns := reportWorkColumns(played.workPart)
		if len(columns) == 0 {
			continue
		}
		found := false
		for _, row := range parts.Rows {
			if row.Part != part || row.workPart != played.workPart {
				continue
			}
			if !found {
				fmt.Fprintf(w, "\nwork: %s\nPROVIDER\tMODEL", plain(part))
				for _, column := range columns {
					fmt.Fprintf(w, "\t%s", column.title)
				}
				fmt.Fprintln(w)
				found = true
			}
			fmt.Fprintf(w, "%s\t%s", reportCell(row.Provider, 20), reportCell(row.Model, 32))
			for _, column := range columns {
				text := fmt.Sprintf("%.0f", row.Work[column.keys[0]])
				if len(column.keys) == 2 {
					text += "/" + fmt.Sprintf("%.0f", row.Work[column.keys[1]])
				}
				if len(column.keys) == 3 {
					text += " over " + fmt.Sprintf("%.0f", row.Work[column.keys[1]]+row.Work[column.keys[2]]) + " runs"
				}
				fmt.Fprintf(w, "\t%s", text)
			}
			fmt.Fprintln(w)
		}
		if err := w.Flush(); err != nil {
			return err
		}
	}
	for _, row := range parts.Rows {
		for _, variation := range []struct {
			name   string
			counts map[string]int
		}{{"effort", row.Efforts}, {"tier", row.Tiers}} {
			if len(variation.counts) == 0 {
				continue
			}
			names := make([]string, 0, len(variation.counts))
			for name := range variation.counts {
				names = append(names, name)
			}
			sort.Strings(names)
			values := []string{}
			for _, name := range names {
				values = append(values, fmt.Sprintf("%s %d", plain(name), variation.counts[name]))
			}
			fmt.Fprintf(w, "%s varies: %s %s %s: %s\n", variation.name, plain(row.Part), plain(row.Provider), plain(row.Model), strings.Join(values, ", "))
		}
	}
	if parts.UnattributedSteps+parts.UnattributedVerdicts+parts.RunsWithoutSteps > 0 {
		fmt.Fprintf(w, "unattributed steps: %d; unattributed verdicts: %d; runs without steps: %d\n", parts.UnattributedSteps, parts.UnattributedVerdicts, parts.RunsWithoutSteps)
	}
	return w.Flush()
}
