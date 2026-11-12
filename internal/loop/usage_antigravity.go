package loop

import "encoding/json"

func antigravityUsage(data []byte) Usage {
	var usage Usage
	counted := make(map[int64]struct{})
	valid := usageLines(data, func(line []byte) bool {
		var entry struct {
			Type      string `json:"type"`
			Status    string `json:"status"`
			StepIndex *int64 `json:"step_index"`
			Input     *int64 `json:"input_tokens"`
			Output    *int64 `json:"output_tokens"`
			CacheRead int64  `json:"cache_read_tokens"`
		}
		if json.Unmarshal(line, &entry) != nil {
			return false
		}
		if entry.Type != "PLANNER_RESPONSE" || entry.Status != "DONE" {
			return true
		}
		if entry.Input == nil || entry.Output == nil || entry.StepIndex == nil || *entry.StepIndex < 0 {
			return false
		}
		tokens := Tokens{InputTokens: *entry.Input, OutputTokens: *entry.Output, CacheReadTokens: entry.CacheRead}
		if !tokens.valid() {
			return false
		}
		if _, ok := counted[*entry.StepIndex]; ok {
			return true
		}
		counted[*entry.StepIndex] = struct{}{}
		usage.addModel("", tokens)
		return true
	})
	if !valid || !usage.Known {
		return unknownUsage("no-usage")
	}
	return usage
}
