package main

import (
	"encoding/json"
	"fmt"
	"strings"
)

type reportTokens struct {
	InputTokens      int64 `json:"input_tokens"`
	OutputTokens     int64 `json:"output_tokens"`
	CacheReadTokens  int64 `json:"cache_read_tokens"`
	CacheWriteTokens int64 `json:"cache_write_tokens"`
}

type reportUsage struct {
	models           map[string]reportTokens
	modelsIncomplete bool
	Known            bool `json:"known"`
	UnknownSteps     int  `json:"unknown_steps,omitempty"`
	*reportTokens
}

type reportUsageTotal struct {
	*ReportCost
	models           map[string]reportTokens
	modelsIncomplete bool
	KnownRuns        int `json:"known_runs"`
	PartialRuns      int `json:"partial_runs,omitempty"`
	UnknownRuns      int `json:"unknown_runs"`
	reportTokens
}

func readReportUsage(data json.RawMessage) reportUsage {
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil {
		return reportUsage{}
	}
	var known bool
	if json.Unmarshal(fields["known"], &known) != nil || !known {
		return reportUsage{}
	}
	unknownSteps := 0
	if raw, ok := fields["unknown_steps"]; ok {
		var steps *int
		if json.Unmarshal(raw, &steps) != nil || steps == nil || *steps < 0 {
			return reportUsage{}
		}
		unknownSteps = *steps
	}
	tokens := reportTokens{}
	for name, target := range map[string]*int64{"input_tokens": &tokens.InputTokens, "output_tokens": &tokens.OutputTokens, "cache_read_tokens": &tokens.CacheReadTokens, "cache_write_tokens": &tokens.CacheWriteTokens} {
		var value *int64
		if json.Unmarshal(fields[name], &value) != nil || value == nil || *value < 0 || *value > 1<<36 {
			return reportUsage{}
		}
		*target = *value
	}
	usage := reportUsage{Known: true, UnknownSteps: unknownSteps, reportTokens: &tokens}
	usage.readModels(fields["models"])
	return usage
}

func (total *reportUsageTotal) add(usage reportUsage) {
	if !usage.Known {
		total.UnknownRuns++
		return
	}
	total.models = mergeReportModels(total.models, usage.models)
	total.modelsIncomplete = total.modelsIncomplete || usage.modelsIncomplete
	total.KnownRuns++
	if usage.UnknownSteps > 0 {
		total.PartialRuns++
	}
	total.InputTokens += usage.InputTokens
	total.OutputTokens += usage.OutputTokens
	total.CacheReadTokens += usage.CacheReadTokens
	total.CacheWriteTokens += usage.CacheWriteTokens
}

func addReportGroupUsage(groups map[string]reportUsageTotal, name string, usage reportUsage) {
	total := groups[name]
	total.add(usage)
	groups[name] = total
}

func compactTokens(n int64) string {
	for _, unit := range []struct {
		value  int64
		suffix string
	}{{1000000000, "B"}, {1000000, "M"}, {1000, "k"}} {
		if n >= unit.value {
			value := float64(n) / float64(unit.value)
			if value >= 100 {
				return fmt.Sprintf("%.0f%s", value, unit.suffix)
			}
			return fmt.Sprintf("%s%s", trimTokenDecimal(value), unit.suffix)
		}
	}
	return fmt.Sprint(n)
}

func trimTokenDecimal(n float64) string {
	text := fmt.Sprintf("%.1f", n)
	if text[len(text)-2:] == ".0" {
		return text[:len(text)-2]
	}
	return text
}

func reportUsageText(total reportUsageTotal) string {
	text := "unknown"
	if total.KnownRuns > 0 {
		bound := ""
		if total.PartialRuns > 0 {
			bound = "≥"
		}
		text = "in " + bound + compactTokens(total.InputTokens) + " out " + bound + compactTokens(total.OutputTokens)
	}
	var notes []string
	if total.PartialRuns > 0 {
		notes = append(notes, fmt.Sprintf("%d partly measured", total.PartialRuns))
	}
	if total.UnknownRuns > 0 {
		notes = append(notes, fmt.Sprintf("%d without usage", total.UnknownRuns))
	}
	if len(notes) > 0 {
		text += " (" + strings.Join(notes, ", ") + ")"
	}
	return text
}
