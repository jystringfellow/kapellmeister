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
	dir := filepath.Join(f.home, "projects", claudeProjectName.ReplaceAllString(s.Dir, "-"))
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
	path := filepath.Join(f.home, "projects", claudeProjectName.ReplaceAllString(s.Dir, "-"), "session.jsonl")
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
			dest := filepath.Join(home, "projects", claudeProjectName.ReplaceAllString(dir, "-"))
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
	dest := filepath.Join(home, "projects", claudeProjectName.ReplaceAllString(dir, "-"))
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
		{"oversize", map[string][]byte{"a.jsonl": bytes.Repeat([]byte("x"), maxReplyRange+1)}, "", ReplyTranscript, false},
		{"quoted", map[string][]byte{"a.jsonl": textEntry("m", "quoted km-begin n and km-end n", false)}, "", ReplyTranscript, false},
		{"later assistant", map[string][]byte{"a.jsonl": append(append([]byte{}, valid...), textEntry("later", "no markers", false)...)}, "", ReplyTranscript, false},
		{"split", map[string][]byte{"a.jsonl": append(textEntry("same", "km-begin n", false), textEntry("same", "exact\nkm-end n", false)...)}, "exact", ReplyTranscript, true},
		{"long", map[string][]byte{"a.jsonl": textEntry("m", "km-begin n\n"+strings.Repeat("line\n", 200)+"km-end n", false)}, strings.TrimSuffix(strings.Repeat("line\n", 200), "\n"), ReplyTranscript, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home, dir := t.TempDir(), t.TempDir()
			c := openReply(home, Step{Dir: dir, Chair: Chair{Provider: "claude_code"}})
			dest := filepath.Join(home, "projects", claudeProjectName.ReplaceAllString(dir, "-"))
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
	dest := filepath.Join(home, "projects", claudeProjectName.ReplaceAllString(dir, "-"))
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
	dest := filepath.Join(home, "projects", claudeProjectName.ReplaceAllString(dir, "-"))
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
				dest := filepath.Join(home, "projects", claudeProjectName.ReplaceAllString(dir, "-"))
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
