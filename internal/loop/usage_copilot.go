package loop

import "encoding/json"

func copilotUsage(data []byte, fallback string) Usage {
	var usage Usage
	counted := make(map[string]struct{})
	valid := usageLines(data, func(line []byte) bool {
		var entry struct {
			Type string `json:"type"`
			Data struct {
				Usage *struct {
					Model         string `json:"model"`
					ResponseModel string `json:"responseModel"`
					Input         *int64 `json:"inputTokens"`
					Output        *int64 `json:"outputTokens"`
					CacheRead     *int64 `json:"cacheReadTokens"`
					CacheWrite    *int64 `json:"cacheWriteTokens"`
					Reasoning     *int64 `json:"reasoningTokens"`
					APICallID     string `json:"apiCallId"`
					Accounting    struct {
						SourceSessionID string `json:"sourceSessionId"`
						UsageID         string `json:"usageId"`
					} `json:"accounting"`
				} `json:"usage"`
			} `json:"data"`
		}
		if json.Unmarshal(line, &entry) != nil {
			return false
		}
		if entry.Type != "session.usage_record" {
			return true
		}
		u := entry.Data.Usage
		if u == nil || u.Input == nil || u.Output == nil || u.CacheRead == nil || u.CacheWrite == nil || u.Reasoning == nil || *u.Reasoning < 0 {
			return false
		}
		tokens := Tokens{*u.Input, *u.Output, *u.CacheWrite, *u.CacheRead}
		if !tokens.valid() || tokens.CacheReadTokens > tokens.InputTokens || tokens.CacheWriteTokens > tokens.InputTokens-tokens.CacheReadTokens {
			return false
		}
		tokens.InputTokens -= tokens.CacheReadTokens + tokens.CacheWriteTokens
		id := u.Accounting.UsageID
		if id == "" {
			id = u.APICallID
		}
		if id != "" {
			key := u.Accounting.SourceSessionID + "\x00" + id
			if _, ok := counted[key]; ok {
				return true
			}
			counted[key] = struct{}{}
		}
		model := u.ResponseModel
		if model == "" {
			model = u.Model
		}
		usage.addModel(modelOr(model, fallback), tokens)
		return true
	})
	if !valid || !usage.Known {
		return unknownUsage("no-usage")
	}
	return usage
}
