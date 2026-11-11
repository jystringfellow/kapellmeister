package loop

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

func usageFixture(t *testing.T, provider string) []byte {
	t.Helper()
	data, err := os.ReadFile("testdata/" + provider + "_usage.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func appendUsage(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err = f.Write(data); err != nil {
		t.Fatal(err)
	}
}

func usageLog(t *testing.T, provider string) (*LogMeter, Step, string) {
	t.Helper()
	meter := &LogMeter{ClaudeHome: t.TempDir(), CodexHome: t.TempDir()}
	step := Step{Dir: t.TempDir(), ReplyPath: RunDirRel("run") + "/implement-1.md", Chair: Chair{Provider: provider}}
	var path string
	if provider == "claude_code" {
		project := regexp.MustCompile(`[^a-zA-Z0-9]`).ReplaceAllString(step.Dir, "-")
		path = filepath.Join(meter.ClaudeHome, "projects", project, "session.jsonl")
	} else {
		path = filepath.Join(meter.CodexHome, "sessions", time.Now().Format("2006/01/02"), "rollout-session.jsonl")
		meta, _ := json.Marshal(map[string]any{"type": "session_meta", "payload": map[string]string{"cwd": step.Dir}})
		appendUsage(t, path, append(meta, '\n'))
	}
	return meter, step, path
}

func requireTokens(t *testing.T, usage Usage, want Tokens) {
	t.Helper()
	if !usage.Known || usage.Tokens == nil || *usage.Tokens != want {
		t.Fatalf("usage = %+v, tokens = %+v, want %+v", usage, usage.Tokens, want)
	}
	var total Tokens
	for _, tokens := range usage.Models {
		total.InputTokens += tokens.InputTokens
		total.OutputTokens += tokens.OutputTokens
		total.CacheWriteTokens += tokens.CacheWriteTokens
		total.CacheReadTokens += tokens.CacheReadTokens
	}
	if total != want {
		t.Fatalf("models total = %+v, want %+v", total, want)
	}
}

func requireUnknown(t *testing.T, usage Usage, reason string) {
	t.Helper()
	if usage.Known || usage.Reason != reason {
		t.Fatalf("usage = %+v, want unknown %s", usage, reason)
	}
	data, _ := json.Marshal(usage)
	for _, field := range []string{"input_tokens", "output_tokens", "cache_write_tokens", "cache_read_tokens", "models"} {
		if bytes.Contains(data, []byte(field)) {
			t.Errorf("unknown usage contains %s: %s", field, data)
		}
	}
}

func TestAClaudeStepsUsageIsTheGrowthOfItsLogDuringThatStep(t *testing.T) {
	m, s, p := usageLog(t, "claude_code")
	appendUsage(t, p, usageFixture(t, "claude"))
	close := m.Open(s)
	data := bytes.ReplaceAll(usageFixture(t, "claude"), []byte("msg-1"), []byte("msg-2"))
	appendUsage(t, p, data)
	requireTokens(t, close(), Tokens{InputTokens: 2, OutputTokens: 158, CacheWriteTokens: 17161, CacheReadTokens: 32489})
}

func TestAClaudeMessageStreamedAcrossSeveralLinesIsCountedOnce(t *testing.T) {
	m, s, p := usageLog(t, "claude_code")
	close := m.Open(s)
	appendUsage(t, p, usageFixture(t, "claude"))
	requireTokens(t, close(), Tokens{InputTokens: 2, OutputTokens: 158, CacheWriteTokens: 17161, CacheReadTokens: 32489})
}

func TestClaudeUsageKeepsInputOutputCacheWriteAndCacheReadApartAndNamesTheModel(t *testing.T) {
	m, s, p := usageLog(t, "claude_code")
	close := m.Open(s)
	appendUsage(t, p, usageFixture(t, "claude"))
	u := close()
	requireTokens(t, u, Tokens{InputTokens: 2, OutputTokens: 158, CacheWriteTokens: 17161, CacheReadTokens: 32489})
	if _, ok := u.Models["claude-haiku-5-5"]; !ok {
		t.Fatal(u.Models)
	}
}

func TestAClaudeStepWithSeveralModelsRecordsEachModelSeparately(t *testing.T) {
	m, s, p := usageLog(t, "claude_code")
	close := m.Open(s)
	appendUsage(t, p, usageFixture(t, "claude"))
	appendUsage(t, p, []byte(`{"type":"assistant","requestId":"fallback","message":{"model":"claude-opus-5-5","usage":{"input_tokens":3,"output_tokens":4}}}`+"\n"))
	u := close()
	requireTokens(t, u, Tokens{InputTokens: 5, OutputTokens: 162, CacheWriteTokens: 17161, CacheReadTokens: 32489})
	if len(u.Models) != 2 {
		t.Fatal(u.Models)
	}
}

