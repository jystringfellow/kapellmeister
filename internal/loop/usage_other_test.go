package loop

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func otherUsageLog(t *testing.T, provider string) (*LogMeter, Step, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("COPILOT_HOME", filepath.Join(home, ".copilot"))
	s := Step{Dir: t.TempDir(), ReplyPath: RunDirRel("run") + "/implement-1.md", Chair: Chair{Provider: provider}}
	p := filepath.Join(home, ".gemini", "antigravity-cli", "brain", "session-1", ".system_generated", "logs", "transcript.jsonl")
	if provider == "copilot_cli" {
		p = filepath.Join(home, ".copilot", "session-state", "session-1", "events.jsonl")
	}
	return NewLogMeter(), s, p
}

func otherFixture(t *testing.T, provider string, s Step) []byte {
	t.Helper()
	return bytes.ReplaceAll(usageFixtureForStep(t, provider, s), []byte("worktree-path"), []byte(s.Dir))
}

func otherTokens(provider string) Tokens {
	if provider == "copilot_cli" {
		return Tokens{10, 114, 27276, 51947}
	}
	return Tokens{13089, 100, 0, 13276}
}

func TestCopilotAndAntigravityStepsSumTheirRequestsWithoutCountingCumulativeRecords(t *testing.T) {
	for _, provider := range []string{"copilot_cli", "antigravity_cli"} {
		t.Run(provider, func(t *testing.T) {
			m, s, p := otherUsageLog(t, provider)
			close := openUsage(t, m, s)
			appendUsage(t, p, otherFixture(t, provider, s))
			u := close()
			requireTokens(t, u, otherTokens(provider))
			model := "unknown"
			if provider == "copilot_cli" {
				model = "claude-sonnet-5-5"
			}
			if len(u.Models) != 1 || u.Models[model] != otherTokens(provider) {
				t.Fatal(u.Models)
			}
			data, _ := json.Marshal(u)
			for _, private := range []string{"hello", "reply-path", s.ReplyPath, s.Dir, "request-1", "session-1"} {
				if strings.Contains(string(data), private) {
					t.Fatal(string(data))
				}
			}
		})
	}
}

func TestResumedCopilotAndAntigravityStepsCountOnlyRequestsAppendedDuringTheStep(t *testing.T) {
	for _, provider := range []string{"copilot_cli", "antigravity_cli"} {
		t.Run(provider, func(t *testing.T) {
			m, s, p := otherUsageLog(t, provider)
			data := otherFixture(t, provider, s)
			marker := []byte(`{"type":"user.message"`)
			want := Tokens{4, 7, 854, 25643}
			if provider == "antigravity_cli" {
				marker = []byte(`{"step_index":4`)
				want = Tokens{2272, 21, 0, 6714}
			}
			split := bytes.LastIndex(data, marker)
			if split <= 0 {
				t.Fatal("fixture has no resumed request")
			}
			appendUsage(t, p, data[:split])
			close := openUsage(t, m, s)
			appendUsage(t, p, data[split:])
			requireTokens(t, close(), want)
		})
	}
}

func TestCopilotAndAntigravitySessionsStillBeingWrittenCountOnlyCompleteRequests(t *testing.T) {
	for _, provider := range []string{"copilot_cli", "antigravity_cli"} {
		t.Run(provider, func(t *testing.T) {
			m, s, p := otherUsageLog(t, provider)
			close := openUsage(t, m, s)
			data := otherFixture(t, provider, s)
			var requests []byte
			for _, line := range bytes.Split(data, []byte("\n")) {
				if bytes.Contains(line, []byte(`"type":"session.shutdown"`)) || bytes.Contains(line, []byte(`"type":"session.usage_checkpoint"`)) {
					continue
				}
				if len(line) > 0 {
					requests = append(requests, append(line, '\n')...)
				}
			}
			appendUsage(t, p, requests)
			appendUsage(t, p, []byte(`{"type":"unfinished","input_tokens":999`))
			requireTokens(t, close(), otherTokens(provider))
		})
	}
}

func TestGrowingCopilotAndAntigravitySessionsAreAttributedOnlyByAUniqueReplyPath(t *testing.T) {
	for _, provider := range []string{"copilot_cli", "antigravity_cli"} {
		t.Run(provider, func(t *testing.T) {
			for _, kind := range []string{"unique", "ambiguous", "unmatched", "no-session", "unchanged"} {
				t.Run(kind, func(t *testing.T) {
					m, s, p := otherUsageLog(t, provider)
					data := otherFixture(t, provider, s)
					if kind == "unchanged" {
						appendUsage(t, p, data)
					}
					close := openUsage(t, m, s)
					if kind != "no-session" && kind != "unchanged" {
						first := data
						if kind == "unmatched" {
							first = bytes.ReplaceAll(data, []byte(s.ReplyPath), []byte("another-reply"))
						}
						appendUsage(t, p, first)
						other := strings.Replace(p, "session-1", "session-2", 1)
						if kind == "unique" || kind == "unmatched" {
							data = bytes.ReplaceAll(data, []byte(s.ReplyPath), []byte("another-reply"))
						}
						appendUsage(t, other, data)
					}
					u := close()
					switch kind {
					case "unique":
						requireTokens(t, u, otherTokens(provider))
					case "no-session", "unchanged":
						requireUnknown(t, u, "no-log")
					default:
						requireUnknown(t, u, "ambiguous-log")
					}
				})
			}
		})
	}
}

