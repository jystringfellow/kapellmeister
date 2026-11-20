package main

import (
	"encoding/json"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jystringfellow/kapellmeister/internal/loop"
)

type statusRecord struct {
	Part  string      `json:"part"`
	Chair *loop.Chair `json:"chair"`
	reportPartRecord
	At          time.Time `json:"at"`
	TerminalID  string    `json:"terminal_id"`
	Kept        bool      `json:"kept"`
	BlockedStep string    `json:"blocked_step"`
	Detail      string    `json:"detail"`
	ExitCode    *int      `json:"exit_code"`
}

func readStatusRecords(dir *os.File) ([]statusRecord, []string) {
	var records []statusRecord
	diagnostics := readTelemetry(dir, func(line []byte) bool {
		var record *statusRecord
		if json.Unmarshal(line, &record) != nil || record == nil || record.Round < 0 {
			return false
		}
		records = append(records, *record)
		return true
	})
	return records, diagnostics
}

type statusSeating struct {
	Name     string          `json:"name"`
	Source   string          `json:"source"`
	Sections []statusSection `json:"sections"`
}

type statusSection struct {
	Name   string        `json:"name"`
	Chairs []statusChair `json:"chairs"`
}

type statusChair struct {
	Address    string           `json:"address"`
	Name       string           `json:"name"`
	Part       string           `json:"part"`
	Agent      string           `json:"agent"`
	Provider   string           `json:"provider"`
	Model      string           `json:"model"`
	Tier       string           `json:"tier"`
	State      string           `json:"state"`
	Step       string           `json:"step"`
	Since      time.Time        `json:"since"`
	Memory     string           `json:"memory"`
	Terminal   string           `json:"terminal"`
	NeedsInput statusNeedsInput `json:"needs_input"`
	Usage      statusUsage      `json:"usage"`
}

type statusNeedsInput struct {
	Count  int    `json:"count"`
	Reason string `json:"reason"`
}

type statusUsage struct {
	InputTokens  int64  `json:"input_tokens"`
	OutputTokens int64  `json:"output_tokens"`
	Measured     string `json:"measured"`
	known        int
	total        int
}

func (u *statusUsage) add(record statusRecord) {
	u.total++
	measured := readReportUsage(record.Usage)
	if measured.Known {
		u.total += measured.UnknownSteps
		u.known++
		u.InputTokens += measured.InputTokens
		u.OutputTokens += measured.OutputTokens
	}
	u.Measured = "none"
	if u.known > 0 {
		u.Measured = "some"
		if u.known == u.total {
			u.Measured = "all"
		}
	}
}

type statusCurrent struct {
	Step    string    `json:"step"`
	Round   int       `json:"round"`
	Chairs  []string  `json:"chairs"`
	Started time.Time `json:"started"`
}

type statusTrail struct {
	Step       string `json:"step"`
	Round      int    `json:"round"`
	Exit       string `json:"exit"`
	Chair      string `json:"chair"`
	DurationMS int64  `json:"duration_ms"`
}

func statusStepName(name string) string {
	if name == "verify-baseline" {
		return "verify"
	}
	if tail, ok := strings.CutPrefix(name, "verify-"); ok {
		if _, err := strconv.Atoi(tail); err == nil {
			return "verify"
		}
	}
	return name
}

func statusChairFor(chairs []*statusChair, steps map[string]loop.StepOutline, step, address, name, part string) *statusChair {
	if address != "" {
		for _, chair := range chairs {
			if chair.Address == address {
				return chair
			}
		}
		return nil
	}
	if part == "" {
		part = steps[strings.TrimSuffix(step, "-nudge")].Part
	}
	if name != "" {
		var found *statusChair
		for _, chair := range chairs {
			if (chair.Name == name || chair.Address == name) && (part == "" || chair.Part == part) {
				if found != nil {
					return nil
				}
				found = chair
			}
		}
		if found != nil {
			return found
		}
	}
	if ordinal, ok := strings.CutPrefix(name, part+"-"); ok && part != "" {
		if index, err := strconv.Atoi(ordinal); err == nil && index > 0 {
			for _, chair := range chairs {
				if chair.Part == part {
					index--
					if index == 0 {
						return chair
					}
				}
			}
		}
	}
	if part == "" {
		return nil
	}
	var found *statusChair
	for _, chair := range chairs {
		if chair.Part == part {
			if found != nil {
				return nil
			}
			found = chair
		}
	}
	return found
}