func codexCount(input, cached, output, lastInput, lastCached, lastOutput int) []byte {
	return []byte(fmt.Sprintf(`{"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":%d,"cached_input_tokens":%d,"output_tokens":%d},"last_token_usage":{"input_tokens":%d,"cached_input_tokens":%d,"output_tokens":%d}}}}`+"\n", input, cached, output, lastInput, lastCached, lastOutput))
}

func TestACodexStepsUsageIsTheDifferenceOfItsCumulativeTokenCounts(t *testing.T) {
	m, s, p := usageLog(t, "codex")
	appendUsage(t, p, usageFixture(t, "codex"))
	close := m.Open(s)
	appendUsage(t, p, codexCount(18000, 9100, 80, 106, 12, 47))
	appendUsage(t, p, codexCount(18200, 9150, 100, 200, 50, 20))
	u := close()
	requireTokens(t, u, Tokens{InputTokens: 244, CacheReadTokens: 62, OutputTokens: 67})
	if _, ok := u.Models["gpt-6.1-sol"]; !ok {
		t.Fatal(u.Models)
	}
}

func TestCodexInputExcludesCachedTokensSoBothAgentsMeanTheSame(t *testing.T) {
	m, s, p := usageLog(t, "codex")
	close := m.Open(s)
	appendUsage(t, p, usageFixture(t, "codex"))
	requireTokens(t, close(), Tokens{InputTokens: 8806, CacheReadTokens: 9088, OutputTokens: 33})
}

func TestARepeatedOrNullCodexTokenCountEventDoesNotChangeTheStepsUsage(t *testing.T) {
	m, s, p := usageLog(t, "codex")
	close := m.Open(s)
	appendUsage(t, p, usageFixture(t, "codex"))
	appendUsage(t, p, []byte(`{"type":"event_msg","payload":{"type":"token_count","info":null}}`+"\n"))
	requireTokens(t, close(), Tokens{InputTokens: 8806, CacheReadTokens: 9088, OutputTokens: 33})
}

func TestTwoStepsOnOneReusedTerminalEachGetOnlyTheirOwnUsage(t *testing.T) {
	for _, provider := range []string{"claude_code", "codex"} {
		t.Run(provider, func(t *testing.T) {
			m, s, p := usageLog(t, provider)
			e, f, records := newEngine(t, passing)
			e.Meter = m
			e.Config.Worktree = s.Dir
			f.dir = s.Dir
			e.Config.ReplyGrace = time.Millisecond
			if err := PrepareRunDir(s.Dir, "run"); err != nil {
				t.Fatal(err)
			}
			f.actions["implement"] = func(s Step) {
				if provider == "claude_code" {
					appendUsage(t, p, bytes.ReplaceAll(usageFixture(t, "claude"), []byte("msg-1"), []byte(fmt.Sprint(s.Round))))
				} else {
					appendUsage(t, p, []byte(`{"type":"turn_context","payload":{"model":"gpt-6.1-sol"}}`+"\n"))
					appendUsage(t, p, codexCount(s.Round*100, s.Round*20, s.Round*10, 100, 20, 10))
				}
				f.write(s.ReplyPath, "done")
			}
			for round := 1; round <= 2; round++ {
				s.Name = "implement"
				s.Round = round
				s.ReuseTerminal = "same"
				if _, err := e.step(context.Background(), s); err != nil {
					t.Fatal(err)
				}
			}
			var recs []Record
			for _, line := range bytes.Split(bytes.TrimSpace(records.Bytes()), []byte("\n")) {
				var rec Record
				if err := json.Unmarshal(line, &rec); err != nil {
					t.Fatal(err)
				}
				recs = append(recs, rec)
			}
			if len(recs) != 2 || recs[0].Usage == nil || !reflect.DeepEqual(recs[0].Usage, recs[1].Usage) {
				t.Fatalf("records = %+v", recs)
			}
			if provider == "codex" {
				requireTokens(t, *recs[1].Usage, Tokens{InputTokens: 80, CacheReadTokens: 20, OutputTokens: 10})
			} else {
				requireTokens(t, *recs[1].Usage, Tokens{InputTokens: 2, OutputTokens: 158, CacheWriteTokens: 17161, CacheReadTokens: 32489})
			}
		})
	}
}

