package loop

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type fakeStepper struct {
	t        *testing.T
	dir      string
	verdicts map[string][]string
	actions  map[string]func(Step)
	errFor   func(Step) error
	screens  map[string]string
	steps    []Step
	released []string
	next     int
}

func (f *fakeStepper) Run(_ context.Context, s Step) (StepResult, error) {
	f.steps = append(f.steps, s)
	if act, ok := f.actions[s.Name]; ok {
		act(s)
	} else if strings.HasPrefix(s.Name, "classify-") {
		f.write(s.ReplyPath, f.pop(strings.TrimPrefix(s.Name, "classify-")))
	} else {
		f.write(s.ReplyPath, s.Name+" reply\n")
	}
	id := s.ReuseTerminal
	if id == "" {
		f.next++
		id = fmt.Sprintf("t%d", f.next)
	}
	if f.errFor != nil {
		if err := f.errFor(s); err != nil {
			return StepResult{TerminalID: id}, err
		}
	}
	return StepResult{TerminalID: id, Status: "completed"}, nil
}

func (f *fakeStepper) Screen(_ context.Context, id string) (string, string, error) {
	return "cao-" + id, f.screens[id], nil
}

func (f *fakeStepper) Release(_ context.Context, id string) error {
	f.released = append(f.released, id)
	return nil
}

func (f *fakeStepper) pop(subject string) string {
	q := f.verdicts[subject]
	if len(q) == 0 {
		return "APPROVE"
	}
	f.verdicts[subject] = q[1:]
	return q[0]
}

func (f *fakeStepper) write(rel, content string) {
	f.t.Helper()
	if err := os.WriteFile(filepath.Join(f.dir, rel), []byte(content), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fakeStepper) names() []string {
	var out []string
	for _, s := range f.steps {
		out = append(out, label(s.Name, s.Round))
	}
	return out
}

func newRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"-c", "user.name=t", "-c", "user.email=t@example.invalid", "commit", "-q", "--allow-empty", "-m", "base"},
	} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	return dir
}

func newEngine(t *testing.T, verify Verifier) (*Engine, *fakeStepper, *bytes.Buffer) {
	t.Helper()
	dir := newRepo(t)
	fake := &fakeStepper{t: t, dir: dir, verdicts: map[string][]string{}, actions: map[string]func(Step){}}
	var records bytes.Buffer
	seat := Seat{Provider: "p", Profile: "x"}
	eng := &Engine{
		Config: Config{
			RunID: "r1", Task: "do the thing", Worktree: dir, MaxRounds: 3,
			Seats:         Seats{Planner: seat, Implementer: seat, Reviewer: seat, Gate: seat, Classifier: seat},
			VerifyCommand: "check",
		},
		Stepper: fake,
		Verify:  verify,
		Records: &records,
	}
	return eng, fake, &records
}

func passing(context.Context, string) (VerifyResult, error) {
	return VerifyResult{ExitCode: 0, Output: "ok\n"}, nil
}

func run(t *testing.T, eng *Engine) Result {
	t.Helper()
	res, err := eng.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func equal(t *testing.T, got, want []string) {
	t.Helper()
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("steps\n got: %v\nwant: %v", got, want)
	}
}

func TestApprovesInOneRound(t *testing.T) {
	eng, fake, _ := newEngine(t, passing)
	res := run(t, eng)
	if res.Outcome != Approved || res.Rounds != 1 {
		t.Fatalf("got %+v", res)
	}
	equal(t, fake.names(), []string{"plan", "implement[1]", "review[1]", "classify-review[1]", "gate[1]", "classify-gate[1]"})
	if len(fake.released) != 6 || fake.released[len(fake.released)-1] != "t2" {
		t.Fatalf("released %v, want every terminal with the implementer's t2 last", fake.released)
	}
}

