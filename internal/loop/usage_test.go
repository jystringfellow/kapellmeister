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

func usageFixtureForStep(t *testing.T, provider string, s Step) []byte {
	t.Helper()
	return bytes.ReplaceAll(usageFixture(t, provider), []byte("reply-path"), []byte(s.ReplyPath))
}

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
	appendUsage(t, p, usageFixtureForStep(t, "claude", s))
	close := openUsage(t, m, s)
	data := bytes.ReplaceAll(usageFixtureForStep(t, "claude", s), []byte("msg-1"), []byte("msg-2"))
	appendUsage(t, p, data)
	requireTokens(t, close(), Tokens{InputTokens: 2, OutputTokens: 158, CacheWriteTokens: 17161, CacheReadTokens: 32489})
}

func TestAClaudeMessageStreamedAcrossSeveralLinesIsCountedOnce(t *testing.T) {
	m, s, p := usageLog(t, "claude_code")
	close := openUsage(t, m, s)
	appendUsage(t, p, usageFixtureForStep(t, "claude", s))
	requireTokens(t, close(), Tokens{InputTokens: 2, OutputTokens: 158, CacheWriteTokens: 17161, CacheReadTokens: 32489})
}

func TestClaudeUsageKeepsInputOutputCacheWriteAndCacheReadApartAndNamesTheModel(t *testing.T) {
	m, s, p := usageLog(t, "claude_code")
	close := openUsage(t, m, s)
	appendUsage(t, p, usageFixtureForStep(t, "claude", s))
	u := close()
	requireTokens(t, u, Tokens{InputTokens: 2, OutputTokens: 158, CacheWriteTokens: 17161, CacheReadTokens: 32489})
	if _, ok := u.Models["claude-haiku-5-5"]; !ok {
		t.Fatal(u.Models)
	}
}

func TestAClaudeStepWithSeveralModelsRecordsEachModelSeparately(t *testing.T) {
	m, s, p := usageLog(t, "claude_code")
	close := openUsage(t, m, s)
	appendUsage(t, p, usageFixtureForStep(t, "claude", s))
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
	appendUsage(t, p, usageFixtureForStep(t, "codex", s))
	close := openUsage(t, m, s)
	appendReplyPath(t, p, s)
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
	close := openUsage(t, m, s)
	appendUsage(t, p, usageFixtureForStep(t, "codex", s))
	requireTokens(t, close(), Tokens{InputTokens: 8806, CacheReadTokens: 9088, OutputTokens: 33})
}