func TestANudgeAttemptRecordsItsOwnUsage(t *testing.T) {
	m, s, p := usageLog(t, "claude_code")
	e, f, records := newEngine(t, passing)
	e.Meter = m
	e.Config.Worktree = s.Dir
	f.dir = s.Dir
	e.Config.ReplyGrace = time.Millisecond
	if err := PrepareRunDir(s.Dir, "run"); err != nil {
		t.Fatal(err)
	}
	f.actions["implement"] = func(Step) { appendUsage(t, p, usageFixture(t, "claude")) }
	f.actions["implement-nudge"] = func(s Step) { appendUsage(t, p, usageFixture(t, "claude")); f.write(s.ReplyPath, "done") }
	s.Name = "implement"
	if _, err := e.step(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	lines := bytes.Split(bytes.TrimSpace(records.Bytes()), []byte("\n"))
	if len(lines) != 2 {
		t.Fatalf("records: %s", records.Bytes())
	}
	for i, line := range lines {
		var rec Record
		_ = json.Unmarshal(line, &rec)
		if rec.Usage == nil {
			t.Fatal(string(line))
		}
		requireTokens(t, *rec.Usage, Tokens{InputTokens: 2, OutputTokens: 158, CacheWriteTokens: 17161, CacheReadTokens: 32489})
		if i == 1 && rec.Step != "implement-nudge" {
			t.Fatal(rec.Step)
		}
	}
}

func TestConcurrentStepsInOneWorktreeAreAttributedByTheirReplyPath(t *testing.T) {
	m, s, p := usageLog(t, "claude_code")
	other := s
	other.ReplyPath = RunDirRel("run") + "/review-1.md"
	first, second := m.Open(s), m.Open(other)
	var wg sync.WaitGroup
	for i, step := range []Step{s, other} {
		wg.Go(func() {
			data := bytes.ReplaceAll(usageFixture(t, "claude"), []byte("reply-path"), []byte(step.ReplyPath))
			if i == 1 {
				data = bytes.ReplaceAll(data, []byte(`"input_tokens":2`), []byte(`"input_tokens":7`))
			}
			appendUsage(t, filepath.Join(filepath.Dir(p), fmt.Sprintf("%d.jsonl", i)), data)
		})
	}
	wg.Wait()
	requireTokens(t, first(), Tokens{InputTokens: 2, OutputTokens: 158, CacheWriteTokens: 17161, CacheReadTokens: 32489})
	requireTokens(t, second(), Tokens{InputTokens: 7, OutputTokens: 158, CacheWriteTokens: 17161, CacheReadTokens: 32489})
}

func TestWhenSeveralLogsGrowAndNoneNamesTheReplyPathUsageIsUnknownAsAmbiguous(t *testing.T) {
	m, s, p := usageLog(t, "claude_code")
	close := m.Open(s)
	appendUsage(t, p, usageFixture(t, "claude"))
	appendUsage(t, filepath.Join(filepath.Dir(p), "other.jsonl"), usageFixture(t, "claude"))
	requireUnknown(t, close(), "ambiguous-log")
}

func TestACopilotOrAntigravityStepRecordsUnknownUsageNotZero(t *testing.T) {
	for _, provider := range []string{"copilot", "copilot_cli", "antigravity", "antigravity_cli"} {
		t.Run(provider, func(t *testing.T) {
			m, s, _ := usageLog(t, provider)
			requireUnknown(t, m.Open(s)(), "unsupported-provider")
		})
	}
}

func TestAMissingUnreadableOrMalformedLogGivesUnknownUsageAndNeverFailsTheStep(t *testing.T) {
	for _, kind := range []string{"missing", "unreadable", "malformed", "no-usage"} {
		t.Run(kind, func(t *testing.T) {
			m, s, p := usageLog(t, "claude_code")
			close := m.Open(s)
			reason := "no-log"
			switch kind {
			case "unreadable":
				appendUsage(t, p, usageFixture(t, "claude"))
				if err := os.Remove(p); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("/proc/1/mem", p); err != nil {
					t.Fatal(err)
				}
				reason = "unreadable-log"
			case "malformed":
				appendUsage(t, p, []byte("{bad json}\n"))
				reason = "no-usage"
			case "no-usage":
				appendUsage(t, p, []byte("{\"type\":\"user\"}\n"))
				reason = "no-usage"
			}
			requireUnknown(t, close(), reason)
		})
	}
	e, _, _ := newEngine(t, passing)
	e.Meter = &LogMeter{ClaudeHome: t.TempDir(), CodexHome: t.TempDir()}
	c := Chair{Provider: "claude_code"}
	e.Config.Chairs = Chairs{Planner: c, Implementer: c, Reviewers: []Chair{c}, Gate: c, Classifier: c}
	if res := run(t, e); res.Outcome != Approved {
		t.Fatal(res)
	}
}

func TestAHalfWrittenLastLineIsLeftOutOfTheStepsUsage(t *testing.T) {
	m, s, p := usageLog(t, "claude_code")
	close := m.Open(s)
	appendUsage(t, p, usageFixture(t, "claude"))
	appendUsage(t, p, []byte(`{"type":"assistant","message":{"id":"unfinished","usage":`))
	requireTokens(t, close(), Tokens{InputTokens: 2, OutputTokens: 158, CacheWriteTokens: 17161, CacheReadTokens: 32489})
}

func TestUsageIsRecordedForAStepThatFailed(t *testing.T) {
	m, s, p := usageLog(t, "claude_code")
	e, f, records := newEngine(t, passing)
	e.Meter = m
	e.Config.Worktree = s.Dir
	f.actions["implement"] = func(Step) { appendUsage(t, p, usageFixture(t, "claude")) }
	f.errFor = func(Step) error { return errors.New("step failed") }
	s.Name = "implement"
	if _, err := e.attempt(context.Background(), s); err == nil {
		t.Fatal("step succeeded")
	}
	var rec Record
	if err := json.Unmarshal(bytes.TrimSpace(records.Bytes()), &rec); err != nil {
		t.Fatal(err)
	}
	if rec.Error == "" || rec.Usage == nil {
		t.Fatal(rec)
	}
	requireTokens(t, *rec.Usage, Tokens{InputTokens: 2, OutputTokens: 158, CacheWriteTokens: 17161, CacheReadTokens: 32489})
}

func TestUsageReadsNothingBeyondCountsAndModelNames(t *testing.T) {
	m, s, p := usageLog(t, "claude_code")
	close := m.Open(s)
	appendUsage(t, p, usageFixture(t, "claude"))
	u := close()
	data, _ := json.Marshal(Record{Usage: &u})
	for _, private := range []string{"PRIVATE PROMPT", "PRIVATE REPLY", "PRIVATE FILE CONTENT", "msg-1", "req-1", "reply-path"} {
		if strings.Contains(string(data), private) {
			t.Fatal(string(data))
		}
	}
}

func TestStepRecordsWithoutUsageStillDecode(t *testing.T) {
	var rec Record
	if err := json.Unmarshal([]byte(`{"run_id":"old","step":"implement","duration_ms":1}`), &rec); err != nil || rec.Usage != nil {
		t.Fatalf("%+v, %v", rec, err)
	}
	var old struct {
		RunID string `json:"run_id"`
		Step  string `json:"step"`
	}
	if err := json.Unmarshal([]byte(`{"run_id":"new","step":"implement","usage":{"known":true,"input_tokens":12}}`), &old); err != nil || old.RunID != "new" || old.Step != "implement" {
		t.Fatalf("%+v, %v", old, err)
	}
}

func TestStepsQueuedForOneChairDoNotCountEachOthersUsage(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m, s, p := usageLog(t, "claude_code")
		s.Chair.Address = "leads.classifier@ensemble"
		first := m.Open(s)
		started := make(chan struct{})
		finished := make(chan Usage, 1)
		go func() {
			close(started)
			second := m.Open(s)
			appendUsage(t, p, []byte(`{"type":"assistant","message":{"id":"second","model":"claude-haiku-5-5","usage":{"input_tokens":8,"output_tokens":9}}}`+"\n"))
			finished <- second()
		}()
		<-started
		synctest.Wait()
		appendUsage(t, p, usageFixture(t, "claude"))
		requireTokens(t, first(), Tokens{InputTokens: 2, OutputTokens: 158, CacheWriteTokens: 17161, CacheReadTokens: 32489})
		requireTokens(t, <-finished, Tokens{InputTokens: 8, OutputTokens: 9})
	})
}