func TestReviewChangesLoopBackToTheSameImplementerWithFeedback(t *testing.T) {
	eng, fake, _ := newEngine(t, passing)
	fake.verdicts["review"] = []string{"CHANGES"}
	res := run(t, eng)
	if res.Outcome != Approved || res.Rounds != 2 {
		t.Fatalf("got %+v", res)
	}
	equal(t, fake.names(), []string{
		"plan", "implement[1]", "review[1]", "classify-review[1]",
		"implement[2]", "review[2]", "classify-review[2]", "gate[2]", "classify-gate[2]",
	})
	second := fake.steps[4]
	if second.ReuseTerminal != "t2" {
		t.Fatalf("round 2 implementer reused %q, want t2", second.ReuseTerminal)
	}
	if !strings.Contains(second.Prompt, RunDirRel("r1")+"/review-1.md") {
		t.Fatalf("round 2 prompt lacks review feedback:\n%s", second.Prompt)
	}
}

func TestUnclearVerdictCountsAsChanges(t *testing.T) {
	eng, fake, _ := newEngine(t, passing)
	fake.verdicts["gate"] = []string{"UNCLEAR", "UNCLEAR", "UNCLEAR"}
	res := run(t, eng)
	if res.Outcome != RoundsExhausted || res.Rounds != 3 {
		t.Fatalf("got %+v", res)
	}
}

func TestFailingVerifySkipsTheGate(t *testing.T) {
	calls := 0
	verify := func(context.Context, string) (VerifyResult, error) {
		calls++
		if calls == 2 {
			return VerifyResult{ExitCode: 1, Output: "FAIL\n"}, nil
		}
		return VerifyResult{Output: "ok\n"}, nil
	}
	eng, fake, _ := newEngine(t, verify)
	res := run(t, eng)
	if res.Outcome != Approved || res.Rounds != 2 {
		t.Fatalf("got %+v", res)
	}
	equal(t, fake.names()[:5], []string{"plan", "implement[1]", "review[1]", "classify-review[1]", "implement[2]"})
	out, _ := os.ReadFile(filepath.Join(eng.Config.Worktree, RunDirRel("r1"), "verify-1.txt"))
	if string(out) != "FAIL\n" {
		t.Fatalf("verify-1.txt = %q", out)
	}
}

func TestReviewerThatEditsTheWorktreeFailsTheRun(t *testing.T) {
	eng, fake, _ := newEngine(t, passing)
	fake.actions["review"] = func(s Step) {
		fake.write(s.ReplyPath, "looks fine\n")
		fake.write("main.go", "package main\n")
	}
	res := run(t, eng)
	if res.Outcome != Failed || !strings.Contains(res.Reason, "modified the worktree") {
		t.Fatalf("got %+v", res)
	}
}

func TestImplementerChangesAreVisibleAndReplyFilesAreIgnored(t *testing.T) {
	eng, fake, _ := newEngine(t, passing)
	fake.actions["implement"] = func(s Step) {
		fake.write(s.ReplyPath, "added new.go\n")
		fake.write("new.go", "package x\n")
	}
	run(t, eng)
	diff, err := eng.gitOutput("diff", "HEAD", "--name-only")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(diff) != "new.go" {
		t.Fatalf("git diff HEAD --name-only = %q, want only new.go", diff)
	}
}

func TestMissingReplyFileFailsEvenWhenCAOReportsCompleted(t *testing.T) {
	eng, fake, records := newEngine(t, passing)
	fake.actions["plan"] = func(Step) {}
	fake.actions["plan-nudge"] = func(Step) {}
	fake.screens = map[string]string{"t1": "agent stopped here"}
	res := run(t, eng)
	if res.Outcome != Failed || !strings.Contains(res.Reason, "no reply file") {
		t.Fatalf("got %+v", res)
	}
	var first Record
	if err := json.Unmarshal(bytes.SplitN(records.Bytes(), []byte("\n"), 3)[1], &first); err != nil {
		t.Fatal(err)
	}
	if first.Step != "plan" || first.Status != "completed" || first.Error == "" || first.Screen != "agent stopped here" {
		t.Fatalf("record = %+v", first)
	}
	equal(t, fake.released, []string{"t1"})
}

