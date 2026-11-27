package loop

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

type transcriptStepper struct{ home string }

func (f transcriptStepper) Run(_ context.Context, s Step) (StepResult, error) {
	nonce := regexp.MustCompile(`km-begin ([0-9a-f]+)`).FindStringSubmatch(s.Prompt)[1]
	dir := filepath.Join(f.home, "projects", claudeProjectName(s.Dir))
	if err := os.MkdirAll(dir, 0700); err != nil {
		return StepResult{}, err
	}
	entry := map[string]any{"type": "assistant", "message": map[string]any{"id": "m", "content": []any{map[string]any{"type": "text", "text": "km-begin " + nonce + "\nexact\nkm-end " + nonce}}}}
	data, _ := json.Marshal(entry)
	if err := os.WriteFile(filepath.Join(dir, "session.jsonl"), append(data, '\n'), 0600); err != nil {
		return StepResult{}, err
	}
	return StepResult{LastMessage: "lost"}, nil
}
func (transcriptStepper) Release(context.Context, string) error { return nil }
func TestAMessageChairReadsItsReplyFromTheGrownTranscript(t *testing.T) {
	home, dir := t.TempDir(), t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", home)
	e := Engine{Config: Config{Worktree: dir}, Stepper: transcriptStepper{home}}
	_, err := e.attempt(context.Background(), Step{Name: "review", Dir: dir, Chair: Chair{Provider: "claude_code", Reply: ReplyMessage}, ReplyPath: "reply"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "reply"))
	if err != nil || string(b) != "exact\n" {
		t.Fatalf("reply %q: %v", b, err)
	}
	if e.ReplySource() != "transcript" {
		t.Fatalf("source %q", e.ReplySource())
	}
}

type transcriptPreflightStepper struct{ transcriptStepper }

func (f transcriptPreflightStepper) Seat(_ context.Context, name string, _ Chair, _ string) (Seat, error) {
	return Seat{TerminalID: "t", Session: name}, nil
}
func (f transcriptPreflightStepper) Unseat(context.Context, string) error { return nil }
func (f transcriptPreflightStepper) Run(ctx context.Context, s Step) (StepResult, error) {
	out, err := f.transcriptStepper.Run(ctx, s)
	path := filepath.Join(f.home, "projects", claudeProjectName(s.Dir), "session.jsonl")
	b, readErr := os.ReadFile(path)
	if readErr != nil {
		return out, readErr
	}
	b = []byte(strings.ReplaceAll(string(b), "exact", "ready"))
	if writeErr := os.WriteFile(path, b, 0600); writeErr != nil {
		return out, writeErr
	}
	return out, err
}
func TestMessagePreflightReportsTheTranscriptSource(t *testing.T) {
	home, dir := t.TempDir(), t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", home)
	results, err := Preflight(context.Background(), transcriptPreflightStepper{transcriptStepper{home}}, dir, []Chair{{Provider: "claude_code", Reply: ReplyMessage}}, time.Second)
	if err != nil || len(results) != 1 || !results[0].OK {
		t.Fatalf("results %+v error %v", results, err)
	}
	b, _ := json.Marshal(results[0])
	if !bytes.Contains(b, []byte(`"reply_source":"transcript"`)) {
		t.Fatalf("result %s", b)
	}
}

func TestProbeRepliesAreCapturedExactlyFromTheirTranscripts(t *testing.T) {
	files, err := filepath.Glob("testdata/message-capture/*.transcript.jsonl")
	if err != nil || len(files) != 19 {
		t.Fatalf("fixtures %d: %v", len(files), err)
	}
	for _, path := range files {
		t.Run(filepath.Base(path), func(t *testing.T) {
			home, dir := t.TempDir(), t.TempDir()
			step := Step{Dir: dir, Chair: Chair{Provider: "claude_code"}}
			capture := openReply(home, step)
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			nonceBytes, err := os.ReadFile(strings.TrimSuffix(path, ".transcript.jsonl") + ".nonce")
			if err != nil {
				t.Fatal(err)
			}
			nonce := strings.TrimSpace(string(nonceBytes))
			var prompt struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
			}
			if err := json.Unmarshal(bytes.SplitN(data, []byte{'\n'}, 2)[0], &prompt); err != nil {
				t.Fatal(err)
			}
			body := strings.SplitN(prompt.Message.Content, "<<<BODY\n", 2)
			if len(body) != 2 {
				t.Fatal("missing body")
			}
			expected := strings.SplitN(body[1], "\nBODY>>>", 2)[0]
			if strings.Contains(path, "long-line.") {
				expected = strings.Replace(expected, " \n\nAPPROVE", "\n\nAPPROVE", 1)
			}
			dest := filepath.Join(home, "projects", claudeProjectName(dir))
			if err := os.MkdirAll(dest, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dest, "session.jsonl"), data, 0600); err != nil {
				t.Fatal(err)
			}
			text, ok, source, cause := capture.read(context.Background(), nonce, "screen")
			if !ok || source != ReplyTranscript || text != expected {
				t.Fatalf("source %s ok %v cause %s\ngot %q\nwant %q", source, ok, cause, text, expected)
			}
		})
	}
}

