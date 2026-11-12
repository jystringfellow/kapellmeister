package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jystringfellow/kapellmeister/internal/loop"
)

type usageStepper struct {
	meter *loop.LogMeter
}

func (f usageStepper) Run(ctx context.Context, s loop.Step) (loop.StepResult, error) {
	var path string
	var lines []byte
	switch s.Chair.Provider {
	case "claude_code":
		path = filepath.Join(f.meter.ClaudeHome, "projects", regexp.MustCompile(`[^a-zA-Z0-9]`).ReplaceAllString(s.Dir, "-"), "session.jsonl")
		lines = []byte(`{"type":"assistant","message":{"id":"` + s.Name + `","model":"claude-haiku-5-5","usage":{"input_tokens":2,"output_tokens":3,"cache_creation_input_tokens":4,"cache_read_input_tokens":5}}}` + "\n")
	case "copilot_cli", "antigravity_cli":
		var err error
		lines, err = os.ReadFile(filepath.Join("..", "..", "internal", "loop", "testdata", s.Chair.Provider+"_usage.jsonl"))
		if err != nil {
			return loop.StepResult{}, err
		}
		lines = []byte(strings.ReplaceAll(strings.ReplaceAll(string(lines), "worktree-path", s.Dir), "reply-path", s.ReplyPath))
		if s.Chair.Provider == "copilot_cli" {
			path = filepath.Join(f.meter.CopilotHome, "session-state", s.Name, "events.jsonl")
		} else {
			path = filepath.Join(f.meter.AntigravityHome, "brain", s.Name, ".system_generated", "logs", "transcript.jsonl")
		}
	case "codex":
		path = filepath.Join(f.meter.CodexHome, "sessions", time.Now().Format("2006/01/02"), "rollout-"+s.Name+".jsonl")
		meta, _ := json.Marshal(map[string]any{"type": "session_meta", "payload": map[string]string{"cwd": s.Dir}})
		lines = append(meta, []byte("\n"+`{"type":"turn_context","payload":{"model":"gpt-6.1-sol"}}`+"\n"+`{"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":20,"cached_input_tokens":7,"output_tokens":6},"last_token_usage":{"input_tokens":20,"cached_input_tokens":7,"output_tokens":6}}}}`+"\n")...)
	}
	if path != "" {
		prompt, _ := json.Marshal(map[string]any{"type": "user", "message": map[string]string{"content": s.ReplyPath}})
		lines = append(lines, append(prompt, '\n')...)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return loop.StepResult{}, err
		}
		f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			return loop.StepResult{}, err
		}
		_, err = f.Write(lines)
		f.Close()
		if err != nil {
			return loop.StepResult{}, err
		}
	}
	text := "done\n"
	if s.Name == "gate" || strings.HasPrefix(s.Name, "classify-") {
		text = "APPROVE"
	}
	if err := os.WriteFile(filepath.Join(s.Dir, s.ReplyPath), []byte(text), 0600); err != nil {
		return loop.StepResult{}, err
	}
	return loop.StepResult{Status: "completed"}, nil
}
func (usageStepper) Release(context.Context, string) error { return nil }