func TestMissingReplyIsNudgedOnceInTheSameTerminal(t *testing.T) {
	eng, fake, _ := newEngine(t, passing)
	fake.actions["implement"] = func(Step) {}
	res := run(t, eng)
	if res.Outcome != Approved {
		t.Fatalf("got %+v", res)
	}
	nudge := fake.steps[2]
	if nudge.Name != "implement-nudge" || nudge.ReuseTerminal != "t2" || !strings.Contains(nudge.Prompt, "implement-1.md") {
		t.Fatalf("nudge step = %+v", nudge)
	}
}

func TestLateReplyFromBackgroundWorkIsAwaitedBeforeNudging(t *testing.T) {
	eng, fake, records := newEngine(t, passing)
	eng.Config.ReplyGrace = 2 * time.Second
	fake.actions["implement"] = func(s Step) {
		go func() {
			time.Sleep(100 * time.Millisecond)
			fake.write(s.ReplyPath, "late summary\n")
		}()
	}
	res := run(t, eng)
	if res.Outcome != Approved {
		t.Fatalf("got %+v", res)
	}
	for _, name := range fake.names() {
		if strings.Contains(name, "nudge") {
			t.Fatalf("nudged although the reply arrived in the grace window: %v", fake.names())
		}
	}
	if !bytes.Contains(records.Bytes(), []byte(`"reply_wait_ms"`)) {
		t.Fatalf("no reply wait recorded: %s", records.String())
	}
}