func TestTranscriptWriteAtTheEndOfTheWaitIsCaptured(t *testing.T) {
	old := replyTranscriptWait
	replyTranscriptWait = 100 * time.Millisecond
	defer func() { replyTranscriptWait = old }()
	home, dir := t.TempDir(), t.TempDir()
	c := openReply(home, Step{Dir: dir, Chair: Chair{Provider: "claude_code"}})
	dest := filepath.Join(home, "projects", claudeProjectName(dir))
	if err := os.MkdirAll(dest, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dest, "s.jsonl")
	if err := os.WriteFile(path, []byte("{\"type\":\"user\",\"message\":{\"content\":\"n\"}}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		time.Sleep(80 * time.Millisecond)
		f, e := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
		if e != nil {
			done <- e
			return
		}
		_, e = f.WriteString("{\"type\":\"assistant\",\"message\":{\"id\":\"m\",\"content\":[{\"type\":\"text\",\"text\":\"km-begin n\\nready\\nkm-end n\"}]}}\n")
		f.Close()
		done <- e
	}()
	text, ok, _, cause := c.read(context.Background(), "n", "")
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !ok || text != "ready" {
		t.Fatalf("%q %v %s", text, ok, cause)
	}
}

func TestTranscriptCaptureFailsClosedAndNeverMixesTheScreen(t *testing.T) {
	old := replyTranscriptWait
	replyTranscriptWait = 20 * time.Millisecond
	defer func() { replyTranscriptWait = old }()
	textEntry := func(id, text string, side bool) []byte {
		data, _ := json.Marshal(map[string]any{"type": "assistant", "isSidechain": side, "message": map[string]any{"id": id, "content": []any{map[string]any{"type": "text", "text": text}}}})
		return append(data, '\n')
	}
	valid := textEntry("m", "km-begin n\nexact\nkm-end n", false)
	for _, tc := range []struct {
		name         string
		files        map[string][]byte
		want, source string
		ok           bool
	}{
		{"screen", nil, "screen", ReplyScreen, true},
		{"unrelated assistant", map[string][]byte{"a.jsonl": textEntry("m", "other reply", false)}, "", ReplyTranscript, false},
		{"prompt only", map[string][]byte{"a.jsonl": []byte("{\"type\":\"user\",\"message\":{\"content\":\"n\"}}\n")}, "", ReplyTranscript, false},
		{"two parents", map[string][]byte{"a.jsonl": valid, "b.jsonl": valid}, "", ReplyTranscript, false},
		{"sidechain", map[string][]byte{"a.jsonl": textEntry("m", "km-begin n\nx\nkm-end n", true)}, "", ReplyTranscript, false},
		{"large file without the nonce", map[string][]byte{"a.jsonl": bytes.Repeat([]byte("x"), maxReplyRange+1)}, "", ReplyTranscript, false},
		{"quoted", map[string][]byte{"a.jsonl": textEntry("m", "quoted km-begin n and km-end n", false)}, "", ReplyTranscript, false},
		{"later assistant", map[string][]byte{"a.jsonl": append(append([]byte{}, valid...), textEntry("later", "no markers", false)...)}, "", ReplyTranscript, false},
		{"split", map[string][]byte{"a.jsonl": append(textEntry("same", "km-begin n", false), textEntry("same", "exact\nkm-end n", false)...)}, "exact", ReplyTranscript, true},
		{"long", map[string][]byte{"a.jsonl": textEntry("m", "km-begin n\n"+strings.Repeat("line\n", 200)+"km-end n", false)}, strings.TrimSuffix(strings.Repeat("line\n", 200), "\n"), ReplyTranscript, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home, dir := t.TempDir(), t.TempDir()
			c := openReply(home, Step{Dir: dir, Chair: Chair{Provider: "claude_code"}})
			dest := filepath.Join(home, "projects", claudeProjectName(dir))
			if err := os.MkdirAll(dest, 0700); err != nil {
				t.Fatal(err)
			}
			for name, data := range tc.files {
				if err := os.WriteFile(filepath.Join(dest, name), data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			got, ok, source, cause := c.read(context.Background(), "n", "km-begin n\nscreen\nkm-end n")
			if got != tc.want || ok != tc.ok || source != tc.source {
				t.Fatalf("%q %v %s %s", got, ok, source, cause)
			}
		})
	}
}

func TestTranscriptCaptureReadsOnlyNewParentEntriesAndIgnoresSubagents(t *testing.T) {
	home, dir := t.TempDir(), t.TempDir()
	dest := filepath.Join(home, "projects", claudeProjectName(dir))
	if err := os.MkdirAll(filepath.Join(dest, "s", "subagents"), 0700); err != nil {
		t.Fatal(err)
	}
	parent := filepath.Join(dest, "s.jsonl")
	old := `{"type":"assistant","message":{"id":"old","content":[{"type":"text","text":"km-begin n\nold\nkm-end n"}]}}` + "\n"
	if err := os.WriteFile(parent, []byte(old), 0600); err != nil {
		t.Fatal(err)
	}
	c := openReply(home, Step{Dir: dir, Chair: Chair{Provider: "claude_code"}})
	if err := os.WriteFile(filepath.Join(dest, "s", "subagents", "agent-a.jsonl"), []byte(old), 0600); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(parent, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.WriteString(strings.Replace(old, "old\\nkm-end", "new\\nkm-end", 1))
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	text, ok, source, cause := c.read(context.Background(), "n", "")
	if !ok || source != ReplyTranscript || text != "new" {
		t.Fatalf("%q %v %s %s", text, ok, source, cause)
	}
}
func TestAFinalToolCallCannotBeCapturedAsAReply(t *testing.T) {
	home, dir := t.TempDir(), t.TempDir()
	c := openReply(home, Step{Dir: dir, Chair: Chair{Provider: "claude_code"}})
	dest := filepath.Join(home, "projects", claudeProjectName(dir))
	if err := os.MkdirAll(dest, 0700); err != nil {
		t.Fatal(err)
	}
	data := `{"type":"assistant","message":{"id":"m","content":[{"type":"text","text":"km-begin n\nok\nkm-end n"},{"type":"tool_use"}]}}` + "\n"
	if err := os.WriteFile(filepath.Join(dest, "s.jsonl"), []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	_, ok, source, cause := c.read(context.Background(), "n", "km-begin n\nscreen\nkm-end n")
	if ok || source != ReplyTranscript || !strings.Contains(cause, "tool call") {
		t.Fatalf("%v %s %s", ok, source, cause)
	}
}

func TestTheRunSourceShowsScreenWhenSourcesAreMixed(t *testing.T) {
	e := Engine{}
	e.replyTranscript.Store(true)
	if e.ReplySource() != ReplyTranscript {
		t.Fatal(e.ReplySource())
	}
	e.replyScreen.Store(true)
	if e.ReplySource() != ReplyScreen {
		t.Fatal(e.ReplySource())
	}
}
func TestAConfiguredClaudeHomeWithoutATranscriptUsesTheScreen(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	c := openReply("", Step{Dir: t.TempDir(), Chair: Chair{Provider: "claude_code"}})
	text, ok, source, cause := c.read(context.Background(), "n", "km-begin n\nready\nkm-end n")
	if !ok || text != "ready" || source != ReplyScreen {
		t.Fatalf("%q %v %s %s", text, ok, source, cause)
	}
}
func TestBrokenTranscriptMarkersHaveSpecificDiagnoses(t *testing.T) {
	for _, tc := range []struct{ text, want string }{{"km-end n", "missing begin marker"}, {"km-begin n", "missing end marker"}, {"quoted km-begin n text\nkm-end n", "marker not on a line of its own"}} {
		if got := bracketProblem(tc.text, "n"); got != tc.want {
			t.Errorf("%q: %q", tc.text, got)
		}
	}
}

func TestTranscriptNudgesNameTheCauseAndFailedRepliesRecordIt(t *testing.T) {
	old := replyTranscriptWait
	replyTranscriptWait = 10 * time.Millisecond
	defer func() { replyTranscriptWait = old }()
	for _, tc := range []struct {
		name, text, cause string
		tool, empty       bool
	}{
		{name: "broken begin", text: "km-broken {nonce}\nreview\nkm-end {nonce}", cause: "missing begin marker"},
		{name: "missing end", text: "km-begin {nonce}\nreview", cause: "missing end marker"},
		{name: "inline marker", text: "quoted km-begin {nonce}\nreview\nkm-end {nonce}", cause: "marker not on a line of its own"},
		{name: "tool call", text: "km-begin {nonce}\nreview\nkm-end {nonce}", cause: "tool call in the final message", tool: true},
		{name: "missing both", text: "No markers for {nonce}", cause: "missing begin and end markers"},
		{name: "empty reply", text: "km-begin {nonce}\n\nkm-end {nonce}", cause: "empty reply", empty: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home, dir := t.TempDir(), t.TempDir()
			var records bytes.Buffer
			var prompts, nonces []string
			e := Engine{Config: Config{Worktree: dir}, ClaudeHome: home, Records: &records}
			e.Stepper = messageFuncStepper(func(s Step) (StepResult, error) {
				prompts = append(prompts, s.Prompt)
				nonce := regexp.MustCompile(`km-begin ([0-9a-f]+)`).FindStringSubmatch(s.Prompt)[1]
				nonces = append(nonces, nonce)
				if len(prompts) == 2 && s.ReuseTerminal != "chair" {
					t.Errorf("nudge reused %q", s.ReuseTerminal)
				}
				blocks := []map[string]string{{"type": "text", "text": strings.ReplaceAll(tc.text, "{nonce}", nonce)}}
				if tc.tool {
					blocks = append(blocks, map[string]string{"type": "tool_use"})
				}
				data, err := json.Marshal(map[string]any{"type": "assistant", "message": map[string]any{"id": fmt.Sprint(len(prompts)), "content": blocks}})
				if err != nil {
					return StepResult{}, err
				}
				dest := filepath.Join(home, "projects", claudeProjectName(dir))
				if err := os.MkdirAll(dest, 0700); err != nil {
					return StepResult{}, err
				}
				f, err := os.OpenFile(filepath.Join(dest, "session.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
				if err != nil {
					return StepResult{}, err
				}
				_, err = f.Write(append(data, '\n'))
				closeErr := f.Close()
				if err != nil {
					return StepResult{}, err
				}
				if closeErr != nil {
					return StepResult{}, closeErr
				}
				return StepResult{TerminalID: "chair", Status: "completed", LastMessage: "km-begin " + nonce + "\nscreen must not win\nkm-end " + nonce}, nil
			})
			_, err := e.step(context.Background(), Step{Name: "review", Chair: Chair{Provider: "claude_code", Reply: ReplyMessage}, ReplyPath: "reply"})
			want := "final-message reply not captured: transcript: " + tc.cause
			if err == nil || !strings.HasPrefix(err.Error(), want) || strings.Contains(err.Error(), "no reply file") || !errors.Is(err, errNoReply) {
				t.Fatalf("error %v; want %q and errNoReply", err, want)
			}
			var empty emptyReplyError
			if errors.As(err, &empty) != tc.empty {
				t.Fatalf("empty error %v: %v", tc.empty, err)
			}
			if len(prompts) != 2 || nonces[0] == nonces[1] {
				t.Fatalf("prompts %d nonces %v", len(prompts), nonces)
			}
			if !tc.empty && !strings.Contains(prompts[1], "The problem was: "+tc.cause+".") {
				t.Errorf("nudge does not name %q: %s", tc.cause, prompts[1])
			}
			for _, prompt := range prompts {
				for _, limit := range []string{"35 lines", "200 characters", "avoid diffs and tables", "start no line with"} {
					if strings.Contains(prompt, limit) {
						t.Errorf("transcript prompt contains screen limit %q: %s", limit, prompt)
					}
				}
			}
			dec := json.NewDecoder(&records)
			for _, step := range []string{"review", "review-nudge"} {
				var rec Record
				if err := dec.Decode(&rec); err != nil {
					t.Fatal(err)
				}
				if rec.Step != step || !strings.HasPrefix(rec.Error, want) || strings.Contains(rec.Error, "no reply file") {
					t.Errorf("record %+v; want step %s and error %q", rec, step, want)
				}
			}
			if dec.More() {
				t.Fatal("unexpected extra attempt")
			}
			if _, err := os.Stat(filepath.Join(dir, "reply")); !os.IsNotExist(err) {
				t.Fatalf("reply remains: %v", err)
			}
		})
	}
}

type cancellingStepper struct {
	cancel  context.CancelFunc
	prompts int
}

func (f *cancellingStepper) Run(_ context.Context, s Step) (StepResult, error) {
	f.prompts++
	f.cancel()
	return StepResult{TerminalID: "t", LastMessage: "lost"}, nil
}
func (*cancellingStepper) Release(context.Context, string) error { return nil }

func TestACancelledRunIsNotNudgedForAMissingMessageReply(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f := &cancellingStepper{cancel: cancel}
	dir := t.TempDir()
	e := Engine{Config: Config{Worktree: dir}, Stepper: f}
	_, err := e.step(ctx, Step{Name: "review", Dir: dir, Chair: Chair{Provider: "claude_code", Reply: ReplyMessage}, ReplyPath: "reply"})
	if !errors.Is(err, context.Canceled) || errors.Is(err, errNoReply) {
		t.Fatalf("error %v", err)
	}
	if f.prompts != 1 {
		t.Fatalf("prompts %d", f.prompts)
	}
}

func TestTranscriptReplyRequiresASettledCompleteLine(t *testing.T) {
	old := replyTranscriptWait
	replyTranscriptWait = 30 * time.Millisecond
	defer func() { replyTranscriptWait = old }()
	for _, tail := range []string{`{"type":"assistant"`, `{"type":"assistant","message":{"id":"m","content":[{"type":"tool_use"}]}}`} {
		t.Run(tail, func(t *testing.T) {
			home, dir := t.TempDir(), t.TempDir()
			c := openReply(home, Step{Dir: dir, Chair: Chair{Provider: "claude_code"}})
			dest := filepath.Join(home, "projects", claudeProjectName(dir))
			if err := os.MkdirAll(dest, 0700); err != nil {
				t.Fatal(err)
			}
			valid := `{"type":"assistant","message":{"id":"m","content":[{"type":"text","text":"km-begin nonce\nready\nkm-end nonce"}]}}` + "\n"
			if err := os.WriteFile(filepath.Join(dest, "s.jsonl"), []byte(valid+tail), 0600); err != nil {
				t.Fatal(err)
			}
			_, ok, _, cause := c.read(context.Background(), "nonce", "")
			if ok || cause != "the transcript ended in a half-written line" {
				t.Fatalf("ok %v cause %s", ok, cause)
			}
		})
	}
}

func TestTranscriptReplyPreservesJoiners(t *testing.T) {
	home, dir := t.TempDir(), t.TempDir()
	c := openReply(home, Step{Dir: dir, Chair: Chair{Provider: "claude_code"}})
	dest := filepath.Join(home, "projects", claudeProjectName(dir))
	if err := os.MkdirAll(dest, 0700); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(map[string]any{"type": "assistant", "message": map[string]any{"id": "m", "content": "km-begin nonce\n👩‍👩‍👧 a‍b\nkm-end nonce"}})
	if err := os.WriteFile(filepath.Join(dest, "s.jsonl"), append(data, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	got, ok, _, cause := c.read(context.Background(), "nonce", "")
	if !ok || got != "👩‍👩‍👧 a‍b" {
		t.Fatalf("%q %v %s", got, ok, cause)
	}
}

func TestTranscriptReplyReadsPastLargeToolOutput(t *testing.T) {
	home, dir := t.TempDir(), t.TempDir()
	c := openReply(home, Step{Dir: dir, Chair: Chair{Provider: "claude_code"}})
	dest := filepath.Join(home, "projects", claudeProjectName(dir))
	if err := os.MkdirAll(dest, 0700); err != nil {
		t.Fatal(err)
	}
	data := []byte(`{"type":"user","message":{"content":"` + strings.Repeat("x", 9<<20) + `"}}` + "\n" + `{"type":"assistant","message":{"id":"m","content":"km-begin nonce\nready\nkm-end nonce"}}` + "\n")
	if err := os.WriteFile(filepath.Join(dest, "s.jsonl"), data, 0600); err != nil {
		t.Fatal(err)
	}
	got, ok, _, cause := c.read(context.Background(), "nonce", "")
	if !ok || got != "ready" {
		t.Fatalf("%q %v %s", got, ok, cause)
	}
}

func TestOversizedFinalTranscriptMessageFailsClosed(t *testing.T) {
	home, dir := t.TempDir(), t.TempDir()
	c := openReply(home, Step{Dir: dir, Chair: Chair{Provider: "claude_code"}})
	dest := filepath.Join(home, "projects", claudeProjectName(dir))
	if err := os.MkdirAll(dest, 0700); err != nil {
		t.Fatal(err)
	}
	data := []byte(`{"type":"assistant","message":{"id":"m","content":"km-begin nonce\n` + strings.Repeat("x", maxReplyRange) + `\nkm-end nonce"}}` + "\n")
	if err := os.WriteFile(filepath.Join(dest, "s.jsonl"), data, 0600); err != nil {
		t.Fatal(err)
	}
	_, ok, _, cause := c.read(context.Background(), "nonce", "")
	if ok || cause != "the final message exceeds 8 MiB" {
		t.Fatalf("%v %s", ok, cause)
	}
}

func TestTranscriptSizeNudgeNamesTheCauseWithoutClaimingBrokenMarkers(t *testing.T) {
	got := nudgeFor(ReplyTranscript, "the final message exceeds 8 MiB")
	if !strings.Contains(got, "8 MiB") || !strings.Contains(got, "shorter") || strings.Contains(got, "bracketed") || strings.Contains(got, "marker") {
		t.Fatal(got)
	}
}

func TestTranscriptReplyRemovesOnlyTerminalControls(t *testing.T) {
	text, ok := transcriptReply("km-begin nonce\n\n👩‍👩‍👧 a‍b\x00\x1b\a\r\x7f\u009b\t\ufeff\n\nkm-end nonce", "nonce")
	if !ok || text != "👩‍👩‍👧 a‍b\t\ufeff" {
		t.Fatalf("%q %v", text, ok)
	}
	screen, _ := bracketedReply("km-begin nonce\na‍b\nkm-end nonce", "nonce")
	if screen != "a b" {
		t.Fatalf("screen %q", screen)
	}
}

func TestUnrelatedMalformedAndStringContentTranscriptsDoNotPreventReply(t *testing.T) {
	home, dir := t.TempDir(), t.TempDir()
	c := openReply(home, Step{Dir: dir, Chair: Chair{Provider: "claude_code"}})
	dest := filepath.Join(home, "projects", claudeProjectName(dir))
	if err := os.MkdirAll(dest, 0700); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string]string{
		"malformed.jsonl": "garbage\n",
		"string.jsonl":    `{"type":"assistant","message":{"id":"other","content":"unrelated reply"}}` + "\n",
		"valid.jsonl":     `{"type":"assistant","message":{"id":"m","content":"km-begin nonce\nready\nkm-end nonce"}}` + "\n",
	} {
		if err := os.WriteFile(filepath.Join(dest, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	got, ok, _, cause := c.read(context.Background(), "nonce", "")
	if !ok || got != "ready" {
		t.Fatalf("%q %v %s", got, ok, cause)
	}
}

func TestANonceAcrossReadChunksStillMakesMalformedTranscriptFailClosed(t *testing.T) {
	home, dir := t.TempDir(), t.TempDir()
	c := openReply(home, Step{Dir: dir, Chair: Chair{Provider: "claude_code"}})
	const nonce = "nonce-crossing-chunks"
	prefix := `{"type":"user","message":{"content":"`
	prompt := prefix + strings.Repeat("x", replyChunk-len(prefix)-2) + nonce + `"}}` + "\n"
	path := filepath.Join(home, "projects", claudeProjectName(dir), "s.jsonl")
	appendUsage(t, path, []byte(prompt+"broken JSON\n"))
	_, ok, source, cause := c.read(context.Background(), nonce, "km-begin "+nonce+"\nscreen must not win\nkm-end "+nonce)
	if ok || source != ReplyTranscript || cause != "invalid transcript JSON" {
		t.Fatalf("ok %v source %q cause %q", ok, source, cause)
	}
}

func TestTwoMatchingTranscriptsFailClosedThroughEngineStep(t *testing.T) {
	old := replyTranscriptWait
	replyTranscriptWait = 10 * time.Millisecond
	defer func() { replyTranscriptWait = old }()
	home, dir := t.TempDir(), t.TempDir()
	var prompts []string
	e := Engine{Config: Config{Worktree: dir}, ClaudeHome: home}
	e.Stepper = messageFuncStepper(func(s Step) (StepResult, error) {
		prompts = append(prompts, s.Prompt)
		nonce := regexp.MustCompile(`km-begin ([0-9a-f]+)`).FindStringSubmatch(s.Prompt)[1]
		dest := filepath.Join(home, "projects", claudeProjectName(dir))
		if err := os.MkdirAll(dest, 0700); err != nil {
			return StepResult{}, err
		}
		data, _ := json.Marshal(map[string]any{"type": "assistant", "message": map[string]any{"id": nonce, "content": "km-begin " + nonce + "\nreply\nkm-end " + nonce}})
		for _, name := range []string{"a.jsonl", "b.jsonl"} {
			f, err := os.OpenFile(filepath.Join(dest, name), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
			if err != nil {
				return StepResult{}, err
			}
			_, err = f.Write(append(data, '\n'))
			f.Close()
			if err != nil {
				return StepResult{}, err
			}
		}
		return StepResult{TerminalID: "chair", LastMessage: "km-begin " + nonce + "\nscreen\nkm-end " + nonce}, nil
	})
	_, err := e.step(context.Background(), Step{Name: "review", Chair: Chair{Provider: "claude_code", Reply: ReplyMessage}, ReplyPath: "reply"})
	if !errors.Is(err, errNoReply) || len(prompts) != 2 || !strings.Contains(prompts[1], "multiple transcripts contain the nonce") {
		t.Fatalf("error %v prompts %v", err, prompts)
	}
	if _, err := os.Stat(filepath.Join(dir, "reply")); !os.IsNotExist(err) {
		t.Fatalf("reply exists: %v", err)
	}
}

func TestEngineWritesTranscriptJoinersExactly(t *testing.T) {
	home, dir := t.TempDir(), t.TempDir()
	e := Engine{Config: Config{Worktree: dir}, ClaudeHome: home}
	e.Stepper = messageFuncStepper(func(s Step) (StepResult, error) {
		nonce := regexp.MustCompile(`km-begin ([0-9a-f]+)`).FindStringSubmatch(s.Prompt)[1]
		dest := filepath.Join(home, "projects", claudeProjectName(dir))
		if err := os.MkdirAll(dest, 0700); err != nil {
			return StepResult{}, err
		}
		data, _ := json.Marshal(map[string]any{"type": "assistant", "message": map[string]any{"id": "m", "content": "km-begin " + nonce + "\n👩‍👩‍👧 a‍b\nkm-end " + nonce}})
		if err := os.WriteFile(filepath.Join(dest, "s.jsonl"), append(data, '\n'), 0600); err != nil {
			return StepResult{}, err
		}
		return StepResult{TerminalID: "chair"}, nil
	})
	_, err := e.step(context.Background(), Step{Name: "review", Chair: Chair{Provider: "claude_code", Reply: ReplyMessage}, ReplyPath: "reply"})
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "reply"))
	if err != nil || string(data) != "👩‍👩‍👧 a‍b\n" {
		t.Fatalf("%q %v", data, err)
	}
}

func TestTranscriptNudgesDescribeTheirActualCauses(t *testing.T) {
	bracketOpening := "You ended your turn without a complete bracketed final message. Finish anything still owed, then give the whole reply again."
	otherOpening := "km could not capture your final message. Finish anything still owed, then give the whole reply again."
	for _, tc := range []struct {
		cause   string
		bracket bool
	}{
		{"marker not on a line of its own", true}, {"missing begin and end markers", true}, {"missing begin marker", true}, {"missing end marker", true}, {"repeated or out of order markers", true},
		{"tool call in the final message", false}, {replySizeCause, false}, {"the transcript ended in a half-written line", false}, {"the transcript was still being written", false}, {"no transcript holds the nonce", false}, {"no final message in the transcript", false}, {"multiple transcripts contain the nonce", false}, {"invalid transcript JSON", false}, {"empty reply", false}, {"transcript disappeared or shrank", false}, {"transcript shrank", false}, {"read error", false},
	} {
		t.Run(tc.cause, func(t *testing.T) {
			opening := otherOpening
			if tc.bracket {
				opening = bracketOpening
			}
			want := opening
			if tc.cause == replySizeCause {
				want += " Make the reply shorter."
			}
			want += " The problem was: " + tc.cause + "."
			if got := nudgeFor(ReplyTranscript, tc.cause); got != want {
				t.Fatalf("got %q; want %q", got, want)
			}
		})
	}
	wantScreen := bracketOpening + " Stay within about 35 lines, counting wrapped lines; keep lines under 200 characters; start no line with >, ❯, ● or ⏺, including inside code blocks; avoid diffs and tables."
	if got := nudgeFor(ReplyScreen, replySizeCause); got != wantScreen {
		t.Fatalf("screen got %q; want %q", got, wantScreen)
	}
}

func TestMalformedLineInNonceHoldingTranscriptFailsClosed(t *testing.T) {
	home, dir := t.TempDir(), t.TempDir()
	c := openReply(home, Step{Dir: dir, Chair: Chair{Provider: "claude_code"}})
	dest := filepath.Join(home, "projects", claudeProjectName(dir))
	if err := os.MkdirAll(dest, 0700); err != nil {
		t.Fatal(err)
	}
	data := `{"type":"user","message":{"content":"nonce"}}` + "\n" + "broken json\n"
	if err := os.WriteFile(filepath.Join(dest, "s.jsonl"), []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	_, ok, _, cause := c.read(context.Background(), "nonce", "")
	if ok || cause != "invalid transcript JSON" {
		t.Fatalf("%v %s", ok, cause)
	}
}

func TestSettledTranscriptReplyDoesNotWaitForTheDeadline(t *testing.T) {
	home, dir := t.TempDir(), t.TempDir()
	c := openReply(home, Step{Dir: dir, Chair: Chair{Provider: "claude_code"}})
	dest := filepath.Join(home, "projects", claudeProjectName(dir))
	if err := os.MkdirAll(dest, 0700); err != nil {
		t.Fatal(err)
	}
	data := `{"type":"assistant","message":{"content":"km-begin nonce\nready\nkm-end nonce"}}` + "\n"
	if err := os.WriteFile(filepath.Join(dest, "s.jsonl"), []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, ok, _, cause := c.read(context.Background(), "nonce", "")
	if !ok || time.Since(start) > time.Second {
		t.Fatalf("ok %v after %s: %s", ok, time.Since(start), cause)
	}
}

func TestLateTranscriptReplyWaitsAFullIntervalAfterItsFirstSighting(t *testing.T) {
	old := replyTranscriptWait
	replyTranscriptWait = 200 * time.Millisecond
	defer func() { replyTranscriptWait = old }()
	home, dir := t.TempDir(), t.TempDir()
	c := openReply(home, Step{Dir: dir, Chair: Chair{Provider: "claude_code"}})
	dest := filepath.Join(home, "projects", claudeProjectName(dir))
	if err := os.MkdirAll(dest, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dest, "s.jsonl")
	if err := os.WriteFile(path, []byte("{\"type\":\"user\",\"message\":{\"content\":\"nonce\"}}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	var written time.Time
	calls := 0
	c.poll = func() {
		calls++
		if calls == 2 {
			time.Sleep(time.Until(start.Add(195 * time.Millisecond)))
			f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
			if err != nil {
				t.Fatal(err)
			}
			_, err = f.WriteString(`{"type":"assistant","message":{"id":"m","content":"km-begin nonce\nready\nkm-end nonce"}}` + "\n")
			f.Close()
			if err != nil {
				t.Fatal(err)
			}
			written = time.Now()
		}
	}
	_, ok, _, cause := c.read(context.Background(), "nonce", "")
	elapsed := time.Since(written)
	if !ok || elapsed < 20*time.Millisecond {
		t.Fatalf("ok %v cause %q interval %s", ok, cause, elapsed)
	}
}

func TestTranscriptThatKeepsGrowingDoesNotSettle(t *testing.T) {
	old := replyTranscriptWait
	replyTranscriptWait = 50 * time.Millisecond
	defer func() { replyTranscriptWait = old }()
	home, dir := t.TempDir(), t.TempDir()
	c := openReply(home, Step{Dir: dir, Chair: Chair{Provider: "claude_code"}})
	dest := filepath.Join(home, "projects", claudeProjectName(dir))
	if err := os.MkdirAll(dest, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dest, "s.jsonl")
	valid := `{"type":"assistant","message":{"id":"m","content":"km-begin nonce\nready\nkm-end nonce"}}` + "\n"
	if err := os.WriteFile(path, []byte(valid), 0600); err != nil {
		t.Fatal(err)
	}
	c.poll = func() {
		f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			t.Fatal(err)
		}
		_, err = f.WriteString("{\"type\":\"user\"}\n")
		f.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
	_, ok, _, cause := c.read(context.Background(), "nonce", "")
	if ok || cause != "the transcript was still being written" {
		t.Fatalf("ok %v cause %q", ok, cause)
	}
}

func TestCompletedTranscriptLineIsJudgedAsPartOfTheFinalMessage(t *testing.T) {
	old := replyTranscriptWait
	replyTranscriptWait = 100 * time.Millisecond
	defer func() { replyTranscriptWait = old }()
	home, dir := t.TempDir(), t.TempDir()
	c := openReply(home, Step{Dir: dir, Chair: Chair{Provider: "claude_code"}})
	dest := filepath.Join(home, "projects", claudeProjectName(dir))
	if err := os.MkdirAll(dest, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dest, "s.jsonl")
	valid := `{"type":"assistant","message":{"id":"m","content":"km-begin nonce\nready\nkm-end nonce"}}` + "\n" + `{"type":"assistant","message":{"id":"m","content":[{"type":"tool_use"}]}}`
	if err := os.WriteFile(path, []byte(valid), 0600); err != nil {
		t.Fatal(err)
	}
	calls := 0
	c.poll = func() {
		calls++
		if calls == 2 {
			f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
			if err != nil {
				t.Fatal(err)
			}
			_, err = f.WriteString("\n")
			f.Close()
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	_, ok, _, cause := c.read(context.Background(), "nonce", "")
	if ok || cause != "tool call in the final message" {
		t.Fatalf("ok %v cause %q", ok, cause)
	}
}

func TestMultipleLinesOfOneOversizedFinalMessageFailClosed(t *testing.T) {
	home, dir := t.TempDir(), t.TempDir()
	c := openReply(home, Step{Dir: dir, Chair: Chair{Provider: "claude_code"}})
	dest := filepath.Join(home, "projects", claudeProjectName(dir))
	if err := os.MkdirAll(dest, 0700); err != nil {
		t.Fatal(err)
	}
	line := func(text string) []byte {
		data, err := json.Marshal(map[string]any{"type": "assistant", "message": map[string]any{"id": "m", "content": text}})
		if err != nil {
			t.Fatal(err)
		}
		return append(data, '\n')
	}
	data := append(line("km-begin nonce\n"+strings.Repeat("x", maxReplyRange/2)), line(strings.Repeat("y", maxReplyRange/2)+"\nkm-end nonce")...)
	if err := os.WriteFile(filepath.Join(dest, "s.jsonl"), data, 0600); err != nil {
		t.Fatal(err)
	}
	_, ok, _, cause := c.read(context.Background(), "nonce", "")
	if ok || cause != replySizeCause {
		t.Fatalf("ok %v cause %q", ok, cause)
	}
}
