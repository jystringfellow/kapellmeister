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
	"sync"
	"testing"
	"time"
)

func TestOnlyTheTextBetweenTheNonceLinesBecomesTheReply(t *testing.T) {
	got, ok := bracketedReply("noise\n km-begin abcd \n\nfirst\tline\x01\nsecond\u200b\n\nkm-end abcd\nnoise", "abcd")
	if !ok || got != "first\tline \nsecond " {
		t.Fatalf("got %q, %v", got, ok)
	}
}

func TestACaptureMissingEitherLineOutOfOrderDuplicatedOrWithAnotherNonceIsNoReply(t *testing.T) {
	for _, capture := range []string{"km-begin n", "km-end n", "km-end n\nkm-begin n", "km-begin n\nkm-begin n\nkm-end n", "km-begin n\nkm-end other"} {
		if _, ok := bracketedReply(capture, "n"); ok {
			t.Errorf("accepted %q", capture)
		}
	}
}

func TestAReplyCutAtAFullWidthRuleIsNoReplyNeverAShortReply(t *testing.T) {
	capture := "km-begin n\npartial\n" + strings.Repeat("─", 20)
	if _, ok := bracketedReply(capture, "n"); ok {
		t.Fatal("accepted truncated reply")
	}
}

func TestBracketedReplyNormalizesCRLFAndTrimsCleanedBlankLines(t *testing.T) {
	got, ok := bracketedReply("km-begin n\r\n\u200b\r\nready\r\n\x01\r\nkm-end n\r\n", "n")
	if !ok || got != "ready" {
		t.Fatalf("got %q, %v", got, ok)
	}
}

func TestBracketedReplyRejectsAnEmptyNonceAndDuplicateEnd(t *testing.T) {
	for _, tc := range []struct{ capture, nonce string }{{"km-begin \nready\nkm-end ", ""}, {"km-begin n\nx\nkm-end n\nkm-end n", "n"}} {
		if _, ok := bracketedReply(tc.capture, tc.nonce); ok {
			t.Fatalf("accepted %q", tc.capture)
		}
	}
}

type messageStepper struct {
	prompts   []string
	truncated bool
}

func (f *messageStepper) Run(_ context.Context, s Step) (StepResult, error) {
	f.prompts = append(f.prompts, s.Prompt)
	m := regexp.MustCompile(`km-begin ([0-9a-f]+)`).FindStringSubmatch(s.Prompt)
	if len(m) == 0 {
		return StepResult{TerminalID: "t", LastMessage: "APPROVE"}, nil
	}
	text := "km-begin " + m[1] + "\nreply\n"
	if f.truncated {
		text += strings.Repeat("─", 20)
	} else {
		text += "km-end " + m[1]
	}
	return StepResult{TerminalID: "t", LastMessage: text}, nil
}
func (*messageStepper) Release(context.Context, string) error { return nil }

func TestAMessageChairWritesOnlyItsBracketedReply(t *testing.T) {
	dir := t.TempDir()
	f := &messageStepper{}
	e := Engine{Config: Config{Worktree: dir}, Stepper: f}
	_, err := e.step(context.Background(), Step{Name: "review", Chair: Chair{Reply: ReplyMessage}, ReplyPath: "reply"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "reply"))
	if err != nil || string(b) != "reply\n" {
		t.Fatalf("reply %q: %v", b, err)
	}
}