func TestARepeatedOrNullCodexTokenCountEventDoesNotChangeTheStepsUsage(t *testing.T) {
	m, s, p := usageLog(t, "codex")
	close := openUsage(t, m, s)
	appendUsage(t, p, usageFixtureForStep(t, "codex", s))
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
					appendUsage(t, p, bytes.ReplaceAll(usageFixtureForStep(t, "claude", s), []byte("msg-1"), []byte(fmt.Sprint(s.Round))))
				} else {
					appendReplyPath(t, p, s)
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
	f.actions["implement"] = func(Step) { appendUsage(t, p, usageFixtureForStep(t, "claude", s)) }
	f.actions["implement-nudge"] = func(s Step) {
		data := bytes.ReplaceAll(usageFixtureForStep(t, "claude", s), []byte("msg-1"), []byte("nudge"))
		appendUsage(t, p, data)
		f.write(s.ReplyPath, "done")
	}
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
	first, second := openUsage(t, m, s), openUsage(t, m, other)
	var wg sync.WaitGroup
	for i, step := range []Step{s, other} {
		wg.Go(func() {
			data := usageFixtureForStep(t, "claude", step)
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
	close := openUsage(t, m, s)
	appendUsage(t, p, usageFixture(t, "claude"))
	appendUsage(t, filepath.Join(filepath.Dir(p), "other.jsonl"), usageFixture(t, "claude"))
	requireUnknown(t, close(), "ambiguous-log")
}

func TestAStepOnAProviderWithoutAUsageReaderRecordsUnknownNotZero(t *testing.T) {
	for _, provider := range []string{"copilot", "antigravity", "gemini_cli"} {
		t.Run(provider, func(t *testing.T) {
			m, s, _ := usageLog(t, provider)
			requireUnknown(t, openUsage(t, m, s)(), "unsupported-provider")
		})
	}
}

func TestAMissingUnreadableOrMalformedLogGivesUnknownUsageAndNeverFailsTheStep(t *testing.T) {
	for _, kind := range []string{"missing", "unreadable", "malformed", "no-usage"} {
		t.Run(kind, func(t *testing.T) {
			m, s, p := usageLog(t, "claude_code")
			close := openUsage(t, m, s)
			reason := "no-log"
			switch kind {
			case "unreadable":
				appendUsage(t, p, usageFixtureForStep(t, "claude", s))
				if err := os.Remove(p); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("/proc/1/mem", p); err != nil {
					t.Fatal(err)
				}
				reason = "unreadable-log"
			case "malformed":
				appendReplyPath(t, p, s)
				appendUsage(t, p, []byte("{bad json}\n"))
				reason = "no-usage"
			case "no-usage":
				appendReplyPath(t, p, s)
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
	close := openUsage(t, m, s)
	appendUsage(t, p, usageFixtureForStep(t, "claude", s))
	appendUsage(t, p, []byte(`{"type":"assistant","message":{"id":"unfinished","usage":`))
	requireTokens(t, close(), Tokens{InputTokens: 2, OutputTokens: 158, CacheWriteTokens: 17161, CacheReadTokens: 32489})
}

func TestUsageIsRecordedForAStepThatFailed(t *testing.T) {
	m, s, p := usageLog(t, "claude_code")
	e, f, records := newEngine(t, passing)
	e.Meter = m
	e.Config.Worktree = s.Dir
	f.actions["implement"] = func(Step) { appendUsage(t, p, usageFixtureForStep(t, "claude", s)) }
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
	close := openUsage(t, m, s)
	appendUsage(t, p, usageFixtureForStep(t, "claude", s))
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
		first := openUsage(t, m, s)
		started := make(chan struct{})
		finished := make(chan Usage, 1)
		go func() {
			close(started)
			second := openUsage(t, m, s)
			appendReplyPath(t, p, s)
			appendUsage(t, p, []byte(`{"type":"assistant","message":{"id":"second","model":"claude-haiku-5-5","usage":{"input_tokens":8,"output_tokens":9}}}`+"\n"))
			finished <- second()
		}()
		<-started
		synctest.Wait()
		appendUsage(t, p, usageFixtureForStep(t, "claude", s))
		requireTokens(t, first(), Tokens{InputTokens: 2, OutputTokens: 158, CacheWriteTokens: 17161, CacheReadTokens: 32489})
		requireTokens(t, <-finished, Tokens{InputTokens: 8, OutputTokens: 9})
	})
}

func TestMalformedUsageNeverProducesAPartialKnownTotal(t *testing.T) {
	for _, provider := range []string{"claude_code", "codex"} {
		t.Run(provider, func(t *testing.T) {
			m, s, p := usageLog(t, provider)
			close := openUsage(t, m, s)
			format := "claude"
			if provider == "codex" {
				format = "codex"
			}
			appendUsage(t, p, usageFixtureForStep(t, format, s))
			appendUsage(t, p, []byte("{broken json}\n"))
			requireUnknown(t, close(), "no-usage")
		})
	}
}

func TestACompletedLineThatStartedBeforeTheStepIsNotItsUsage(t *testing.T) {
	m, s, p := usageLog(t, "claude_code")
	line := []byte(`{"type":"assistant","message":{"id":"prior","model":"m","usage":{"input_tokens":99,"output_tokens":99}}}` + "\n")
	appendUsage(t, p, line[:20])
	close := openUsage(t, m, s)
	appendUsage(t, p, line[20:])
	appendUsage(t, p, usageFixtureForStep(t, "claude", s))
	requireTokens(t, close(), Tokens{InputTokens: 2, OutputTokens: 158, CacheWriteTokens: 17161, CacheReadTokens: 32489})
}

func TestAStreamedClaudeMessagesLastUsageWins(t *testing.T) {
	m, s, p := usageLog(t, "claude_code")
	close := openUsage(t, m, s)
	appendUsage(t, p, usageFixtureForStep(t, "claude", s))
	appendUsage(t, p, []byte(`{"type":"assistant","message":{"id":"msg-1","model":"claude-haiku-5-5","usage":{"input_tokens":2,"output_tokens":200,"cache_creation_input_tokens":17161,"cache_read_input_tokens":32489}}}`+"\n"))
	requireTokens(t, close(), Tokens{InputTokens: 2, OutputTokens: 200, CacheWriteTokens: 17161, CacheReadTokens: 32489})
}

func TestCodexCacheWritesRemainSeparateWithoutSubtractingThemFromInput(t *testing.T) {
	m, s, p := usageLog(t, "codex")
	close := openUsage(t, m, s)
	appendReplyPath(t, p, s)
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
			appendUsage(t, p, usageFixtureForStep(t, "claude", s))
			close := openUsage(t, m, s)
			if kind == "oversized" {
				appendReplyPath(t, p, s)
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
	f.actions["plan"] = func(s Step) { appendUsage(t, p, usageFixtureForStep(t, "claude", s)) }
	f.errFor = func(Step) error { return errors.New("step failed") }
	res := run(t, e)
	if res.Outcome != Failed || res.Usage == nil {
		t.Fatal(res)
	}
	requireTokens(t, *res.Usage, Tokens{InputTokens: 2, OutputTokens: 158, CacheWriteTokens: 17161, CacheReadTokens: 32489})
}

func openUsage(t *testing.T, m *LogMeter, s Step) func() Usage {
	t.Helper()
	close, err := m.Open(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	return close
}

func appendReplyPath(t *testing.T, path string, s Step) {
	t.Helper()
	line, err := json.Marshal(map[string]any{"type": "user", "message": map[string]string{"content": s.ReplyPath}})
	if err != nil {
		t.Fatal(err)
	}
	appendUsage(t, path, append(line, '\n'))
}

func TestAClaudeMessageStraddlingTwoWindowsOnOneTerminalIsCountedOnlyOnce(t *testing.T) {
	m, s, p := usageLog(t, "claude_code")
	s.ReuseTerminal = "same"
	first := openUsage(t, m, s)
	appendReplyPath(t, p, s)
	appendUsage(t, p, usageFixture(t, "claude"))
	requireTokens(t, first(), Tokens{InputTokens: 2, OutputTokens: 158, CacheWriteTokens: 17161, CacheReadTokens: 32489})
	second := openUsage(t, m, s)
	appendReplyPath(t, p, s)
	appendUsage(t, p, bytes.ReplaceAll(usageFixture(t, "claude"), []byte(`"output_tokens":158`), []byte(`"output_tokens":200`)))
	appendUsage(t, p, []byte(`{"type":"assistant","message":{"id":"next","model":"claude-haiku-5-5","usage":{"input_tokens":3,"output_tokens":4}}}`+"\n"))
	requireTokens(t, second(), Tokens{InputTokens: 3, OutputTokens: 4})
}

func TestAClaudeMessageIsCountedAgainWhenTheEarlierWindowWasUnknown(t *testing.T) {
	m, s, p := usageLog(t, "claude_code")
	first := openUsage(t, m, s)
	appendReplyPath(t, p, s)
	appendUsage(t, p, usageFixture(t, "claude"))
	appendUsage(t, p, []byte("{broken json}\n"))
	requireUnknown(t, first(), "no-usage")
	second := openUsage(t, m, s)
	appendReplyPath(t, p, s)
	appendUsage(t, p, usageFixture(t, "claude"))
	requireTokens(t, second(), Tokens{InputTokens: 2, OutputTokens: 158, CacheWriteTokens: 17161, CacheReadTokens: 32489})
}

func TestOneGrownLogThatDoesNotNameTheReplyPathIsUnknownAsAmbiguous(t *testing.T) {
	for _, provider := range []string{"claude_code", "codex"} {
		t.Run(provider, func(t *testing.T) {
			for _, match := range []bool{false, true} {
				t.Run(fmt.Sprint(match), func(t *testing.T) {
					m, s, p := usageLog(t, provider)
					close := openUsage(t, m, s)
					format := "claude"
					if provider == "codex" {
						format = "codex"
					}
					data := usageFixture(t, format)
					if match {
						data = bytes.ReplaceAll(data, []byte("reply-path"), []byte(s.ReplyPath))
						appendReplyPath(t, p, s)
					}
					appendUsage(t, p, data)
					usage := close()
					if match {
						if !usage.Known {
							t.Fatal(usage)
						}
					} else {
						requireUnknown(t, usage, "ambiguous-log")
					}
				})
			}
		})
	}
}

func TestAStepWithoutAReplyPathNeverGetsUsageFromAGrownLog(t *testing.T) {
	m, s, p := usageLog(t, "claude_code")
	s.ReplyPath = ""
	close := openUsage(t, m, s)
	appendUsage(t, p, usageFixture(t, "claude"))
	requireUnknown(t, close(), "ambiguous-log")
}

func queuedUsageEngine(t *testing.T) (*Engine, *fakeStepper, *bytes.Buffer, Step, Step, chan struct{}) {
	t.Helper()
	m, first, _ := usageLog(t, "claude_code")
	first.Chair.Address = "leads.classifier@ensemble"
	first.Name = "first"
	second := first
	second.Name = "second"
	release := make(chan struct{})
	f := &fakeStepper{t: t, contextActions: map[string]func(context.Context, Step) error{
		"first":  func(context.Context, Step) error { <-release; return errNoReply },
		"second": func(context.Context, Step) error { time.Sleep(2 * time.Second); return errNoReply },
	}}
	records := new(bytes.Buffer)
	return &Engine{Meter: m, Stepper: f, Records: records}, f, records, first, second, release
}

func TestAStepQueuedBehindAnotherOnItsChairRecordsTheWaitAsQueueMSAndNotAsDuration(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e, _, records, first, second, release := queuedUsageEngine(t)
		var wg sync.WaitGroup
		wg.Go(func() { _, _ = e.attempt(context.Background(), first) })
		synctest.Wait()
		wg.Go(func() { _, _ = e.attempt(context.Background(), second) })
		synctest.Wait()
		time.Sleep(10 * time.Second)
		close(release)
		wg.Wait()
		for _, line := range bytes.Split(bytes.TrimSpace(records.Bytes()), []byte("\n")) {
			var rec map[string]any
			if err := json.Unmarshal(line, &rec); err != nil {
				t.Fatal(err)
			}
			if rec["step"] == "second" {
				if rec["queue_ms"] != float64(10000) || rec["duration_ms"] != float64(2000) {
					t.Errorf("queued step = %v", rec)
				}
			} else if rec["queue_ms"] != nil || rec["duration_ms"] != float64(10000) {
				t.Errorf("first step = %v", rec)
			}
		}
	})
}

func TestAStepStillQueuedForItsChairIsNotWatchedAsRunning(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e, _, _, first, second, release := queuedUsageEngine(t)
		var wg sync.WaitGroup
		wg.Go(func() { _, _ = e.attempt(context.Background(), first) })
		synctest.Wait()
		wg.Go(func() { _, _ = e.attempt(context.Background(), second) })
		synctest.Wait()
		check := func(want string) {
			t.Helper()
			e.watch.Lock()
			defer e.watch.Unlock()
			if len(e.watch.running) != 1 {
				t.Errorf("running = %v, want only %s", e.watch.running, want)
			}
			for _, s := range e.watch.running {
				if s.Name != want {
					t.Errorf("watching %s, want %s", s.Name, want)
				}
			}
		}
		check("first")
		close(release)
		synctest.Wait()
		check("second")
		wg.Wait()
	})
}

func TestCancellingTheRunWhileAStepWaitsForItsChairEndsTheWaitWithoutRunningTheStep(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e, f, records, first, second, release := queuedUsageEngine(t)
		firstDone := make(chan struct{})
		go func() { _, _ = e.attempt(context.Background(), first); close(firstDone) }()
		synctest.Wait()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan error, 1)
		go func() { _, err := e.attempt(ctx, second); done <- err }()
		synctest.Wait()
		time.Sleep(10 * time.Second)
		cancel()
		synctest.Wait()
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Errorf("queued step returned %v", err)
			}
		default:
			t.Error("cancelled step still waiting for its chair")
			close(release)
			<-firstDone
			<-done
			return
		}
		f.mu.Lock()
		if len(f.steps) != 1 {
			t.Errorf("stepper ran %v", f.steps)
		}
		f.mu.Unlock()
		recs := recordsOf(t, records, "second")
		if len(recs) != 1 || recs[0].Error != context.Canceled.Error() || recs[0].DurationMS != 0 || recs[0].Usage != nil {
			t.Errorf("cancelled step records = %+v", recs)
		}
		var rec map[string]any
		if err := json.Unmarshal(bytes.TrimSpace(records.Bytes()), &rec); err != nil || rec["queue_ms"] != float64(10000) {
			t.Errorf("queue record = %v, %v", rec, err)
		}
		close(release)
		<-firstDone
		third := openUsage(t, e.Meter.(*LogMeter), second)
		requireUnknown(t, third(), "no-log")
	})
}

