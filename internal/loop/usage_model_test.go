package loop

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func modelUsageLog(t *testing.T, provider string) (*LogMeter, Step, string, []byte, Tokens, string) {
	t.Helper()
	if provider == "copilot_cli" || provider == "antigravity_cli" {
		m, s, p := otherUsageLog(t, provider)
		model := ""
		if provider == "copilot_cli" {
			model = "claude-sonnet-5-5"
		}
		return m, s, p, otherFixture(t, provider, s), otherTokens(provider), model
	}
	m, s, p := usageLog(t, provider)
	if provider == "codex" {
		return m, s, p, usageFixtureForStep(t, "codex", s), Tokens{InputTokens: 8806, CacheReadTokens: 9088, OutputTokens: 33}, "gpt-6.1-sol"
	}
	return m, s, p, usageFixtureForStep(t, "claude", s), Tokens{2, 158, 17161, 32489}, "claude-haiku-5-5"
}

func withoutUsageModel(data []byte, provider, model string) []byte {
	data = bytes.ReplaceAll(data, []byte(`"model":"`+model+`"`), []byte(`"model":""`))
	if provider == "codex" {
		data = bytes.ReplaceAll(data, []byte(`{"type":"turn_context","payload":{"model":""}}`+"\n"), nil)
	}
	if provider == "copilot_cli" {
		data = bytes.ReplaceAll(data, []byte(`"model":"claude-sonnet-5.5"`), []byte(`"model":""`))
		data = bytes.ReplaceAll(data, []byte(`"responseModel":"`+model+`"`), []byte(`"responseModel":""`))
	}
	return data
}

func TestTokensWithoutAModelNameAreFiledUnderTheChairsConfiguredModel(t *testing.T) {
	for _, provider := range []string{"claude_code", "codex", "copilot_cli", "antigravity_cli"} {
		t.Run(provider, func(t *testing.T) {
			m, s, p, data, want, model := modelUsageLog(t, provider)
			s.Chair.Model = "configured-model"
			close := openUsage(t, m, s)
			appendUsage(t, p, withoutUsageModel(data, provider, model))
			u := close()
			requireTokens(t, u, want)
			if !reflect.DeepEqual(u.Models, map[string]Tokens{s.Chair.Model: want}) {
				t.Fatalf("models = %v, want %s: %+v", u.Models, s.Chair.Model, want)
			}
		})
	}
}

func TestTokensWithoutAnyModelNameAreFiledUnderUnknownWhenTheChairHasNoModelEither(t *testing.T) {
	for _, provider := range []string{"claude_code", "codex", "copilot_cli", "antigravity_cli"} {
		t.Run(provider, func(t *testing.T) {
			m, s, p, data, want, model := modelUsageLog(t, provider)
			close := openUsage(t, m, s)
			appendUsage(t, p, withoutUsageModel(data, provider, model))
			u := close()
			requireTokens(t, u, want)
			if !reflect.DeepEqual(u.Models, map[string]Tokens{"unknown": want}) {
				t.Fatal(u.Models)
			}
		})
	}
}

func TestAModelNameFromTheUsageSourceIsKeptEvenWhenTheChairsConfiguredModelIsSpelledDifferently(t *testing.T) {
	for _, provider := range []string{"claude_code", "codex", "copilot_cli"} {
		t.Run(provider, func(t *testing.T) {
			for _, source := range []string{"named", "unknown", "copilot-model"} {
				if source == "copilot-model" && provider != "copilot_cli" {
					continue
				}
				t.Run(source, func(t *testing.T) {
					m, s, p, data, want, model := modelUsageLog(t, provider)
					s.Chair.Model = strings.ReplaceAll(model, "-", ".")
					if source == "unknown" {
						data = bytes.ReplaceAll(data, []byte(model), []byte("unknown"))
						model = "unknown"
					} else if source == "copilot-model" {
						data = bytes.ReplaceAll(data, []byte(`"responseModel":"`+model+`",`), nil)
						model = "claude-sonnet-5.5"
					}
					close := openUsage(t, m, s)
					appendUsage(t, p, data)
					u := close()
					requireTokens(t, u, want)
					if !reflect.DeepEqual(u.Models, map[string]Tokens{model: want}) {
						t.Fatalf("models = %v, want %s: %+v", u.Models, model, want)
					}
				})
			}
		})
	}
}

func TestClaudeWindowsAttributeNamedAndUnnamedMessagesIndependently(t *testing.T) {
	for _, configured := range []string{"configured-model", "claude-haiku-5-5"} {
		t.Run(configured, func(t *testing.T) {
			m, s, p, data, named, _ := modelUsageLog(t, "claude_code")
			s.Chair.Model = configured
			close := openUsage(t, m, s)
			appendUsage(t, p, data)
			appendUsage(t, p, []byte(`{"type":"assistant","message":{"id":"unnamed-parent","usage":{"input_tokens":3,"output_tokens":4}}}`+"\n"))
			child := filepath.Join(strings.TrimSuffix(p, ".jsonl"), "subagents", "agent-child.jsonl")
			appendUsage(t, child, []byte(`{"type":"assistant","message":{"id":"unnamed-child","usage":{"input_tokens":5,"output_tokens":6}}}`+"\n"))
			u := close()
			unnamed := Tokens{InputTokens: 8, OutputTokens: 10}
			requireTokens(t, u, named.add(unnamed))
			want := map[string]Tokens{"claude-haiku-5-5": named}
			want[configured] = want[configured].add(unnamed)
			if !reflect.DeepEqual(u.Models, want) {
				t.Fatalf("models = %v, want %v", u.Models, want)
			}
		})
	}
}

func TestAnOldStepRecordWithUnknownModelStillReadsAndSumsWithoutReattribution(t *testing.T) {
	data := []byte(`{"step":"review","chair":{"model":"configured-model"},"usage":{"known":true,"input_tokens":2,"output_tokens":3,"cache_write_tokens":4,"cache_read_tokens":5,"models":{"unknown":{"input_tokens":2,"output_tokens":3,"cache_write_tokens":4,"cache_read_tokens":5}}}}`)
	var rec Record
	if err := json.Unmarshal(data, &rec); err != nil {
		t.Fatal(err)
	}
	if rec.Usage == nil {
		t.Fatal("missing usage")
	}
	want := Tokens{2, 3, 4, 5}
	requireTokens(t, *rec.Usage, want)
	e := &Engine{}
	e.record(rec)
	e.record(rec)
	requireTokens(t, e.usage, want.add(want))
	if !reflect.DeepEqual(e.usage.Models, map[string]Tokens{"unknown": want.add(want)}) {
		t.Fatal(e.usage.Models)
	}
}