func statusRecordChair(chairs []*statusChair, steps map[string]loop.StepOutline, record statusRecord) *statusChair {
	address := ""
	if record.Chair != nil {
		address = record.Chair.Address
	}
	step := record.Step
	if record.BlockedStep != "" {
		step = record.BlockedStep
	}
	return statusChairFor(chairs, steps, step, address, record.ChairName, record.Part)
}

func addStatusData(run *statusRun, recorded reportRun) {
	run.Schema = 1
	run.Current = []statusCurrent{}
	run.Trail = []statusTrail{}
	state := recorded.state
	if state == nil {
		return
	}
	run.CAO, run.Score = statusServerAddress(state.CAO), state.Score
	steps := map[string]loop.StepOutline{}
	parts := map[string]bool{}
	if run.Score != nil {
		for _, step := range run.Score.Steps {
			steps[step.Name] = step
			parts[step.Part] = true
		}
	}
	var chairs []*statusChair
	if state.Seating != nil {
		seating := *state.Seating
		seating.Sections = append([]statusSection{}, seating.Sections...)
		for i := range seating.Sections {
			section := &seating.Sections[i]
			section.Chairs = append([]statusChair{}, section.Chairs...)
			for j := range section.Chairs {
				chair := &section.Chairs[j]
				chair.State = "unknown"
				chair.Since = state.Started
				chair.Usage = statusUsage{Measured: "none"}
				chairs = append(chairs, chair)
			}
		}
		run.Seating = &seating
	}
	verdicts := map[reportVerdictKey]string{}
	for _, record := range recorded.records {
		if subject, ok := strings.CutPrefix(record.Step, "verdict-"); ok {
			verdicts[reportVerdictKey{subject, record.Round, record.ChairName}] = strings.ToLower(record.Verdict)
		}
	}
	type blockedRecord struct {
		record statusRecord
		order  int
	}
	blocked := map[string]blockedRecord{}
	last := map[*statusChair]statusRecord{}
	finished := map[string]bool{}
	for order, record := range recorded.records {
		chair := statusRecordChair(chairs, steps, record)
		switch record.Step {
		case "blocked":
			blocked[record.TerminalID] = blockedRecord{record, order}
			continue
		case "unblocked":
			delete(blocked, record.TerminalID)
			continue
		case "escalate":
			if chair != nil {
				chair.Terminal = ""
				updateStatusBinding(chair, record)
			}
			continue
		case "restore":
			continue
		}
		if record.Chair != nil || len(record.Usage) > 0 {
			if run.Usage == nil {
				run.Usage = &statusUsage{Measured: "none"}
			}
			run.Usage.add(record)
		}
		if chair != nil && record.Chair != nil {
			chair.Usage.add(record)
			updateStatusBinding(chair, record)
			chair.Step, chair.Since = record.Step, record.At
			if record.Kept {
				chair.Terminal = record.TerminalID
			} else if state.Command != "play" {
				chair.Terminal = ""
			}
			last[chair] = record
		}
		name := statusStepName(record.Step)
		step, exists := steps[name]
		if !exists || record.Error != "" || strings.HasSuffix(record.Step, "-nudge") {
			continue
		}
		if step.Part != "" && record.Chair == nil {
			continue
		}
		entry := statusTrail{Step: name, Round: record.Round, DurationMS: 0}
		if record.DurationMS != nil {
			entry.DurationMS = *record.DurationMS
		}
		if chair != nil {
			entry.Chair = chair.Address
		}
		switch step.Kind {
		case "produce":
			entry.Exit = "done"
		case "verify", "strict":
			if record.ExitCode != nil {
				entry.Exit = "pass"
				if *record.ExitCode != 0 {
					entry.Exit = "fail"
				}
			}
		default:
			entry.Exit = verdicts[reportVerdictKey{record.Step, record.Round, record.ChairName}]
		}
		run.Trail = append(run.Trail, entry)
		finished[name] = true
	}
	if run.Score == nil {
		return
	}
	playing := map[*statusChair]bool{}
	for _, active := range state.Playing {
		name := statusStepName(active.Step)
		chair := statusChairFor(chairs, steps, active.Step, active.Chair, active.ChairName, active.Part)
		if chair != nil {
			chair.Step, chair.Since = active.Step, active.Started
			if run.Liveness != "abandoned" {
				chair.State = "playing"
			}
			if active.Terminal != "" || state.Command != "play" {
				chair.Terminal = active.Terminal
			}
			playing[chair] = true
		}
		if run.Liveness == "abandoned" {
			continue
		}
		index := -1
		for i, current := range run.Current {
			if current.Step == name && current.Round == active.Round {
				index = i
				break
			}
		}
		if index < 0 {
			run.Current = append(run.Current, statusCurrent{Step: name, Round: active.Round, Chairs: []string{}, Started: active.Started})
			index = len(run.Current) - 1
		}
		current := &run.Current[index]
		if active.Started.Before(current.Started) {
			current.Started = active.Started
		}
		if chair != nil {
			current.Chairs = append(current.Chairs, chair.Address)
		}
	}
	sort.SliceStable(run.Current, func(i, j int) bool { return run.Current[i].Started.Before(run.Current[j].Started) })
	reachable := statusReachable(run.Current, run.Trail, run.Score.Steps, finished)
	for _, chair := range chairs {
		if !playing[chair] && run.Score != nil && run.Liveness != "abandoned" {
			chair.State = "done"
			if _, played := last[chair]; !played {
				chair.State = "waiting"
			}
			for name := range reachable {
				if steps[name].Part == chair.Part || (!parts[chair.Part] && statusVerdictKind(steps[name].Kind)) {
					chair.State = "waiting"
				}
			}
		}
		latest := -1
		for _, block := range blocked {
			record := block.record
			if statusRecordChair(chairs, steps, record) != chair {
				continue
			}
			chair.NeedsInput.Count++
			if block.order > latest {
				chair.NeedsInput.Reason, chair.Terminal = record.Detail, record.TerminalID
				latest = block.order
			}
		}
	}
}