func TestACopilotSessionFromAnotherWorkingDirectoryIsNotThisStepsLog(t *testing.T) {
	m, s, p := otherUsageLog(t, "copilot_cli")
	close := openUsage(t, m, s)
	data := otherFixture(t, "copilot_cli", s)
	appendUsage(t, p, bytes.ReplaceAll(data, []byte(s.Dir), []byte(t.TempDir())))
	requireUnknown(t, close(), "no-log")
}

func TestCopilotAndAntigravityMissingMalformedOrUnreadableLogsNeverProduceKnownZero(t *testing.T) {
	for _, provider := range []string{"copilot_cli", "antigravity_cli"} {
		t.Run(provider, func(t *testing.T) {
			for _, kind := range []string{"removed", "truncated", "symlink", "session-symlink", "directory", "malformed", "no-usage"} {
				t.Run(kind, func(t *testing.T) {
					m, s, p := otherUsageLog(t, provider)
					appendUsage(t, p, otherFixture(t, provider, s))
					close := openUsage(t, m, s)
					reason := "unreadable-log"
					switch kind {
					case "removed":
						if err := os.Remove(p); err != nil {
							t.Fatal(err)
						}
					case "truncated":
						if err := os.Truncate(p, 0); err != nil {
							t.Fatal(err)
						}
					case "symlink", "session-symlink":
						path := p
						if kind == "session-symlink" {
							path = filepath.Dir(p)
							if provider == "antigravity_cli" {
								path = filepath.Dir(filepath.Dir(path))
							}
						}
						target := filepath.Join(t.TempDir(), "target")
						if err := os.Rename(path, target); err != nil {
							t.Fatal(err)
						}
						if err := os.Symlink(target, path); err != nil {
							t.Fatal(err)
						}
					case "directory":
						if err := os.Remove(p); err != nil {
							t.Fatal(err)
						}
						if err := os.Mkdir(p, 0700); err != nil {
							t.Fatal(err)
						}
					case "malformed":
						appendReplyPath(t, p, s)
						appendUsage(t, p, []byte("{broken json}\n"))
						reason = "no-usage"
					case "no-usage":
						appendReplyPath(t, p, s)
						reason = "no-usage"
					}
					requireUnknown(t, close(), reason)
				})
			}
		})
	}
}

func TestInvalidCopilotRequestCountsMakeTheWholeStepUnknown(t *testing.T) {
	for _, change := range []struct{ from, to string }{
		{`"reasoningTokens":0`, `"reasoningTokens":-1`},
		{`"inputTokens":26308`, `"inputTokens":null`},
		{`"outputTokens":73`, `"outputTokens":-1`},
		{`"cacheReadTokens":0`, `"cacheReadTokens":26309`},
		{`"cacheWriteTokens":26304`, `"cacheWriteTokens":null`},
		{`"cacheReadTokens":0`, `"cacheReadTokens":-1`},
	} {
		t.Run(change.to, func(t *testing.T) {
			m, s, p := otherUsageLog(t, "copilot_cli")
			close := openUsage(t, m, s)
			appendUsage(t, p, bytes.ReplaceAll(otherFixture(t, "copilot_cli", s), []byte(change.from), []byte(change.to)))
			requireUnknown(t, close(), "no-usage")
		})
	}
}

func TestACopilotSessionWithoutPerRequestUsageIsUnknownEvenWithShutdownTotals(t *testing.T) {
	m, s, p := otherUsageLog(t, "copilot_cli")
	close := openUsage(t, m, s)
	appendUsage(t, p, otherFixture(t, "copilot_cli_1_0_86", s))
	requireUnknown(t, close(), "no-usage")
}

func TestInvalidAntigravityRequestCountsMakeTheWholeStepUnknown(t *testing.T) {
	for _, change := range []struct{ from, to string }{
		{`"input_tokens":8616`, `"input_tokens":null`},
		{`"output_tokens":55`, `"output_tokens":null`},
		{`"output_tokens":55`, `"output_tokens":-1`},
		{`"cache_read_tokens":0`, `"cache_read_tokens":-1`},
		{`"step_index":1,`, ``},
	} {
		t.Run(change.to, func(t *testing.T) {
			m, s, p := otherUsageLog(t, "antigravity_cli")
			close := openUsage(t, m, s)
			appendUsage(t, p, bytes.ReplaceAll(otherFixture(t, "antigravity_cli", s), []byte(change.from), []byte(change.to)))
			requireUnknown(t, close(), "no-usage")
		})
	}
}