func TestRunRecordsEachStepsOwnUsageForClaudeCodexAndUnknownAgentsAndTotalsThemInResultJSON(t *testing.T) {
	standing := playStanding(t)
	meter := &loop.LogMeter{ClaudeHome: t.TempDir(), CodexHome: t.TempDir()}
	claude := loop.Chair{Provider: "claude_code"}
	codex := loop.Chair{Provider: "codex"}
	dir := filepath.Join(loop.StateDir(), "runs", "usage")
	sum, err := runEnsemble(context.Background(), ensembleRun{RunID: "usage", StateDir: dir, Meter: meter, Stepper: usageStepper{meter: meter}, Progress: io.Discard, Config: loop.Config{RunID: "usage", Worktree: standing.Worktree, Task: "usage", MaxRounds: 1, ReplyGrace: time.Millisecond, Chairs: loop.Chairs{Planner: claude, Implementer: claude, Reviewers: []loop.Chair{codex}, Gate: codex, Classifier: loop.Chair{Provider: "copilot"}}}})
	if err != nil || sum.Outcome != loop.Approved {
		t.Fatalf("%+v %v", sum, err)
	}
	unknown := 0
	steps := recordedSteps(t, dir)
	if len(steps) != 5 {
		t.Fatalf("steps = %d", len(steps))
	}
	for _, rec := range steps {
		if rec.Usage == nil {
			t.Fatalf("%s has no usage", rec.Step)
		}
		if rec.Chair.Provider == "copilot" {
			unknown++
			if rec.Usage.Known || rec.Usage.Reason != "unsupported-provider" {
				t.Fatal(rec.Usage)
			}
			continue
		}
		want := loop.Tokens{InputTokens: 2, OutputTokens: 3, CacheWriteTokens: 4, CacheReadTokens: 5}
		if rec.Chair.Provider == "codex" {
			want = loop.Tokens{InputTokens: 13, CacheReadTokens: 7, OutputTokens: 6}
		}
		if !rec.Usage.Known || rec.Usage.Tokens == nil || *rec.Usage.Tokens != want {
			t.Fatalf("%s usage = %+v", rec.Step, rec.Usage)
		}
	}
	data, err := os.ReadFile(filepath.Join(dir, "result.json"))
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Usage *loop.Usage `json:"usage"`
	}
	if err = json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	want := loop.Tokens{InputTokens: 30, OutputTokens: 18, CacheWriteTokens: 8, CacheReadTokens: 24}
	if unknown != 1 || result.Usage == nil || !result.Usage.Known || result.Usage.Tokens == nil || *result.Usage.Tokens != want || result.Usage.UnknownSteps != 1 || len(result.Usage.Models) != 2 {
		t.Fatalf("total = %+v, steps = %+v", result.Usage, steps)
	}
	var old struct {
		Outcome string `json:"outcome"`
	}
	if err = json.Unmarshal(data, &old); err != nil || old.Outcome != loop.Approved {
		t.Fatalf("%+v %v", old, err)
	}
}

func TestResultJSONOmitsUsageWhenNoMeterIsConfigured(t *testing.T) {
	standing := playStanding(t)
	if value, ok := recordedResult(t, recordRun(t, standing.Worktree, "", plainChairs()))["usage"]; ok {
		t.Fatalf("usage = %v", value)
	}
}

func TestRunRecordsCopilotAndAntigravityUsageInTelemetryAndResultJSON(t *testing.T) {
	standing := playStanding(t)
	meter := &loop.LogMeter{CopilotHome: t.TempDir(), AntigravityHome: t.TempDir()}
	copilot := loop.Chair{Provider: "copilot_cli"}
	antigravity := loop.Chair{Provider: "antigravity_cli"}
	dir := filepath.Join(loop.StateDir(), "runs", "other-usage")
	sum, err := runEnsemble(context.Background(), ensembleRun{RunID: "other-usage", StateDir: dir, Meter: meter, Stepper: usageStepper{meter: meter}, Progress: io.Discard, Config: loop.Config{RunID: "other-usage", Worktree: standing.Worktree, Task: "usage", MaxRounds: 1, ReplyGrace: time.Millisecond, Chairs: loop.Chairs{Planner: copilot, Implementer: copilot, Reviewers: []loop.Chair{antigravity}, Gate: antigravity, Classifier: copilot}}})
	if err != nil || sum.Outcome != loop.Approved {
		t.Fatalf("%+v %v", sum, err)
	}
	steps := recordedSteps(t, dir)
	if len(steps) != 5 {
		t.Fatalf("steps = %d", len(steps))
	}
	var want loop.Tokens
	for _, rec := range steps {
		tokens := loop.Tokens{InputTokens: 10, OutputTokens: 114, CacheWriteTokens: 27276, CacheReadTokens: 51947}
		if rec.Chair.Provider == "antigravity_cli" {
			tokens = loop.Tokens{InputTokens: 13089, OutputTokens: 100, CacheReadTokens: 13276}
		}
		if rec.Usage == nil || !rec.Usage.Known || rec.Usage.Tokens == nil || *rec.Usage.Tokens != tokens {
			t.Fatalf("%s usage = %+v", rec.Step, rec.Usage)
		}
		want.InputTokens += tokens.InputTokens
		want.OutputTokens += tokens.OutputTokens
		want.CacheWriteTokens += tokens.CacheWriteTokens
		want.CacheReadTokens += tokens.CacheReadTokens
	}
	data, err := os.ReadFile(filepath.Join(dir, "result.json"))
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Usage *loop.Usage `json:"usage"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	if result.Usage == nil || !result.Usage.Known || result.Usage.Tokens == nil || *result.Usage.Tokens != want || result.Usage.UnknownSteps != 0 || len(result.Usage.Models) != 2 {
		t.Fatalf("total = %+v, want %+v", result.Usage, want)
	}
}