func statusReachable(current []statusCurrent, trail []statusTrail, scoreSteps []loop.StepOutline, finished map[string]bool) map[string]bool {
	steps := map[string]loop.StepOutline{}
	first := ""
	for _, step := range scoreSteps {
		steps[step.Name] = step
		if first == "" && step.Part != "" {
			first = step.Name
		}
	}
	normalize := func(name string) string {
		if _, ok := steps[name]; ok {
			return name
		}
		name = strings.TrimSuffix(name, "-nudge")
		if _, ok := steps[name]; ok {
			return name
		}
		return strings.TrimPrefix(name, "classify-")
	}

	reachable := map[string]bool{}
	var visit func(string)
	visit = func(name string) {
		step, ok := steps[name]
		if !ok || reachable[name] {
			return
		}
		reachable[name] = true
		for _, next := range step.Next {
			visit(next)
		}
	}
	foreground := false
	for _, active := range current {
		name := normalize(active.Step)
		visit(name)
		foreground = foreground || len(steps[name].Next) > 0
	}
	if !foreground {
		for i := len(trail) - 1; i >= 0; i-- {
			entry := trail[i]
			if target := steps[entry.Step].Next[entry.Exit]; target != "" {
				visit(target)
				foreground = true
				break
			}
		}
	}
	if !foreground {
		visit(first)
	}
	for name, step := range steps {
		if step.Part != "" && len(step.Next) == 0 && !finished[name] {
			visit(name)
		}
	}
	return reachable
}

func statusVerdictKind(kind string) bool {
	return kind != "produce" && kind != "verify" && kind != "strict" && kind != ""
}

func updateStatusBinding(chair *statusChair, record statusRecord) {
	if record.Chair == nil {
		return
	}
	chair.Tier = firstNonEmpty(record.Tier, record.Chair.Tier, chair.Tier)
	model := record.Chair.Model
	if record.ModelHonored != nil && !*record.ModelHonored && record.Model != "" {
		model = record.Model
	}
	chair.Model = firstNonEmpty(model, record.Model, chair.Model)
}