func TestMalformedUsageNeverProducesAPartialKnownTotal(t *testing.T) {
	for _, provider := range []string{"claude_code", "codex"} {
		t.Run(provider, func(t *testing.T) {
			m, s, p := usageLog(t, provider)
			close := m.Open(s)
			format := "claude"
			if provider == "codex" {
				format = "codex"
			}
			appendUsage(t, p, usageFixture(t, format))
			appendUsage(t, p, []byte("{broken json}\n"))
			requireUnknown(t, close(), "no-usage")
		})
	}
}

func TestACompletedLineThatStartedBeforeTheStepIsNotItsUsage(t *testing.T) {
	m, s, p := usageLog(t, "claude_code")
	line := []byte(`{"type":"assistant","message":{"id":"prior","model":"m","usage":{"input_tokens":99,"output_tokens":99}}}` + "\n")
	appendUsage(t, p, line[:20])
	close := m.Open(s)
	appendUsage(t, p, line[20:])
	appendUsage(t, p, usageFixture(t, "claude"))
	requireTokens(t, close(), Tokens{InputTokens: 2, OutputTokens: 158, CacheWriteTokens: 17161, CacheReadTokens: 32489})
}

func TestAStreamedClaudeMessagesLastUsageWins(t *testing.T) {
	m, s, p := usageLog(t, "claude_code")
	close := m.Open(s)
	appendUsage(t, p, usageFixture(t, "claude"))
	appendUsage(t, p, []byte(`{"type":"assistant","message":{"id":"msg-1","model":"claude-haiku-5-5","usage":{"input_tokens":2,"output_tokens":200,"cache_creation_input_tokens":17161,"cache_read_input_tokens":32489}}}`+"\n"))
	requireTokens(t, close(), Tokens{InputTokens: 2, OutputTokens: 200, CacheWriteTokens: 17161, CacheReadTokens: 32489})
}

