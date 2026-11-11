package loop

import "encoding/json"

func claudeUsage(data []byte, alreadyCounted map[string]struct{}) (Usage, []string, bool) {
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
		if _, counted := alreadyCounted[id]; !counted {
			messages[id] = m
		}
		return true
	})
	if !valid {
		return unknownUsage("no-usage"), nil, false
	}
	if len(messages) == 0 {
		return unknownUsage("no-usage"), nil, true
	}
	var usage Usage
	var ids []string
	for id, m := range messages {
		tokens := Tokens{*m.Usage.Input, *m.Usage.Output, m.Usage.CacheWrite, m.Usage.CacheRead}
		if !tokens.valid() {
			return unknownUsage("no-usage"), nil, false
		}
		usage.addModel(m.Model, tokens)
		ids = append(ids, id)
	}
	return usage, ids, true
}

func (m *LogMeter) claudeWindow(grown []usageGrowth, parent string) Usage {
	m.mu.Lock()
	defer m.mu.Unlock()
	var total Usage
	pending := make(map[string][]string)
	for _, g := range grown {
		if g.parent != parent {
			continue
		}
		usage, ids, valid := claudeUsage(g.data, m.counted[g.path])
		if !valid {
			return unknownUsage("no-usage")
		}
		if usage.Known {
			total.add(usage)
		}
		pending[g.path] = ids
	}
	if !total.Known {
		return unknownUsage("no-usage")
	}
	if m.counted == nil {
		m.counted = make(map[string]map[string]struct{})
	}
	for path, ids := range pending {
		if m.counted[path] == nil {
			m.counted[path] = make(map[string]struct{})
		}
		for _, id := range ids {
			m.counted[path][id] = struct{}{}
		}
	}
	return total
}