func TestPreflightPassesReadySeatsAndReportsBlockedOnes(t *testing.T) {
	dir := newRepo(t)
	fake := &fakeStepper{t: t, dir: dir, verdicts: map[string][]string{}, actions: map[string]func(Step){}}
	fake.actions["preflight"] = func(s Step) {
		if s.Seat.Provider == "ready" {
			fake.write(s.ReplyPath, "ready\n")
		}
	}
	fake.errFor = func(s Step) error {
		if s.Seat.Provider == "blocked" {
			return errors.New("HTTP 504: did not complete")
		}
		return nil
	}
	fake.screens = map[string]string{}
	seats := UniqueSeats(Seat{Provider: "ready", Profile: "a"}, Seat{Provider: "blocked", Profile: "b"}, Seat{Provider: "ready", Profile: "a"})
	results, err := Preflight(context.Background(), &screenByProvider{fake, "  SessionStart hooks\n  1 hook needs review before it can run.\n"}, dir, seats, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 || !results[0].OK || results[1].OK {
		t.Fatalf("results = %+v", results)
	}
	if results[1].Blocker != "1 hook needs review before it can run." || results[1].Session == "" {
		t.Fatalf("blocked result = %+v", results[1])
	}
	for _, id := range fake.released {
		if id == results[1].TerminalID {
			t.Fatalf("released the blocked terminal %s; it must stay up for the user to attach", id)
		}
	}
	if status, _ := exec.Command("git", "-C", dir, "status", "--porcelain").Output(); len(status) != 0 {
		t.Fatalf("preflight left tracked changes: %s", status)
	}
}

type screenByProvider struct {
	*fakeStepper
	blocked string
}

func (s *screenByProvider) Screen(_ context.Context, id string) (string, string, error) {
	return "cao-" + id, s.blocked, nil
}

func TestEveryStepIsRecorded(t *testing.T) {
	eng, _, records := newEngine(t, passing)
	run(t, eng)
	var steps []string
	sc := bufio.NewScanner(records)
	for sc.Scan() {
		var rec Record
		if err := json.Unmarshal(sc.Bytes(), &rec); err != nil {
			t.Fatal(err)
		}
		if rec.RunID != "r1" {
			t.Fatalf("record without run id: %s", sc.Text())
		}
		steps = append(steps, label(rec.Step, rec.Round))
	}
	equal(t, steps, []string{
		"verify-baseline", "plan", "implement[1]", "verify[1]", "review[1]", "classify-review[1]", "verdict-review[1]",
		"gate[1]", "classify-gate[1]", "verdict-gate[1]",
	})
}

func TestParseSeat(t *testing.T) {
	s, err := ParseSeat("codex:km-codex:gpt-6-luna")
	if err != nil || s != (Seat{Provider: "codex", Profile: "km-codex", Model: "gpt-6-luna"}) {
		t.Fatalf("got %+v, %v", s, err)
	}
	if _, err := ParseSeat("codex"); err == nil {
		t.Fatal("want error for a seat without a profile")
	}
}

func TestFailingBaselineStopsBeforePlanning(t *testing.T) {
	verify := func(context.Context, string) (VerifyResult, error) {
		return VerifyResult{ExitCode: 1, Output: "FAIL on base\n"}, nil
	}
	eng, fake, _ := newEngine(t, verify)
	res := run(t, eng)
	if res.Outcome != Failed || !strings.Contains(res.Reason, "before any change") {
		t.Fatalf("got %+v", res)
	}
	if len(fake.steps) != 0 {
		t.Fatalf("ran agent steps after a failing baseline: %v", fake.names())
	}
}

func TestRetryingVerifierPassesAFlakeAndKeepsBothOutputs(t *testing.T) {
	calls := 0
	flaky := func(context.Context, string) (VerifyResult, error) {
		calls++
		if calls == 1 {
			return VerifyResult{ExitCode: 1, Output: "timing FAIL\n"}, nil
		}
		return VerifyResult{Output: "ok\n"}, nil
	}
	r, err := Retrying(flaky, 1)(context.Background(), "")
	if err != nil || r.ExitCode != 0 || r.Attempts != 2 {
		t.Fatalf("got %+v, %v", r, err)
	}
	if !strings.Contains(r.Output, "timing FAIL") || !strings.Contains(r.Output, "attempt 2") {
		t.Fatalf("output lost the failed attempt: %q", r.Output)
	}
}

func TestRetryingVerifierStopsAfterItsRetries(t *testing.T) {
	calls := 0
	failing := func(context.Context, string) (VerifyResult, error) {
		calls++
		return VerifyResult{ExitCode: 1}, nil
	}
	r, _ := Retrying(failing, 1)(context.Background(), "")
	if r.ExitCode != 1 || calls != 2 || r.Attempts != 2 {
		t.Fatalf("got %+v after %d calls", r, calls)
	}
}

func TestRepoHooksSupplyVerifyAndCheckUnlessVerifyIsGiven(t *testing.T) {
	dir := newRepo(t)
	if v, c, _ := ResolveHooks(dir, ""); v != "" || c != "" {
		t.Fatalf("no hooks: got %q %q", v, c)
	}
	if err := os.MkdirAll(filepath.Join(dir, ".kapellmeister"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, hook := range []string{VerifyHook, CheckHook} {
		if err := os.WriteFile(filepath.Join(dir, hook), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if v, c, _ := ResolveHooks(dir, ""); v != "./.kapellmeister/verify" || c != "./.kapellmeister/check" {
		t.Fatalf("hooks: got %q %q", v, c)
	}
	if v, _, _ := ResolveHooks(dir, "make test"); v != "make test" {
		t.Fatalf("flag should win: got %q", v)
	}
	if err := os.Chmod(filepath.Join(dir, CheckHook), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, c, _ := ResolveHooks(dir, ""); c != "" {
		t.Fatalf("non-executable check hook used: %q", c)
	}
}

func TestImplementerIsPointedAtTheCheckHookAndAwayFromTheFullSuite(t *testing.T) {
	eng, fake, _ := newEngine(t, passing)
	eng.Config.CheckCommand = "./.kapellmeister/check"
	run(t, eng)
	for _, s := range fake.steps {
		switch s.Name {
		case "implement":
			if !strings.Contains(s.Prompt, "./.kapellmeister/check <tests or packages>") || !strings.Contains(s.Prompt, "Do not run the full suite") {
				t.Fatalf("implement prompt: %s", s.Prompt)
			}
		case "review", "gate":
			if !strings.Contains(s.Prompt, "Do not re-run the test suite") {
				t.Fatalf("%s prompt: %s", s.Name, s.Prompt)
			}
		}
	}
}

func TestRepoConfigCommandsWinOverHookScripts(t *testing.T) {
	dir := newRepo(t)
	if err := os.MkdirAll(filepath.Join(dir, ".kapellmeister"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, VerifyHook), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	config := "[test]\nverify = \"scripts/verify\"\ncheck = \"scripts/check {args} -short\"\n"
	if err := os.WriteFile(filepath.Join(dir, ConfigFile), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	v, c, err := ResolveHooks(dir, "")
	if err != nil || v != "scripts/verify" || c != "scripts/check {args} -short" {
		t.Fatalf("got %q %q %v", v, c, err)
	}
	if got := CheckUsage(c); got != "scripts/check <tests or packages> -short" {
		t.Fatalf("CheckUsage = %q", got)
	}
	if got := CheckUsage("scripts/check"); got != "scripts/check <tests or packages>" {
		t.Fatalf("CheckUsage without token = %q", got)
	}
}

func TestRepoConfigWithUnknownKeysIsRejected(t *testing.T) {
	dir := newRepo(t)
	if err := os.MkdirAll(filepath.Join(dir, ".kapellmeister"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ConfigFile), []byte("[test]\nverfy = \"x\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ResolveHooks(dir, ""); err == nil || !strings.Contains(err.Error(), "verfy") {
		t.Fatalf("err = %v", err)
	}
}

func shortPreflightGrace(t *testing.T) {
	saved := preflightReplyGrace
	preflightReplyGrace = 100 * time.Millisecond
	t.Cleanup(func() { preflightReplyGrace = saved })
}

func TestPreflightRetriesASeatOnceWhenNothingNeedsTheUser(t *testing.T) {
	shortPreflightGrace(t)
	dir := newRepo(t)
	fake := &fakeStepper{t: t, dir: dir, verdicts: map[string][]string{}, actions: map[string]func(Step){}}
	calls := 0
	fake.actions["preflight"] = func(s Step) {
		calls++
		if calls > 1 {
			fake.write(s.ReplyPath, "ready\n")
		}
	}
	fake.screens = map[string]string{"t1": "policy is still loading"}
	results, err := Preflight(context.Background(), fake, dir, []Seat{{Provider: "slow", Profile: "x"}}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if !results[0].OK || !results[0].Retried || calls != 2 {
		t.Fatalf("results = %+v, calls = %d", results, calls)
	}
	equal(t, fake.released, []string{"t1", "t2"})
}

func TestPreflightWaitsBrieflyForAReplyWrittenAfterCAOReportsCompleted(t *testing.T) {
	dir := newRepo(t)
	fake := &fakeStepper{t: t, dir: dir, verdicts: map[string][]string{}, actions: map[string]func(Step){}}
	fake.actions["preflight"] = func(s Step) {
		go func() {
			time.Sleep(1500 * time.Millisecond)
			_ = os.WriteFile(filepath.Join(dir, s.ReplyPath), []byte("ready\n"), 0o644)
		}()
	}
	results, err := Preflight(context.Background(), fake, dir, []Seat{{Provider: "late", Profile: "x"}}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if !results[0].OK || results[0].Retried {
		t.Fatalf("results = %+v", results)
	}
}

func TestBlockersAreReadOnlyFromTheBottomOfTheScreen(t *testing.T) {
	skills := "  - **cao-workflow**: running it is a separate, user-approved step.\n" + strings.Repeat("  more startup text\n", blockerLines) + "> ready\n"
	if got := findBlocker(skills); got != "" {
		t.Fatalf("text scrolled above the prompt was read as a blocker: %q", got)
	}
	prompt := strings.Repeat("startup\n", 30) + "Do you trust the files in this folder?\n\n  1. Yes\n  2. No\n"
	if got := findBlocker(prompt); got != "Do you trust the files in this folder?" {
		t.Fatalf("blocker = %q", got)
	}
}

func TestTerminalsAreLabelledWithRunAndRole(t *testing.T) {
	eng, fake, _ := newEngine(t, passing)
	run(t, eng)
	got := map[string]string{}
	for _, s := range fake.steps {
		got[s.Name] = s.Label
	}
	want := map[string]string{
		"plan":            "km r1 · planner",
		"implement":       "km r1 · implementer",
		"review":          "km r1 · reviewer",
		"gate":            "km r1 · gate",
		"classify-review": "km r1 · classifier",
	}
	for name, label := range want {
		if got[name] != label {
			t.Fatalf("%s label = %q, want %q", name, got[name], label)
		}
	}
}