func TestAFullWidthRuleCutsTheReplyAndTriggersANudgeWithANewNonce(t *testing.T) {
	dir := t.TempDir()
	f := &messageStepper{truncated: true}
	e := Engine{Config: Config{Worktree: dir}, Stepper: f}
	_, err := e.step(context.Background(), Step{Name: "review", Chair: Chair{Reply: ReplyMessage}, ReplyPath: "reply"})
	if !errors.Is(err, errNoReply) {
		t.Fatalf("error %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "reply")); !os.IsNotExist(err) {
		t.Fatalf("reply exists: %v", err)
	}
	if len(f.prompts) != 2 {
		t.Fatalf("prompts %d", len(f.prompts))
	}
	re := regexp.MustCompile(`km-begin ([0-9a-f]+)`)
	a, b := re.FindStringSubmatch(f.prompts[0]), re.FindStringSubmatch(f.prompts[1])
	if len(a) < 2 || len(b) < 2 || a[1] == b[1] {
		t.Fatalf("nonces %v %v", a, b)
	}
}

func TestTheRealScreenCaptureRetainsBothMarkers(t *testing.T) {
	data, err := os.ReadFile("testdata/message-screen.txt")
	if err != nil {
		t.Fatal(err)
	}
	text, ok := bracketedReply(string(data), "1234567890abcdef")
	if !ok || !strings.HasSuffix(text, "APPROVE") {
		t.Fatalf("capture %q, %v", text, ok)
	}
}

type messagePreflightStepper struct{ messageStepper }

func (f *messagePreflightStepper) Seat(_ context.Context, name string, _ Chair, _ string) (Seat, error) {
	return Seat{TerminalID: "t", Session: name}, nil
}
func (f *messagePreflightStepper) Unseat(context.Context, string) error { return nil }
func (f *messagePreflightStepper) Run(_ context.Context, s Step) (StepResult, error) {
	m := regexp.MustCompile(`km-begin ([0-9a-f]+)`).FindStringSubmatch(s.Prompt)
	if len(m) == 0 {
		return StepResult{}, nil
	}
	return StepResult{LastMessage: "km-begin " + m[1] + "\nready\nkm-end " + m[1]}, nil
}
func TestAMessageChairPreflightRequiresABracketedReady(t *testing.T) {
	dir := t.TempDir()
	f := &messagePreflightStepper{}
	results, err := Preflight(context.Background(), f, dir, []Chair{{Provider: "claude_code", Reply: ReplyMessage}}, time.Second)
	if err != nil || len(results) != 1 || !results[0].OK {
		t.Fatalf("results %+v error %v", results, err)
	}
}
func TestMessageAndFileChairsArePreflightedSeparately(t *testing.T) {
	chairs := UniqueChairs(Chair{Provider: "claude_code", Profile: "p"}, Chair{Provider: "claude_code", Profile: "p", Reply: ReplyMessage})
	if len(chairs) != 2 {
		t.Fatalf("chairs %+v", chairs)
	}
}

func TestAMessageReviewAsksForTheVerdictInTheFinalMessage(t *testing.T) {
	e := Engine{}
	spec, _ := DefaultScore().Step("review")
	p := e.brief(spec, handoff{}, "reply.md", Chair{Reply: ReplyMessage})
	if strings.Contains(p, "reply.md") || strings.Contains(p, "reply with one short sentence") || !strings.Contains(p, "before the closing km-end line") {
		t.Fatalf("prompt %s", p)
	}
}

func TestAMessageChairRecordsTheScreenSource(t *testing.T) {
	dir := t.TempDir()
	f := &messageStepper{}
	var records bytes.Buffer
	e := Engine{Config: Config{Worktree: dir}, Stepper: f, Records: &records}
	_, err := e.step(context.Background(), Step{Name: "review", Chair: Chair{Reply: ReplyMessage}, ReplyPath: "reply"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(records.String(), `"reply_source":"screen"`) || e.ReplySource() != ReplyScreen {
		t.Fatalf("records %s source %q", records.String(), e.ReplySource())
	}
}
func TestABlockedMessagePreflightReportsTheUntrustedCapture(t *testing.T) {
	r := PreflightResult{CaptureFailed: true, Error: "final-message reply not captured", Screen: screenTail("pane\x01text")}
	if !strings.Contains(r.Reason(), "final-message reply not captured") || !strings.Contains(r.Reason(), "pane text") {
		t.Fatalf("reason %q", r.Reason())
	}
}

type fileWritingMessageStepper struct {
	last    string
	failure error
}

func (f *fileWritingMessageStepper) Run(_ context.Context, s Step) (StepResult, error) {
	_ = os.WriteFile(filepath.Join(s.Dir, s.ReplyPath), []byte("untrusted file"), 0600)
	m := regexp.MustCompile(`km-begin ([0-9a-f]+)`).FindStringSubmatch(s.Prompt)
	if len(m) == 0 {
		return StepResult{TerminalID: "t", LastMessage: f.last}, f.failure
	}
	return StepResult{TerminalID: "t", LastMessage: strings.ReplaceAll(f.last, "NONCE", m[1])}, f.failure
}
func (*fileWritingMessageStepper) Release(context.Context, string) error { return nil }
func TestAMessageChairCannotSubstituteAFileForItsCapture(t *testing.T) {
	for _, runErr := range []error{nil, errors.New("run failed")} {
		t.Run(fmt.Sprint(runErr), func(t *testing.T) {
			dir := t.TempDir()
			f := &fileWritingMessageStepper{failure: runErr}
			e := Engine{Config: Config{Worktree: dir}, Stepper: f}
			_, err := e.attempt(context.Background(), Step{Name: "review", Dir: dir, Chair: Chair{Reply: ReplyMessage}, ReplyPath: "reply"})
			if err == nil {
				t.Fatal("accepted file")
			}
			if _, err := os.Stat(filepath.Join(dir, "reply")); !os.IsNotExist(err) {
				t.Fatalf("file remains: %v", err)
			}
		})
	}
}
func TestAnEmptyMessageReplyDoesNotRecordAScreenSource(t *testing.T) {
	dir := t.TempDir()
	f := &fileWritingMessageStepper{last: "km-begin NONCE\n\nkm-end NONCE"}
	e := Engine{Config: Config{Worktree: dir}, Stepper: f}
	_, err := e.attempt(context.Background(), Step{Name: "lessons", Dir: dir, Chair: Chair{Reply: ReplyMessage}, ReplyPath: "lessons-reply.json"})
	if err == nil || e.ReplySource() != "" {
		t.Fatalf("error %v source %q", err, e.ReplySource())
	}
}
func TestAFileChairNeverTakesTheFinalMessageAsItsReply(t *testing.T) {
	dir := t.TempDir()
	f := &messageStepper{}
	e := Engine{Config: Config{Worktree: dir}, Stepper: f}
	_, err := e.step(context.Background(), Step{Name: "review", Chair: Chair{}, ReplyPath: "reply"})
	if !errors.Is(err, errNoReply) {
		t.Fatalf("error %v", err)
	}
}

func TestTheBuiltInReviewScoreDoesNotTellMessageChairsToWriteFiles(t *testing.T) {
	score, _ := BuiltinScore("review")
	e := Engine{score: score}
	for _, name := range []string{"review", "gate"} {
		spec, _ := score.Step(name)
		p := e.brief(spec, handoff{}, "reply.md", Chair{Reply: ReplyMessage})
		if strings.Contains(p, "Write your review to") || strings.Contains(p, "Write to your final") || strings.Contains(p, "reply with one short sentence") {
			t.Fatalf("%s prompt: %s", name, p)
		}
	}
}

type blockedMessagePreflight struct{ messagePreflightStepper }

func (*blockedMessagePreflight) Run(_ context.Context, _ Step) (StepResult, error) {
	return StepResult{LastMessage: strings.Repeat("x", 500) + "\x01tail"}, nil
}
func TestAnIncompleteMessagePreflightIsBlockedWithACleanBoundedCapture(t *testing.T) {
	f := &blockedMessagePreflight{}
	results, err := Preflight(context.Background(), f, t.TempDir(), []Chair{{Provider: "claude_code", Reply: ReplyMessage}}, time.Second)
	if err != nil || len(results) != 1 || results[0].OK || !results[0].CaptureFailed || results[0].Error != "final-message reply not captured" {
		t.Fatalf("results %+v error %v", results, err)
	}
	reason := results[0].Reason()
	if len([]rune(reason)) > 300 || strings.ContainsRune(reason, '\x01') || !strings.Contains(reason, "tail") {
		t.Fatalf("reason %q", reason)
	}
}

type messageFuncStepper func(Step) (StepResult, error)

func (f messageFuncStepper) Run(_ context.Context, s Step) (StepResult, error) { return f(s) }
func (messageFuncStepper) Release(context.Context, string) error               { return nil }
func TestAMessageReviewerStillHasItsWorktreeChangesRestoredAndRetriesWithANewNonce(t *testing.T) {
	e, f, _ := newEngine(t, passing)
	defer e.cleanupSnapshot()
	if err := PrepareRunDir(f.dir, e.Config.RunID); err != nil {
		t.Fatal(err)
	}
	f.write("new", "original")
	var nonces []string
	e.Stepper = messageFuncStepper(func(s Step) (StepResult, error) {
		nonce := regexp.MustCompile(`km-begin ([0-9a-f]+)`).FindStringSubmatch(s.Prompt)[1]
		nonces = append(nonces, nonce)
		if len(nonces) == 1 {
			f.write("new", "changed")
		}
		return StepResult{TerminalID: "t", LastMessage: "km-begin " + nonce + "\nAPPROVE\nkm-end " + nonce}, nil
	})
	errs, err := e.readOnlySteps(context.Background(), []Step{{Name: "review", Chair: Chair{Reply: ReplyMessage}, ReplyPath: e.rel("review.md")}})
	if err != nil || len(errs) != 1 || errs[0] != nil || len(nonces) != 2 || nonces[0] == nonces[1] {
		t.Fatalf("errors %v %v nonces %v", err, errs, nonces)
	}
	data, err := os.ReadFile(filepath.Join(f.dir, "new"))
	if err != nil || string(data) != "original" {
		t.Fatalf("data %q error %v", data, err)
	}
}
func TestAMessageClassifierIgnoresVerdictsOutsideTheBrackets(t *testing.T) {
	e, f, _ := newEngine(t, passing)
	if err := PrepareRunDir(f.dir, e.Config.RunID); err != nil {
		t.Fatal(err)
	}
	e.Config.Chairs.Classifier.Reply = ReplyMessage
	e.Stepper = messageFuncStepper(func(s Step) (StepResult, error) {
		nonce := regexp.MustCompile(`km-begin ([0-9a-f]+)`).FindStringSubmatch(s.Prompt)[1]
		return StepResult{TerminalID: "t", LastMessage: "APPROVE\nkm-begin " + nonce + "\nunknown\nkm-end " + nonce}, nil
	})
	verdict, err := e.classify(context.Background(), 1, "review", e.rel("review.md"), "reviewer")
	if err != nil || verdict != "UNCLEAR" {
		t.Fatalf("verdict %q error %v", verdict, err)
	}
}

func TestEveryMessagePromptUsesTheFinalMessageAndKeepsMarkersInsideSentences(t *testing.T) {
	e := Engine{}
	chair := Chair{Reply: ReplyMessage}
	prompts := map[string]string{}
	for _, name := range []string{"plan", "review", "gate", "test-plan", "test"} {
		spec, _ := DefaultScore().Step(name)
		prompts[name] = e.brief(spec, handoff{}, "reply.md", chair)
	}
	e.Config.Chairs.Classifier = chair
	prompts["classify"] = e.classifyBrief("review", "review.md", "reply.md")
	prompts["lessons"] = lessonsPartPrompt("gate", "task", Result{}, nil, finalMessage, []string{"gate"})
	spec := ScoreStep{Name: "research", Part: "reader", Kind: KindProduce, Writes: "reply"}
	e.score = Score{Name: "custom", Parts: []Part{{Name: "reader", Prompt: map[string]string{"research": "Give the research in {reply}."}}}, Steps: []ScoreStep{spec}}
	prompts["custom"] = e.brief(spec, handoff{}, "reply.md", chair)
	nonce := "0123456789abcdef"
	for name, prompt := range prompts {
		t.Run(name, func(t *testing.T) {
			for _, forbidden := range []string{"reply.md", "{reply}", "reply with one short sentence", "Do not modify any file other than", "Write your reply to", "Write your review to", "Write exactly one word to", "Write only a JSON array to", "write an implementation plan to", "Write the test plan to", "Append each scenario's result", "last line of the review file", "last line of the decision file", "last line of the report"} {
				if strings.Contains(prompt, forbidden) {
					t.Errorf("prompt contains %q: %s", forbidden, prompt)
				}
			}
			if !strings.Contains(prompt, finalMessage) {
				t.Fatalf("missing final-message destination: %s", prompt)
			}
			if name == "review" || name == "gate" || name == "test" {
				if !strings.Contains(prompt, "last line before the closing km-end line") {
					t.Errorf("missing verdict placement: %s", prompt)
				}
			}
			for _, line := range strings.Split(prompt+messagePrompt(nonce), "\n") {
				if strings.TrimSpace(line) == "km-begin "+nonce || strings.TrimSpace(line) == "km-end "+nonce {
					t.Fatalf("prompt contains a standalone marker: %q", line)
				}
			}
		})
	}
}

func TestEngineRunRecordsTheScreenSourceOnlyWhenAStepCapturedAMessage(t *testing.T) {
	for _, message := range []bool{false, true} {
		t.Run(fmt.Sprint(message), func(t *testing.T) {
			e, f, _ := newEngine(t, passing)
			want := ""
			if message {
				want = ReplyScreen
				e.Config.Chairs.Reviewers[0].Reply = ReplyMessage
				e.Stepper = messageFuncStepper(func(s Step) (StepResult, error) {
					if s.Chair.Reply != ReplyMessage {
						return f.Run(context.Background(), s)
					}
					nonce := regexp.MustCompile(`km-begin ([0-9a-f]+)`).FindStringSubmatch(s.Prompt)[1]
					return StepResult{TerminalID: "message", LastMessage: "km-begin " + nonce + "\nAPPROVE\nkm-end " + nonce}, nil
				})
			}
			result := run(t, e)
			if result.Outcome != Approved || result.ReplySource != want {
				t.Fatalf("result %+v", result)
			}
		})
	}
}

type exactReadyPreflight struct {
	messagePreflightStepper
	body string
}

func (f *exactReadyPreflight) Run(_ context.Context, s Step) (StepResult, error) {
	nonce := regexp.MustCompile(`km-begin ([0-9a-f]+)`).FindStringSubmatch(s.Prompt)[1]
	return StepResult{LastMessage: "km-begin " + nonce + "\n" + f.body + "\nkm-end " + nonce}, nil
}
func TestAMessagePreflightRejectsBracketedTextOtherThanExactlyReady(t *testing.T) {
	for _, body := range []string{"I am ready", "ready!", "READY", "ready\nextra", ""} {
		t.Run(body, func(t *testing.T) {
			f := &exactReadyPreflight{body: body}
			results, err := Preflight(context.Background(), f, t.TempDir(), []Chair{{Provider: "claude_code", Reply: ReplyMessage}}, time.Second)
			if err != nil || len(results) != 1 || results[0].OK || !results[0].CaptureFailed || results[0].Error != "final-message reply not captured" {
				t.Fatalf("results %+v error %v", results, err)
			}
		})
	}
}

func TestAnEmptyBracketedStepReplyIsNudgedOnceWithAFreshNonceAndNoSource(t *testing.T) {
	dir := t.TempDir()
	var nonces []string
	var records bytes.Buffer
	e := Engine{Config: Config{Worktree: dir}, Records: &records}
	e.Stepper = messageFuncStepper(func(s Step) (StepResult, error) {
		nonce := regexp.MustCompile(`km-begin ([0-9a-f]+)`).FindStringSubmatch(s.Prompt)[1]
		nonces = append(nonces, nonce)
		if len(nonces) == 2 && !strings.Contains(s.Prompt, "without a complete bracketed final message") {
			t.Errorf("nudge %s", s.Prompt)
		}
		return StepResult{TerminalID: "t", LastMessage: "km-begin " + nonce + "\n\nkm-end " + nonce}, nil
	})
	_, err := e.step(context.Background(), Step{Name: "review", Chair: Chair{Reply: ReplyMessage}, ReplyPath: "reply"})
	var empty emptyReplyError
	if !errors.As(err, &empty) || !errors.Is(err, errNoReply) {
		t.Fatalf("error %v", err)
	}
	if len(nonces) != 2 || nonces[0] == nonces[1] {
		t.Fatalf("nonces %v", nonces)
	}
	if _, err := os.Stat(filepath.Join(dir, "reply")); !os.IsNotExist(err) {
		t.Fatalf("reply remains: %v", err)
	}
	if e.ReplySource() != "" || strings.Contains(records.String(), `"reply_source"`) {
		t.Fatalf("source %q records %s", e.ReplySource(), records.String())
	}
}

func appendMessageTranscript(t *testing.T, home string, s Step, file, prompt string, n int) {
	t.Helper()
	project := regexp.MustCompile(`[^a-zA-Z0-9]`).ReplaceAllString(s.Dir, "-")
	user, _ := json.Marshal(map[string]any{"type": "user", "message": map[string]any{"content": prompt}})
	assistant, _ := json.Marshal(map[string]any{"type": "assistant", "message": map[string]any{"id": fmt.Sprintf("%s-%d", file, n), "model": "claude-test", "usage": map[string]int{"input_tokens": n, "output_tokens": n + 1}}})
	appendUsage(t, filepath.Join(home, "projects", project, file+".jsonl"), append(append(append(user, '\n'), assistant...), '\n'))
}

func testMeteredChair(t *testing.T, message, nudge bool) {
	t.Helper()
	dir, home := t.TempDir(), t.TempDir()
	var records bytes.Buffer
	calls := 0
	e := Engine{Config: Config{Worktree: dir, ReplyGrace: time.Millisecond}, Meter: &LogMeter{ClaudeHome: home}, Records: &records}
	e.Stepper = messageFuncStepper(func(s Step) (StepResult, error) {
		calls++
		prompt := s.Prompt
		if !message {
			prompt = s.ReplyPath
		}
		appendMessageTranscript(t, home, s, "chair", prompt, calls*10)
		if !message {
			if err := os.WriteFile(filepath.Join(dir, s.ReplyPath), []byte("ok"), 0600); err != nil {
				t.Fatal(err)
			}
			return StepResult{}, nil
		}
		nonce := regexp.MustCompile(`km-begin ([0-9a-f]+)`).FindStringSubmatch(s.Prompt)[1]
		reply := "km-begin " + nonce + "\nok\n"
		if !nudge || calls > 1 {
			reply += "km-end " + nonce
		}
		return StepResult{TerminalID: "t", LastMessage: reply}, nil
	})
	chair := Chair{Provider: "claude_code"}
	if message {
		chair.Reply = ReplyMessage
	}
	_, err := e.step(context.Background(), Step{Name: "review", Chair: chair, ReplyPath: "unique-reply-path-9382"})
	if err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(&records)
	var got []Record
	for dec.More() {
		var rec Record
		if err := dec.Decode(&rec); err != nil {
			t.Fatal(err)
		}
		if rec.Usage != nil {
			got = append(got, rec)
		}
	}
	want := 1
	if nudge {
		want = 2
	}
	if len(got) != want {
		t.Fatalf("records %+v", got)
	}
	for i, rec := range got {
		if rec.Step != []string{"review", "review-nudge"}[i] {
			t.Fatalf("step %s", rec.Step)
		}
		requireTokens(t, *rec.Usage, Tokens{InputTokens: int64((i + 1) * 10), OutputTokens: int64((i+1)*10 + 1)})
	}
}
func TestAMessageChairsStepRecordsKnownUsage(t *testing.T) { testMeteredChair(t, true, false) }
func TestAMessageChairsNudgeAttemptRecordsKnownUsageByItsOwnNonce(t *testing.T) {
	testMeteredChair(t, true, true)
}
func TestAFileChairsStepIsStillMeteredByItsReplyPath(t *testing.T) { testMeteredChair(t, false, false) }

func TestConcurrentMessageReviewersEachMatchOnlyTheirOwnTranscript(t *testing.T) {
	dir, home := t.TempDir(), t.TempDir()
	meter := &LogMeter{ClaudeHome: home}
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	written := make(chan struct{}, 2)
	finish := make(chan struct{})
	e := Engine{Config: Config{Worktree: dir}, Meter: meter}
	e.Stepper = messageFuncStepper(func(s Step) (StepResult, error) {
		entered <- struct{}{}
		<-release
		n := 10
		if s.ChairName == "two" {
			n = 20
		}
		appendMessageTranscript(t, home, s, s.ChairName, s.Prompt, n)
		if s.ChairName == "one" {
			appendMessageTranscript(t, home, s, "other", "reply-one reply-two", 99)
		}
		written <- struct{}{}
		<-finish
		nonce := regexp.MustCompile(`km-begin ([0-9a-f]+)`).FindStringSubmatch(s.Prompt)[1]
		return StepResult{LastMessage: "km-begin " + nonce + "\nok\nkm-end " + nonce}, nil
	})
	var wg sync.WaitGroup
	for _, name := range []string{"one", "two"} {
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			out, err := e.step(context.Background(), Step{Name: "review", ChairName: name, Chair: Chair{Provider: "claude_code", Reply: ReplyMessage, Address: name}, ReplyPath: "reply-" + name})
			if err != nil {
				t.Error(err)
				return
			}
			n := int64(10)
			if name == "two" {
				n = 20
			}
			if out.usage == nil || !out.usage.Known || out.usage.Tokens == nil || *out.usage.Tokens != (Tokens{InputTokens: n, OutputTokens: n + 1}) {
				t.Errorf("%s usage %+v", name, out.usage)
			}
		}(name)
	}
	<-entered
	<-entered
	close(release)
	<-written
	<-written
	close(finish)
	wg.Wait()
}
