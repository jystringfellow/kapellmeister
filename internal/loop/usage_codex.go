package loop

import "encoding/json"

func codexUsage(data, head []byte, fallback string) Usage {
	type counts struct {
		Input      *int64 `json:"input_tokens"`
		Output     *int64 `json:"output_tokens"`
		Cached     int64  `json:"cached_input_tokens"`
		CacheWrite int64  `json:"cache_write_input_tokens"`
	}
	normalize := func(c counts) (Tokens, bool) {
		if c.Input == nil || c.Output == nil {
			return Tokens{}, false
		}
		tokens := Tokens{*c.Input - c.Cached, *c.Output, c.CacheWrite, c.Cached}
		return tokens, tokens.valid()
	}
	model := ""
	usageLines(head, func(line []byte) bool {
		var entry struct {
			Type    string `json:"type"`
			Payload struct {
				Model string `json:"model"`
			} `json:"payload"`
		}
		if json.Unmarshal(line, &entry) == nil && entry.Type == "turn_context" {
			model = entry.Payload.Model
		}
		return true
	})
	var first, last Tokens
	found := false
	valid := usageLines(data, func(line []byte) bool {
		var entry struct {
			Type    string `json:"type"`
			Payload struct {
				Type  string `json:"type"`
				Model string `json:"model"`
				Info  *struct {
					Total counts `json:"total_token_usage"`
					Last  counts `json:"last_token_usage"`
				} `json:"info"`
			} `json:"payload"`
		}
		if json.Unmarshal(line, &entry) != nil {
			return false
		}
		if entry.Type == "turn_context" {
			model = entry.Payload.Model
			return true
		}
		p := entry.Payload
		if entry.Type != "event_msg" || p.Type != "token_count" || p.Info == nil {
			return true
		}
		total, ok := normalize(p.Info.Total)
		if !ok {
			return false
		}
		if !found {
			previous, ok := normalize(p.Info.Last)
			if !ok {
				return false
			}
			first = total.subtract(previous)
			if !first.valid() {
				return false
			}
		} else if !total.subtract(last).valid() {
			return false
		}
		last = total
		found = true
		return true
	})
	if !valid || !found {
		return unknownUsage("no-usage")
	}
	tokens := last.subtract(first)
	if !tokens.valid() {
		return unknownUsage("no-usage")
	}
	var usage Usage
	usage.addModel(modelOr(model, fallback), tokens)
	return usage
}
