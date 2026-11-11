package loop

import "encoding/json"

func claudeUsage(data []byte) Usage {
	type message struct {
		ID    string `json:"id"`
		Model string `json:"model"`
		Usage *struct {
			Input      *int64 `json:"input_tokens"`
			Output     *int64 `json:"output_tokens"`
			CacheWrite int64  `json:"cache_creation_input_tokens"`
			CacheRead  int64  `json:"cache_read_input_tokens"`
		} `json:"usage"`
	}
	messages := make(map[string]message)
	valid := usageLines(data, func(line []byte) bool {
		var entry struct {
			Type      string  `json:"type"`
			RequestID string  `json:"requestId"`
			Message   message `json:"message"`
		}
		if json.Unmarshal(line, &entry) != nil {
			return false
		}
		m := entry.Message
		if entry.Type != "assistant" || m.Model == "<synthetic>" {
			return true
		}
		if m.Usage == nil {
			return true
		}
		if m.Usage.Input == nil || m.Usage.Output == nil {
			return false
		}
		id := m.ID
		if id == "" {
			id = entry.RequestID
		}
		if id == "" {
			return false
		}
		messages[id] = m
		return true
	})
	if !valid || len(messages) == 0 {
		return unknownUsage("no-usage")
	}
	var usage Usage
	for _, m := range messages {
		tokens := Tokens{*m.Usage.Input, *m.Usage.Output, m.Usage.CacheWrite, m.Usage.CacheRead}
		if !tokens.valid() {
			return unknownUsage("no-usage")
		}
		usage.addModel(m.Model, tokens)
	}
	return usage
}