func claudeSubagentPath(parent, name string) string {
	return filepath.Join(strings.TrimSuffix(parent, ".jsonl"), "subagents", name)
}

func claudeSubagentLine(t *testing.T, parent, id string, tokens Tokens) []byte {
	t.Helper()
	line, err := json.Marshal(map[string]any{
		"type": "assistant", "isSidechain": true, "sessionId": strings.TrimSuffix(filepath.Base(parent), ".jsonl"),
		"message": map[string]any{
			"id": id, "model": "claude-sonnet-4-6",
			"usage": map[string]int64{"input_tokens": tokens.InputTokens, "output_tokens": tokens.OutputTokens,
				"cache_creation_input_tokens": tokens.CacheWriteTokens, "cache_read_input_tokens": tokens.CacheReadTokens},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return append(line, '\n')
}

func TestAClaudeStepsUsageIncludesItsSubagentTranscripts(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(fmt.Sprint(existing), func(t *testing.T) {
			m, s, p := usageLog(t, "claude_code")
			subagent := claudeSubagentPath(p, "agent-child.jsonl")
			if existing {
				appendReplyPath(t, p, s)
				appendUsage(t, subagent, claudeSubagentLine(t, p, "earlier", Tokens{OutputTokens: 1000}))
			}
			close := openUsage(t, m, s)
			appendUsage(t, p, usageFixtureForStep(t, "claude", s))
			child := Tokens{InputTokens: 10, OutputTokens: 502, CacheWriteTokens: 20695, CacheReadTokens: 30}
			appendUsage(t, subagent, claudeSubagentLine(t, p, "child", child))
			appendUsage(t, subagent, claudeSubagentLine(t, p, "child", child))
			appendUsage(t, strings.TrimSuffix(subagent, ".jsonl")+".meta.json", []byte("{not a transcript}\n"))
			appendUsage(t, claudeSubagentPath(p, "other.jsonl"), []byte("{not an agent transcript}\n"))
			u := close()
			requireTokens(t, u, Tokens{InputTokens: 12, OutputTokens: 660, CacheWriteTokens: 37856, CacheReadTokens: 32519})
			if u.Models["claude-sonnet-4-6"] != child {
				t.Fatal(u.Models)
			}
		})
	}
}

func TestClaudeSubagentUsageIsAttributedOnlyThroughItsParentReplyPath(t *testing.T) {
	for _, match := range []bool{false, true} {
		t.Run(fmt.Sprint(match), func(t *testing.T) {
			m, s, p := usageLog(t, "claude_code")
			close := openUsage(t, m, s)
			parentData := usageFixture(t, "claude")
			if match {
				parentData = usageFixtureForStep(t, "claude", s)
			}
			appendUsage(t, p, parentData)
			appendUsage(t, claudeSubagentPath(p, "agent-child.jsonl"), claudeSubagentLine(t, p, "child", Tokens{OutputTokens: 10}))
			other := filepath.Join(filepath.Dir(p), "other.jsonl")
			appendUsage(t, other, usageFixture(t, "claude"))
			foreign := claudeSubagentPath(other, "agent-foreign.jsonl")
			appendReplyPath(t, foreign, s)
			appendUsage(t, foreign, claudeSubagentLine(t, other, "foreign", Tokens{OutputTokens: 1000}))
			if match {
				requireTokens(t, close(), Tokens{InputTokens: 2, OutputTokens: 168, CacheWriteTokens: 17161, CacheReadTokens: 32489})
			} else {
				requireUnknown(t, close(), "ambiguous-log")
			}
		})
	}
}

func TestAClaudeSubagentMessageStraddlingWindowsIsCountedOnlyOnce(t *testing.T) {
	m, s, p := usageLog(t, "claude_code")
	s.ReuseTerminal = "same"
	subagent := claudeSubagentPath(p, "nested/agent-child.jsonl")
	first := openUsage(t, m, s)
	appendUsage(t, p, usageFixtureForStep(t, "claude", s))
	appendUsage(t, subagent, claudeSubagentLine(t, p, "child", Tokens{OutputTokens: 10}))
	requireTokens(t, first(), Tokens{InputTokens: 2, OutputTokens: 168, CacheWriteTokens: 17161, CacheReadTokens: 32489})
	second := openUsage(t, m, s)
	appendUsage(t, p, bytes.ReplaceAll(usageFixtureForStep(t, "claude", s), []byte("msg-1"), []byte("msg-2")))
	appendUsage(t, subagent, claudeSubagentLine(t, p, "child", Tokens{OutputTokens: 20}))
	appendUsage(t, subagent, claudeSubagentLine(t, p, "next", Tokens{OutputTokens: 3}))
	requireTokens(t, second(), Tokens{InputTokens: 2, OutputTokens: 161, CacheWriteTokens: 17161, CacheReadTokens: 32489})
}

func TestAClaudeWindowCanCountSubagentUsageWhenItsParentAddsOnlyTheReplyPath(t *testing.T) {
	m, s, p := usageLog(t, "claude_code")
	close := openUsage(t, m, s)
	appendReplyPath(t, p, s)
	appendUsage(t, claudeSubagentPath(p, "agent-child.jsonl"), claudeSubagentLine(t, p, "child", Tokens{OutputTokens: 10}))
	requireTokens(t, close(), Tokens{OutputTokens: 10})
}

func TestAnUnknownClaudeSubagentWindowCommitsNoMessageIDs(t *testing.T) {
	m, s, p := usageLog(t, "claude_code")
	subagent := claudeSubagentPath(p, "agent-child.jsonl")
	first := openUsage(t, m, s)
	appendUsage(t, p, usageFixtureForStep(t, "claude", s))
	appendUsage(t, subagent, claudeSubagentLine(t, p, "child", Tokens{OutputTokens: 10}))
	appendUsage(t, subagent, []byte("{broken json}\n"))
	requireUnknown(t, first(), "no-usage")
	second := openUsage(t, m, s)
	appendUsage(t, p, usageFixtureForStep(t, "claude", s))
	appendUsage(t, subagent, claudeSubagentLine(t, p, "child", Tokens{OutputTokens: 10}))
	requireTokens(t, second(), Tokens{InputTokens: 2, OutputTokens: 168, CacheWriteTokens: 17161, CacheReadTokens: 32489})
}

func TestMissingTruncatedOrUnreadableClaudeSubagentLogsMakeUsageUnknown(t *testing.T) {
	for _, kind := range []string{"missing", "truncated", "file-symlink", "session-symlink", "directory-symlink"} {
		t.Run(kind, func(t *testing.T) {
			m, s, p := usageLog(t, "claude_code")
			subagent := claudeSubagentPath(p, "agent-child.jsonl")
			appendReplyPath(t, p, s)
			appendUsage(t, subagent, claudeSubagentLine(t, p, "earlier", Tokens{OutputTokens: 1000}))
			close := openUsage(t, m, s)
			appendUsage(t, p, usageFixtureForStep(t, "claude", s))
			var err error
			switch kind {
			case "missing":
				err = os.Remove(subagent)
			case "truncated":
				err = os.Truncate(subagent, 0)
			default:
				path := subagent
				if kind == "session-symlink" {
					path = strings.TrimSuffix(p, ".jsonl")
				} else if kind == "directory-symlink" {
					path = filepath.Dir(subagent)
				}
				target := filepath.Join(t.TempDir(), "transcript")
				if err = os.Rename(path, target); err != nil {
					t.Fatal(err)
				}
				err = os.Symlink(target, path)
			}
			if err != nil {
				t.Fatal(err)
			}
			requireUnknown(t, close(), "unreadable-log")
		})
	}
}