func TestCopilotSubagentRequestsAreCountedOnceWithTheirParents(t *testing.T) {
	m, s, p := otherUsageLog(t, "copilot_cli")
	close := openUsage(t, m, s)
	data := otherFixture(t, "copilot_cli", s)
	appendUsage(t, p, data)
	var request []byte
	for _, line := range bytes.Split(data, []byte("\n")) {
		if bytes.Contains(line, []byte(`"usageId":"request-1"`)) {
			request = append(append([]byte(nil), line...), '\n')
			break
		}
	}
	if len(request) == 0 {
		t.Fatal("missing request")
	}
	appendUsage(t, p, request)
	child := bytes.ReplaceAll(request, []byte(`"sourceSessionId":"session-1"`), []byte(`"sourceSessionId":"child-1"`))
	child = bytes.ReplaceAll(child, []byte(`"initiator":"user"`), []byte(`"initiator":"sub-agent"`))
	appendUsage(t, p, child)
	appendUsage(t, p, child)
	requireTokens(t, close(), otherTokens("copilot_cli").add(Tokens{4, 73, 26304, 0}))
}

func TestRepeatedAntigravityPlannerResponsesAreCountedOnceAndIncompleteResponsesAreIgnored(t *testing.T) {
	m, s, p := otherUsageLog(t, "antigravity_cli")
	close := openUsage(t, m, s)
	data := otherFixture(t, "antigravity_cli", s)
	appendUsage(t, p, data)
	appendUsage(t, p, data)
	appendUsage(t, p, []byte(`{"step_index":7,"type":"PLANNER_RESPONSE","status":"RUNNING"}`+"\n"))
	requireTokens(t, close(), otherTokens("antigravity_cli"))
}

func TestCopilotRequestAndModelNamesFallBackToTheRecordedAlternatives(t *testing.T) {
	m, s, p := otherUsageLog(t, "copilot_cli")
	close := openUsage(t, m, s)
	data := otherFixture(t, "copilot_cli", s)
	data = bytes.ReplaceAll(data, []byte(`"responseModel":"claude-sonnet-5-5",`), nil)
	data = bytes.ReplaceAll(data, []byte(`"usageId":"request-1"`), []byte(`"usageId":""`))
	data = bytes.ReplaceAll(data, []byte(`"usageId":"request-2"`), []byte(`"usageId":""`))
	data = bytes.ReplaceAll(data, []byte(`"usageId":"request-3"`), []byte(`"usageId":""`))
	appendUsage(t, p, data)
	appendUsage(t, p, data)
	u := close()
	requireTokens(t, u, otherTokens("copilot_cli"))
	if len(u.Models) != 1 || u.Models["claude-sonnet-5.5"] != otherTokens("copilot_cli") {
		t.Fatal(u.Models)
	}
}

func TestACopilotStepsOutputIncludesReasoningWithoutAddingItAgain(t *testing.T) {
	for _, kind := range []string{"several-requests", "still-writing", "resumed"} {
		t.Run(kind, func(t *testing.T) {
			m, s, p := otherUsageLog(t, "copilot_cli")
			data := otherFixture(t, "copilot_cli_reasoning", s)
			want := Tokens{12, 366, 27907, 78557}
			if kind != "resumed" {
				marker := []byte(`{"type":"session.shutdown"`)
				if kind == "still-writing" {
					marker = []byte(`{"type":"session.usage_checkpoint"`)
				}
				end := bytes.Index(data, marker)
				if end < 0 {
					t.Fatal("fixture lacks cumulative records")
				}
				data = data[:end]
				want = Tokens{8, 362, 26727, 52896}
			}
			close := openUsage(t, m, s)
			appendUsage(t, p, data)
			u := close()
			requireTokens(t, u, want)
			if u.Models["claude-sonnet-5-5"] != want {
				t.Fatal(u.Models)
			}
		})
	}
}

func TestConcurrentCopilotStepsKeepTheirOwnOutputIncludingReasoning(t *testing.T) {
	m, s, p := otherUsageLog(t, "copilot_cli")
	s.Chair.Address = "section.a@ensemble"
	other := s
	other.Chair.Address = "section.b@ensemble"
	other.ReplyPath = RunDirRel("run") + "/review-1.md"
	first, second := openUsage(t, m, s), openUsage(t, m, other)
	appendUsage(t, p, otherFixture(t, "copilot_cli", s))
	otherPath := strings.Replace(p, "session-1", "session-2", 1)
	appendUsage(t, otherPath, otherFixture(t, "copilot_cli_single_reasoning", other))
	requireTokens(t, first(), otherTokens("copilot_cli"))
	requireTokens(t, second(), Tokens{4, 106, 7604, 18721})
}