func TestCodexCacheWritesRemainSeparateWithoutSubtractingThemFromInput(t *testing.T) {
	m, s, p := usageLog(t, "codex")
	close := m.Open(s)
	appendUsage(t, p, []byte(`{"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":20,"cached_input_tokens":7,"cache_write_input_tokens":3,"output_tokens":6},"last_token_usage":{"input_tokens":20,"cached_input_tokens":7,"cache_write_input_tokens":3,"output_tokens":6}}}}`+"\n"))
	u := close()
	requireTokens(t, u, Tokens{InputTokens: 13, CacheReadTokens: 7, CacheWriteTokens: 3, OutputTokens: 6})
	if _, ok := u.Models["unknown"]; !ok {
		t.Fatal(u.Models)
	}
}

func TestOversizedUsageLinesAndTruncatedLogsAreUnknown(t *testing.T) {
	for _, kind := range []string{"oversized", "truncated"} {
		t.Run(kind, func(t *testing.T) {
			m, s, p := usageLog(t, "claude_code")
			appendUsage(t, p, usageFixture(t, "claude"))
			close := m.Open(s)
			if kind == "oversized" {
				appendUsage(t, p, []byte(strings.Repeat(" ", maxUsageLine)+"\n"))
				requireUnknown(t, close(), "no-usage")
			} else {
				if err := os.Truncate(p, 0); err != nil {
					t.Fatal(err)
				}
				requireUnknown(t, close(), "unreadable-log")
			}
		})
	}
}

func TestUsageTotalsAreRecordedEvenWithoutATelemetryWriter(t *testing.T) {
	e, _, _ := newEngine(t, passing)
	e.Records = nil
	e.Meter = &LogMeter{ClaudeHome: t.TempDir(), CodexHome: t.TempDir()}
	res := run(t, e)
	if res.Usage == nil || res.Usage.Known || res.Usage.UnknownSteps != 6 {
		t.Fatalf("usage = %+v", res.Usage)
	}
}

func TestAFailedRunsResultIncludesTheUsageIncurredBeforeFailure(t *testing.T) {
	e, f, _ := newEngine(t, passing)
	m, s, p := usageLog(t, "claude_code")
	e.Meter = m
	e.Config.Chairs.Planner = s.Chair
	project := claudeProjectName.ReplaceAllString(e.Config.Worktree, "-")
	p = filepath.Join(m.ClaudeHome, "projects", project, "session.jsonl")
	f.actions["plan"] = func(Step) { appendUsage(t, p, usageFixture(t, "claude")) }
	f.errFor = func(Step) error { return errors.New("step failed") }
	res := run(t, e)
	if res.Outcome != Failed || res.Usage == nil {
		t.Fatal(res)
	}
	requireTokens(t, *res.Usage, Tokens{InputTokens: 2, OutputTokens: 158, CacheWriteTokens: 17161, CacheReadTokens: 32489})
}
