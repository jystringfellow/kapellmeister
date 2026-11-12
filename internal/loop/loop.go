package loop

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

type Chair struct {
	Address  string        `json:"address,omitempty"`
	Provider string        `json:"provider"`
	Profile  string        `json:"profile"`
	Model    string        `json:"model,omitempty"`
	Tier     string        `json:"tier,omitempty"`
	Timeout  time.Duration `json:"timeout,omitempty"`
}

func SplitTier(spec string) (string, string, error) {
	i := strings.LastIndex(spec, "@")
	if i < 0 {
		return spec, "", nil
	}
	base, tier := spec[:i], spec[i+1:]
	if tierIndex(tier) < 0 {
		return "", "", fmt.Errorf("chair %q: unknown tier %q (want %s)", spec, tier, strings.Join(TierNames, ", "))
	}
	if parts := strings.SplitN(base, ":", 3); len(parts) == 3 && parts[2] != "" {
		return "", "", fmt.Errorf("chair %q: an explicit model is never re-tiered; drop @%s or the model", spec, tier)
	}
	return base, tier, nil
}

func ParseChair(spec string) (Chair, error) {
	base, tier, err := SplitTier(spec)
	if err != nil {
		return Chair{}, err
	}
	parts := strings.SplitN(base, ":", 3)
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return Chair{}, fmt.Errorf("chair %q: want provider:profile[:model]", spec)
	}
	s := Chair{Provider: parts[0], Profile: parts[1], Tier: tier}
	if len(parts) == 3 {
		s.Model = parts[2]
	}
	return s, nil
}

func (s Chair) String() string {
	if s.Model == "" {
		return s.Provider + ":" + s.Profile
	}
	return s.Provider + ":" + s.Profile + ":" + s.Model
}

type Chairs struct {
	Tester      *Chair  `json:"tester,omitempty"`
	Planner     Chair   `json:"planner"`
	Implementer Chair   `json:"implementer"`
	Reviewers   []Chair `json:"reviewers"`
	Gate        Chair   `json:"gate"`
	Classifier  Chair   `json:"classifier"`
}

type Step struct {
	ChairName     string
	Name          string
	Round         int
	Chair         Chair
	Prompt        string
	Dir           string
	ReplyPath     string
	KeepTerminal  bool
	ReuseTerminal string
	FreshTerminal bool
	Timeout       time.Duration
	Label         string
	Assignment    Assignment
}

type StepResult struct {
	TerminalID    string
	Status        string
	LastMessage   string
	ReportedModel string
	ModelHonored  *bool
}

type Stepper interface {
	Run(ctx context.Context, step Step) (StepResult, error)
	Release(ctx context.Context, terminalID string) error
}

type Seat struct {
	TerminalID string
	Session    string
	Model      string
	Honored    *bool
}

type Seater interface {
	Seat(ctx context.Context, name string, chair Chair, dir string) (Seat, error)
	Unseat(ctx context.Context, session string) error
}

type Screener interface {
	Screen(ctx context.Context, terminalID string) (session, text string, err error)
}

type VerifyResult struct {
	ExitCode int
	Output   string
	Attempts int
}

type Verifier func(ctx context.Context, dir string) (VerifyResult, error)

type Config struct {
	Lessons        bool
	LessonsTimeout time.Duration
	LearnedBudget  int
	CAOURL         string
	StateDir       string
	ScratchDir     string
	BlockerPoll    time.Duration
	GateWhen       string
	RunID          string
	Task           string
	Worktree       string
	Chairs         Chairs
	MaxRounds      int
	StepTimeout    time.Duration
	VerifyCommand  string
	CheckCommand   string
	ReplyGrace     time.Duration
	SkipBaseline   bool
	Policy         *Policy
}

type Record struct {
	Usage         *Usage         `json:"usage,omitempty"`
	Lessons       *LessonsRecord `json:"lessons,omitempty"`
	PID           int            `json:"pid,omitempty"`
	Command       string         `json:"command,omitempty"`
	BlockedStep   string         `json:"blocked_step,omitempty"`
	ChairName     string         `json:"chair_name,omitempty"`
	At            time.Time      `json:"at"`
	RunID         string         `json:"run_id"`
	Round         int            `json:"round,omitempty"`
	Step          string         `json:"step"`
	Chair         *Chair         `json:"chair,omitempty"`
	Tier          string         `json:"tier,omitempty"`
	Effort        string         `json:"effort,omitempty"`
	TierSource    string         `json:"tier_source,omitempty"`
	Decision      *SizeDecision  `json:"decision,omitempty"`
	Detail        string         `json:"detail,omitempty"`
	TerminalID    string         `json:"terminal_id,omitempty"`
	Status        string         `json:"status,omitempty"`
	Model         string         `json:"model,omitempty"`
	ModelHonored  *bool          `json:"model_honored,omitempty"`
	DurationMS    int64          `json:"duration_ms"`
	QueueMS       int64          `json:"queue_ms,omitempty"`
	Reply         string         `json:"reply,omitempty"`
	ReplyChars    int            `json:"reply_chars,omitempty"`
	ReplyWaitMS   int64          `json:"reply_wait_ms,omitempty"`
	Verdict       string         `json:"verdict,omitempty"`
	VerdictSource string         `json:"verdict_source,omitempty"`
	ExitCode      *int           `json:"exit_code,omitempty"`
	Attempts      int            `json:"attempts,omitempty"`
	TreeChanged   bool           `json:"tree_changed,omitempty"`
	LastMessage   string         `json:"last_message,omitempty"`
	Screen        string         `json:"screen,omitempty"`
	Error         string         `json:"error,omitempty"`
}

const (
	Approved        = "approved"
	Untested        = "untested"
	RoundsExhausted = "rounds_exhausted"
	Failed          = "failed"
	VerdictFailed   = "FAILED"
)

type Result struct {
	failure  error
	Usage    *Usage         `json:"usage,omitempty"`
	Lessons  *LessonsReport `json:"lessons,omitempty"`
	Proposed []Lesson       `json:"-"`
	RunID    string         `json:"run_id"`
	Outcome  string         `json:"outcome"`
	Rounds   int            `json:"rounds"`
	Reason   string         `json:"reason,omitempty"`
	Worktree string         `json:"worktree"`
	RunDir   string         `json:"run_dir"`
}

type ActiveStep struct {
	Step      string    `json:"step"`
	ChairName string    `json:"chair_name,omitempty"`
	Chair     string    `json:"chair"`
	Started   time.Time `json:"started"`
}

type Engine struct {
	Meter     UsageMeter
	usage     Usage
	gateRound int
	Watcher   Watcher
	watch     watchState
	Config    Config
	Stepper   Stepper
	Verify    Verifier
	Records   io.Writer
	Progress  io.Writer
	Playing   func(round int, active []ActiveStep)

	lessons map[lessonKey]chairLessons
	active  []*ActiveStep
	round   int
	mu      sync.Mutex
	size    string
	bump    int
}

var verdictRE = regexp.MustCompile(`\b(APPROVE|CHANGES|UNCLEAR)\b`)

func RunDirRel(runID string) string {
	return filepath.Join(".kapellmeister", "runs", runID)
}

func (e *Engine) Run(ctx context.Context) (res Result, err error) {
	e.mu.Lock()
	e.usage = Usage{}
	e.mu.Unlock()
	defer func() {
		if e.Meter != nil {
			e.mu.Lock()
			total := e.usage
			e.mu.Unlock()
			res.Usage = &total
		}
	}()
	stopWatcher := e.startWatcher(ctx)
	defer stopWatcher()
	e.gateRound = 0
	res, err = e.play(ctx)
	if res.Outcome == Failed {
		res.Reason = cancellationReason(ctx, res.failure)
	}
	if cause := SignalOf(ctx); cause != nil {
		e.record(Record{RunID: e.Config.RunID, Step: "interrupted", Error: cause.Error()})
	}
	if e.Config.Lessons {
		proposed, report := e.proposeLessons(ctx, res)
		res.Proposed, res.Lessons = proposed, &report
	}
	return res, err
}

func (e *Engine) play(ctx context.Context) (Result, error) {
	cfg := e.Config
	res := Result{RunID: cfg.RunID, Worktree: cfg.Worktree, RunDir: filepath.Join(cfg.Worktree, RunDirRel(cfg.RunID))}
	if err := PrepareRunDir(cfg.Worktree, cfg.RunID); err != nil {
		return res, err
	}

	e.loadLessons()

	if cfg.VerifyCommand != "" && e.Verify != nil && !cfg.SkipBaseline {
		baselinePath := e.rel("verify-0.txt")
		baseline, err := e.runVerify(ctx, 0, baselinePath)
		if err != nil {
			return e.fail(res, err), nil
		}
		if baseline.ExitCode != 0 {
			return e.fail(res, fmt.Errorf("verify already fails before any change (exit %d); see %s", baseline.ExitCode, baselinePath)), nil
		}
	}

	plan := e.rel("plan.md")
	askSize := e.sizing().Mode != SizingOff && e.sizing().Decider == DeciderPlanner
	if _, err := e.step(ctx, Step{Name: "plan", Chair: cfg.Chairs.Planner, Prompt: planPrompt(cfg.Task, plan, askSize, e.lessonsFor("plan", cfg.Chairs.Planner)), ReplyPath: plan}); err != nil {
		return e.fail(res, err), nil
	}
	e.decideSize(ctx, plan)

	var testPlanDone chan error
	if cfg.Chairs.Tester != nil {
		testCtx, cancel := context.WithCancel(ctx)
		testPlanDone = make(chan error, 1)
		go func() {
			path := e.rel("test-plan.md")
			_, err := e.step(testCtx, Step{Name: "test-plan", Chair: *cfg.Chairs.Tester,
				Prompt: testPlanPrompt(cfg.Task, plan, path, cfg.ScratchDir, e.testerTimeout(), os.Getpid(), e.lessonsFor("test-plan", *cfg.Chairs.Tester), cfg.CAOURL, cfg.StateDir), ReplyPath: path})
			testPlanDone <- err
		}()
		defer func() {
			cancel()
			if testPlanDone != nil {
				<-testPlanDone
			}
		}()
	}
	joinTestPlan := func() error {
		if testPlanDone == nil {
			return nil
		}
		err := <-testPlanDone
		testPlanDone = nil
		return err
	}

	implementer := ""
	var implementerChair Chair
	misses := 0
	defer func() {
		if implementer != "" {
			_ = e.Stepper.Release(context.WithoutCancel(ctx), implementer)
		}
	}()

	var feedback []string
	var feedbackGate, feedbackTest string
	for round := 1; round <= cfg.MaxRounds; round++ {
		res.Rounds = round
		summary := e.relf("implement-%d.md", round)
		if chair, _ := e.resolve("implement", cfg.Chairs.Implementer); implementer != "" && chair != implementerChair {
			_ = e.Stepper.Release(context.WithoutCancel(ctx), implementer)
			implementer = ""
		}
		implementerChair, _ = e.resolve("implement", cfg.Chairs.Implementer)
		out, err := e.step(ctx, Step{
			Name: "implement", Round: round, Chair: cfg.Chairs.Implementer,
			Prompt:    implementPrompt(cfg.Task, plan, summary, cfg.CheckCommand, feedback, feedbackGate, feedbackTest, e.lessonsFor("implement", cfg.Chairs.Implementer)),
			ReplyPath: summary, KeepTerminal: true, ReuseTerminal: implementer,
		})
		if out.TerminalID != "" {
			implementer = out.TerminalID
		}
		if err != nil {
			return e.fail(res, err), nil
		}
		if err := e.git("add", "--intent-to-add", "--all"); err != nil {
			return e.fail(res, err), nil
		}

		verifyPath := e.relf("verify-%d.txt", round)
		verify, err := e.runVerify(ctx, round, verifyPath)
		if err != nil {
			return e.fail(res, err), nil
		}

		reviews, err := e.reviewRound(ctx, round, plan, summary, verify, verifyPath)
		if err != nil {
			return e.fail(res, err), nil
		}
		var files, failed, verdicts []string
		for _, review := range reviews {
			if _, err := os.Stat(filepath.Join(cfg.Worktree, review.Path)); err == nil {
				files = append(files, review.Path)
			}
			if review.Err != nil {
				failed = append(failed, fmt.Sprintf("%s (%s)", review.Name, review.Chair))
			}
			verdicts = append(verdicts, review.Verdict)
		}
		feedback = append(append([]string{}, files...), verifyPath)
		feedbackGate, feedbackTest = "", ""
		if !gateRuns(cfg.GateWhen, verify.ExitCode == 0, verdicts) {
			misses++
			e.escalate(round, misses)
			continue
		}

		gate := e.relf("gate-%d.md", round)
		if errs, err := e.readOnlySteps(ctx, []Step{{
			Name: "gate", Round: round, Chair: cfg.Chairs.Gate,
			Prompt:    gatePrompt(cfg.Task, plan, summary, files, failed, cfg.VerifyCommand, verify, verifyPath, gate, e.lessonsFor("gate", cfg.Chairs.Gate)),
			ReplyPath: gate,
		}}); err != nil {
			return e.fail(res, err), nil
		} else if errs[0] != nil {
			return e.fail(res, errs[0]), nil
		}
		gateVerdict, err := e.verdict(ctx, round, "gate", gate, "")
		if err != nil {
			return e.fail(res, err), nil
		}
		e.gateRound = round
		if gateVerdict == "APPROVE" {
			if cfg.Chairs.Tester == nil {
				res.Outcome = Approved
				return res, nil
			}
			if err := joinTestPlan(); err != nil {
				return e.fail(res, err), nil
			}
			testPath := e.relf("test-%d.md", round)
			verdict, err := e.testRound(ctx, round, plan, summary, verify, verifyPath, testPath)
			if err != nil {
				var interrupted *testInterruptedError
				if errors.As(err, &interrupted) {
					res.Outcome = Untested
					res.Reason = "change approved by the gate but the tester did not finish: " + interrupted.Signal.Error() + "; the gate's approval stands unverified"
					if info, err := os.Stat(filepath.Join(cfg.Worktree, interrupted.Path)); err == nil && info.Mode().IsRegular() {
						res.Reason += "; partial report: " + interrupted.Path
					}
					e.progress("  %s", res.Reason)
					e.record(Record{RunID: cfg.RunID, Round: round, Step: "unverified", Detail: res.Reason})
					return res, nil
				}
				var timeout *testTimeoutError
				if errors.As(err, &timeout) {
					res.Outcome = Untested
					res.Reason = fmt.Sprintf("change approved by the gate but testing did not finish: test step timed out: %s", timeout.Err)
					if info, err := os.Stat(filepath.Join(cfg.Worktree, timeout.Path)); err == nil && info.Mode().IsRegular() {
						res.Reason += "; partial report: " + timeout.Path
					}
					return res, nil
				}
				return e.fail(res, err), nil
			}
			if verdict == "APPROVE" {
				res.Outcome = Approved
				return res, nil
			}
			feedback = append([]string{testPath}, feedback...)
			feedbackTest = testPath
			misses++
			e.escalate(round, misses)
			continue
		}
		feedback = append([]string{gate}, feedback...)
		feedbackGate = gate
		misses++
		e.escalate(round, misses)
	}
	if err := joinTestPlan(); err != nil {
		return e.fail(res, err), nil
	}
	res.Outcome = RoundsExhausted
	return res, nil
}

type testInterruptedError struct {
	Path   string
	Signal *SignalError
}

func (e *testInterruptedError) Error() string { return e.Signal.Error() }
func (e *testInterruptedError) Unwrap() error { return e.Signal }

type testTimeoutError struct {
	Path string
	Err  error
}

func (e *testTimeoutError) Error() string { return e.Err.Error() }
func (e *testTimeoutError) Unwrap() error { return e.Err }

func (e *Engine) testerTimeout() time.Duration {
	if chair := e.Config.Chairs.Tester; chair != nil && chair.Timeout != 0 {
		return chair.Timeout
	}
	return e.Config.StepTimeout
}

func (e *Engine) testRound(ctx context.Context, round int, plan, summary string, verify VerifyResult, verifyPath, path string) (string, error) {
	defer e.reportLeftRunning(round)
	errs, err := e.readOnlySteps(ctx, []Step{{
		Name: "test", Round: round, Chair: *e.Config.Chairs.Tester,
		Prompt:    testPrompt(e.Config.Task, plan, e.rel("test-plan.md"), summary, e.Config.VerifyCommand, verify, verifyPath, e.Config.ScratchDir, path, e.testerTimeout(), os.Getpid(), e.lessonsFor("test", *e.Config.Chairs.Tester), e.Config.CAOURL, e.Config.StateDir),
		ReplyPath: path,
	}})
	if err != nil {
		return "", err
	}
	if errs[0] != nil {
		if errors.Is(errs[0], context.Canceled) && SignalOf(ctx) != nil {
			return "", &testInterruptedError{Path: path, Signal: SignalOf(ctx)}
		}
		if errors.Is(errs[0], context.DeadlineExceeded) && ctx.Err() == nil {
			return "", &testTimeoutError{Path: path, Err: errs[0]}
		}
		return "", errs[0]
	}
	return e.verdict(ctx, round, "test", path, "")
}

func lastVerdictLine(text string) string {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	switch last := strings.TrimSpace(lines[len(lines)-1]); last {
	case "APPROVE", "CHANGES":
		return last
	}
	return ""
}

func (e *Engine) fail(res Result, err error) Result {
	res.Outcome = Failed
	res.Reason = err.Error()
	res.failure = err
	return res
}

func (e *Engine) rel(name string) string {
	return filepath.Join(RunDirRel(e.Config.RunID), name)
}

func (e *Engine) relf(format string, round int) string {
	return e.rel(fmt.Sprintf(format, round))
}

func (e *Engine) sizing() SizingConfig {
	if e.Config.Policy == nil {
		return SizingConfig{Mode: SizingOff}
	}
	return e.Config.Policy.Sizing
}

func stepKey(name string) string {
	if name == "lessons" {
		return "gate"
	}
	if name == "test-plan" {
		return "test"
	}
	if strings.HasPrefix(name, "classify-") {
		return "classify"
	}
	return name
}

func (e *Engine) resolve(key string, chair Chair) (Chair, Assignment) {
	want, bump := "", 0
	switch key {
	case "implement":
		want, bump = e.size, e.bump
	case "review", "gate", "test":
		want = e.size
	}
	return e.Config.Policy.Chair(key, chair, want, bump)
}

func (e *Engine) decideSize(ctx context.Context, planPath string) {
	sizing := e.sizing()
	if sizing.Mode == SizingOff {
		return
	}
	d := SizeDecision{Decider: sizing.Decider, Mode: sizing.Mode}
	var err error
	switch sizing.Decider {
	case DeciderPlanner:
		var plan string
		if plan, err = e.readReply(planPath); err == nil {
			d.Size, d.Reason, err = PlannerSize(plan)
		}
	case DeciderCommand:
		plan, _ := e.readReply(planPath)
		d.Size, d.Reason, err = CommandSize(ctx, sizing.Command, e.Config.Worktree, SizeRequest{Task: e.Config.Task, Plan: plan, Tiers: TierNames})
	}
	if err != nil {
		d.Error = err.Error()
	}
	if err == nil && sizing.Mode == SizingOn {
		e.size, d.Applied = d.Size, true
	}
	e.record(Record{RunID: e.Config.RunID, Step: "size", Tier: d.Size, Decision: &d})
	e.progress("  size: %s", sizeNote(d))
}

func sizeNote(d SizeDecision) string {
	if d.Error != "" {
		return fmt.Sprintf("none from %s (%s); using each step's default tier", d.Decider, d.Error)
	}
	note := fmt.Sprintf("%s from %s", d.Size, d.Decider)
	if !d.Applied {
		note += " (shadow: recorded, not applied)"
	}
	if d.Reason != "" {
		note += ": " + d.Reason
	}
	return note
}

func (e *Engine) escalate(round, misses int) {
	after := 0
	if e.Config.Policy != nil {
		after = e.Config.Policy.EscalateAfter()
	}
	if after == 0 || misses/after <= e.bump {
		return
	}
	_, before := e.resolve("implement", e.Config.Chairs.Implementer)
	e.bump = misses / after
	chair, now := e.resolve("implement", e.Config.Chairs.Implementer)
	if now.Tier == before.Tier {
		return
	}
	detail := fmt.Sprintf("%d rounds without approval: implementer %s → %s", misses, before.Tier, now.Tier)
	e.record(Record{RunID: e.Config.RunID, Round: round, Step: "escalate", Chair: &chair, Tier: now.Tier, Effort: now.Effort, TierSource: now.Source, Detail: detail})
	e.progress("  escalate: %s", detail)
}

func (e *Engine) step(ctx context.Context, s Step) (StepResult, error) {
	if s.Assignment == (Assignment{}) {
		s.Chair, s.Assignment = e.resolve(stepKey(s.Name), s.Chair)
	}
	s.Dir = e.Config.Worktree
	labelName := firstSet(s.ChairName, s.Name)
	if strings.HasPrefix(s.Name, "classify-") {
		labelName = s.Name
	}
	s.Label = TerminalLabel(e.Config.RunID, labelName)
	if s.Timeout == 0 {
		s.Timeout = s.Chair.Timeout
	}
	if s.Timeout == 0 {
		s.Timeout = e.Config.StepTimeout
	}
	out, err := e.attempt(ctx, s)
	if err != nil && out.TerminalID != "" && errors.Is(err, errNoReply) {
		nudge := s
		nudge.Name, nudge.ReuseTerminal = s.Name+"-nudge", out.TerminalID
		nudge.Prompt = nudgePrompt(s.ReplyPath)
		var nudged StepResult
		nudged, err = e.attempt(ctx, nudge)
		if nudged.TerminalID != "" {
			out.TerminalID = nudged.TerminalID
		}
		if err == nil {
			out.Status, out.LastMessage = nudged.Status, nudged.LastMessage
		}
	}
	if out.TerminalID != "" && (!s.KeepTerminal || err != nil) && s.ReuseTerminal == "" {
		_ = e.Stepper.Release(context.WithoutCancel(ctx), out.TerminalID)
		if s.KeepTerminal {
			out.TerminalID = ""
		}
	}
	return out, err
}

var stepRoles = map[string]string{"lessons": "gate", "plan": "planner", "implement": "implementer", "review": "reviewer", "gate": "gate", "test-plan": "tester", "test": "tester"}

func partOf(stepName string) string {
	if part, ok := stepRoles[stepName]; ok {
		return part
	}
	if strings.HasPrefix(stepName, "classify-") {
		return "classifier"
	}
	return stepName
}

func TerminalLabel(runID, stepName string) string {
	return "km " + runID + " · " + partOf(stepName)
}

var errNoReply = errors.New("no reply file")

func (e *Engine) attempt(ctx context.Context, s Step) (StepResult, error) {
	chair := s.Chair
	rec := Record{RunID: e.Config.RunID, Round: s.Round, Step: s.Name, ChairName: s.ChairName, Chair: &chair, Reply: s.ReplyPath,
		Tier: s.Assignment.Tier, Effort: s.Assignment.Effort, TierSource: s.Assignment.Source}
	e.progress("→ %s %s%s", strings.TrimSpace(label(s.Name, s.Round)+" "+s.ChairName), chair, tierNote(s.Assignment))
	var closeUsage func() Usage
	if e.Meter != nil {
		queued := time.Now()
		var err error
		closeUsage, err = e.Meter.Open(ctx, s)
		rec.QueueMS = time.Since(queued).Milliseconds()
		if err != nil {
			rec.Error = cancellationReason(ctx, err)
			e.record(rec)
			return StepResult{}, err
		}
	}
	finish := e.beginStep(s.Round, ActiveStep{Step: s.Name, ChairName: s.ChairName, Chair: s.Chair.Address})
	defer finish()
	start := time.Now()
	finishWatch := e.watchStep(s)
	out, err := e.Stepper.Run(ctx, s)
	finishWatch()
	rec.DurationMS = time.Since(start).Milliseconds()
	rec.TerminalID, rec.Status, rec.Model, rec.ModelHonored = out.TerminalID, out.Status, out.ReportedModel, out.ModelHonored
	if err == nil {
		var reply string
		var waited time.Duration
		reply, waited, err = e.awaitReply(ctx, s.ReplyPath)
		rec.ReplyChars, rec.ReplyWaitMS = len(reply), waited.Milliseconds()
	}
	if closeUsage != nil {
		usage := closeUsage()
		rec.Usage = &usage
	}
	if err != nil {
		rec.Error = cancellationReason(ctx, err)
		rec.LastMessage = tailChars(out.LastMessage, 800)
		if sc, ok := e.Stepper.(Screener); ok && out.TerminalID != "" {
			if _, screen, serr := sc.Screen(context.WithoutCancel(ctx), out.TerminalID); serr == nil {
				rec.Screen = tailChars(screen, 1500)
			}
		}
	}
	e.record(rec)
	e.progress("  %s in %s%s%s", strings.TrimSpace(s.ChairName+" "+statusOf(out, err)), secs(rec.DurationMS), waitNote(rec.ReplyWaitMS), errSuffix(cancellationCause(ctx, err)))
	return out, err
}

func ChairList(spec string) []string {
	specs := strings.Split(spec, ",")
	for i := range specs {
		specs[i] = strings.TrimSpace(specs[i])
	}
	return specs
}

func ReviewerName(i, n int) string {
	if n == 1 {
		return "reviewer"
	}
	return fmt.Sprintf("reviewer-%d", i+1)
}

func (e *Engine) readOnlySteps(ctx context.Context, steps []Step) ([]error, error) {
	return e.readOnlyStepsThen(ctx, steps, nil)
}

func (e *Engine) readOnlyStepsThen(ctx context.Context, steps []Step, then func(int)) ([]error, error) {
	before, err := e.treeState()
	if err != nil {
		return nil, err
	}
	errs := make([]error, len(steps))
	var wg sync.WaitGroup
	for i, s := range steps {
		wg.Go(func() {
			_, errs[i] = e.step(ctx, s)
			if errs[i] == nil && then != nil {
				then(i)
			}
		})
	}
	wg.Wait()
	after, err := e.treeState()
	if err != nil {
		return nil, err
	}
	if before != after {
		s := steps[0]
		e.record(Record{RunID: e.Config.RunID, Round: s.Round, Step: s.Name, TreeChanged: true, Error: "worktree modified"})
		if len(steps) == 1 {
			return nil, fmt.Errorf("%s chair %s modified the worktree", s.Name, s.Chair)
		}
		names := make([]string, len(steps))
		for i, s := range steps {
			names[i] = fmt.Sprintf("%s %s", s.ChairName, s.Chair)
		}
		return nil, fmt.Errorf("%s chairs %s modified the worktree", s.Name, strings.Join(names, ", "))
	}
	return errs, nil
}

type review struct {
	Name    string
	Chair   Chair
	Path    string
	Verdict string
	Err     error
}

func (e *Engine) reviewRound(ctx context.Context, round int, plan, summary string, verify VerifyResult, verifyPath string) ([]review, error) {
	chairs := e.Config.Chairs.Reviewers
	if len(chairs) == 0 {
		return nil, errors.New("no reviewers configured")
	}
	reviews := make([]review, len(chairs))
	steps := make([]Step, len(chairs))
	for i, chair := range chairs {
		name, chairName := ReviewerName(i, len(chairs)), ""
		path := e.relf("review-%d.md", round)
		if len(chairs) > 1 {
			chairName = name
			path = e.rel(fmt.Sprintf("review-%d-%d.md", round, i+1))
		}
		reviews[i] = review{Name: name, Chair: chair, Path: path}
		steps[i] = Step{Name: "review", Round: round, Chair: chair, ChairName: chairName,
			Prompt: reviewPrompt(e.Config.Task, plan, summary, e.Config.VerifyCommand, verify, verifyPath, path, e.lessonsFor("review", chair)), ReplyPath: path}
	}
	classifyErrs := make([]error, len(chairs))
	errs, err := e.readOnlyStepsThen(ctx, steps, func(i int) {
		reviews[i].Verdict, classifyErrs[i] = e.verdict(ctx, round, "review", reviews[i].Path, steps[i].ChairName)
	})
	if err != nil {
		return nil, err
	}
	var failures []error
	for i, err := range errs {
		if err == nil {
			continue
		}
		reviews[i].Err, reviews[i].Verdict = err, VerdictFailed
		failures = append(failures, fmt.Errorf("%s: %w", reviews[i].Name, err))
		e.record(Record{RunID: e.Config.RunID, Round: round, Step: "verdict-review", ChairName: steps[i].ChairName,
			Verdict: VerdictFailed, Reply: reviews[i].Path, Error: cancellationReason(ctx, err)})
	}
	if len(failures) == len(chairs) {
		if len(chairs) == 1 {
			return nil, errs[0]
		}
		combined := failures[0]
		for _, failure := range failures[1:] {
			combined = fmt.Errorf("%w; %w", combined, failure)
		}
		return nil, fmt.Errorf("every reviewer failed: %w", combined)
	}
	for _, err := range classifyErrs {
		if err != nil {
			return nil, err
		}
	}
	return reviews, nil
}

func gateRuns(when string, verifyPassed bool, verdicts []string) bool {
	if !verifyPassed {
		return false
	}
	if when == GateWhenAlways {
		return true
	}
	if when == GateWhenAll {
		for _, verdict := range verdicts {
			if verdict != "APPROVE" {
				return false
			}
		}
		return len(verdicts) > 0
	}
	for _, verdict := range verdicts {
		if verdict == "APPROVE" {
			return true
		}
	}
	return false
}

func (e *Engine) verdict(ctx context.Context, round int, subject, path, chairName string) (string, error) {
	text, _ := e.readReply(path)
	if verdict := lastVerdictLine(text); verdict != "" {
		e.record(Record{RunID: e.Config.RunID, Round: round, Step: "verdict-" + subject, ChairName: chairName,
			Verdict: verdict, Reply: path, VerdictSource: "reply"})
		noun := subject
		if subject == "test" {
			noun = "report"
		}
		e.progress("  %s verdict: %s (from the %s's last line)", firstSet(chairName, subject), verdict, noun)
		return verdict, nil
	}
	return e.classify(ctx, round, subject, path, chairName)
}

func (e *Engine) classify(ctx context.Context, round int, subject, path, chairName string) (string, error) {
	verdictPath := strings.TrimSuffix(path, filepath.Ext(path)) + ".verdict"
	out, err := e.step(ctx, Step{
		Name: "classify-" + subject, Round: round, Chair: e.Config.Chairs.Classifier, ChairName: chairName,
		Prompt: classifyPrompt(subject, path, verdictPath, e.lessonsFor("classify-"+subject, e.Config.Chairs.Classifier)), ReplyPath: verdictPath,
	})
	if err != nil {
		return "", err
	}
	text, _ := e.readReply(verdictPath)
	verdict := firstVerdict(text, out.LastMessage)
	e.record(Record{RunID: e.Config.RunID, Round: round, Step: "verdict-" + subject, ChairName: chairName, Verdict: verdict, Reply: path})
	e.progress("  %s verdict: %s", firstSet(chairName, subject), verdict)
	return verdict, nil
}

func firstVerdict(texts ...string) string {
	for _, t := range texts {
		if m := verdictRE.FindString(t); m != "" {
			return m
		}
	}
	return "UNCLEAR"
}

func (e *Engine) runVerify(ctx context.Context, round int, path string) (VerifyResult, error) {
	finish := e.beginStep(round, ActiveStep{Step: fmt.Sprintf("verify-%d", round)})
	defer finish()
	if e.Verify == nil || e.Config.VerifyCommand == "" {
		v := VerifyResult{Output: "no verify command configured\n"}
		return v, os.WriteFile(filepath.Join(e.Config.Worktree, path), []byte(v.Output), 0o644)
	}
	e.progress("→ %s %s", label("verify", round), e.Config.VerifyCommand)
	start := time.Now()
	v, err := e.Verify(ctx, e.Config.Worktree)
	code := v.ExitCode
	rec := Record{RunID: e.Config.RunID, Round: round, Step: verifyStepName(round), DurationMS: time.Since(start).Milliseconds(), ExitCode: &code, Attempts: v.Attempts, Reply: path}
	if err != nil {
		rec.Error = cancellationReason(ctx, err)
	}
	e.record(rec)
	if err != nil {
		return v, err
	}
	e.progress("  exit %d in %s%s", code, secs(rec.DurationMS), attemptsNote(v.Attempts))
	return v, os.WriteFile(filepath.Join(e.Config.Worktree, path), []byte(tail(v.Output, 400)), 0o644)
}

const replyPollInterval = 5 * time.Second

func (e *Engine) awaitReply(ctx context.Context, rel string) (string, time.Duration, error) {
	start := time.Now()
	for {
		reply, err := e.readReply(rel)
		waited := time.Since(start)
		if !errors.Is(err, errNoReply) || waited >= e.Config.ReplyGrace {
			return reply, waited, err
		}
		wait := min(replyPollInterval, e.Config.ReplyGrace-waited)
		select {
		case <-ctx.Done():
			return "", waited, ctx.Err()
		case <-time.After(wait):
		}
	}
}

func (e *Engine) readReply(rel string) (string, error) {
	if filepath.Base(rel) == "lessons-reply.json" {
		return ReadLessonsReply(filepath.Join(e.Config.Worktree, rel))
	}
	data, err := os.ReadFile(filepath.Join(e.Config.Worktree, rel))
	if errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("%w at %s", errNoReply, rel)
	}
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(string(data)) == "" {
		return "", fmt.Errorf("empty reply file at %s", rel)
	}
	return string(data), nil
}

func (e *Engine) treeState() (string, error) {
	status, err := e.gitOutput("status", "--porcelain=v1", "--untracked-files=all")
	if err != nil {
		return "", err
	}
	diff, err := e.gitOutput("diff", "HEAD", "--binary")
	if err != nil {
		return "", err
	}
	head, err := e.gitOutput("rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(head + "\x00" + status + "\x00" + diff))
	return hex.EncodeToString(sum[:]), nil
}

func (e *Engine) git(args ...string) error {
	_, err := e.gitOutput(args...)
	return err
}

func (e *Engine) gitOutput(args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", e.Config.Worktree}, args...)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, &stderr)
	}
	return string(out), nil
}

func (e *Engine) beginStep(round int, step ActiveStep) func() {
	e.mu.Lock()
	step.Started = time.Now().UTC()
	e.active = append(e.active, &step)
	e.notifyPlaying(round)
	e.mu.Unlock()
	return func() {
		e.mu.Lock()
		defer e.mu.Unlock()
		for i, active := range e.active {
			if active == &step {
				e.active = append(e.active[:i], e.active[i+1:]...)
				break
			}
		}
		e.notifyPlaying(round)
	}
}

func (e *Engine) notifyPlaying(round int) {
	e.round = max(e.round, round)
	if e.Playing == nil {
		return
	}
	active := make([]ActiveStep, 0, len(e.active))
	for _, step := range e.active {
		active = append(active, *step)
	}
	e.Playing(e.round, active)
}

func (e *Engine) record(rec Record) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if rec.Usage != nil {
		e.usage.add(*rec.Usage)
	}
	if e.Records == nil {
		return
	}
	rec.At = time.Now().UTC()
	data, _ := json.Marshal(rec)
	_, _ = e.Records.Write(append(data, '\n'))
}

func (e *Engine) progress(format string, args ...any) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.Progress != nil {
		fmt.Fprintf(e.Progress, format+"\n", args...)
	}
}

func PrepareRunDir(worktree, runID string) error {
	runs := filepath.Join(worktree, ".kapellmeister", "runs")
	if err := os.MkdirAll(filepath.Join(runs, runID), 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(runs, ".gitignore"), []byte("*\n"), 0o644)
}

func Retrying(v Verifier, retries int) Verifier {
	return func(ctx context.Context, dir string) (VerifyResult, error) {
		var out strings.Builder
		for attempt := 1; ; attempt++ {
			r, err := v(ctx, dir)
			if err != nil {
				return r, err
			}
			if attempt > 1 {
				fmt.Fprintf(&out, "\n--- km: attempt %d ---\n", attempt)
			}
			out.WriteString(r.Output)
			if r.ExitCode == 0 || attempt > retries {
				r.Attempts = attempt
				r.Output = out.String()
				return r, nil
			}
		}
	}
}

func Serialized(v Verifier, mu *sync.Mutex) Verifier {
	return func(ctx context.Context, dir string) (VerifyResult, error) {
		mu.Lock()
		defer mu.Unlock()
		return v(ctx, dir)
	}
}

func CommandVerifier(command string) Verifier {
	return func(ctx context.Context, dir string) (VerifyResult, error) {
		cmd := exec.CommandContext(ctx, "sh", "-c", command)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if ctx.Err() != nil {
			return VerifyResult{Output: string(out)}, ctx.Err()
		}
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return VerifyResult{ExitCode: exit.ExitCode(), Output: string(out)}, nil
		}
		return VerifyResult{Output: string(out)}, err
	}
}

func verifyStepName(round int) string {
	if round == 0 {
		return "verify-baseline"
	}
	return "verify"
}

func attemptsNote(n int) string {
	if n > 1 {
		return fmt.Sprintf(" after %d attempts", n)
	}
	return ""
}

func secs(ms int64) time.Duration {
	return (time.Duration(ms) * time.Millisecond).Round(time.Second)
}

func label(name string, round int) string {
	if name == "verify" && round == 0 {
		return "verify[baseline]"
	}
	if round == 0 {
		return name
	}
	return fmt.Sprintf("%s[%d]", name, round)
}

func statusOf(out StepResult, err error) string {
	if err != nil {
		return "failed"
	}
	return out.Status
}

func tierNote(a Assignment) string {
	switch a.Source {
	case "":
		return ""
	case SourceExplicit:
		return " (explicit model)"
	case SourceDefault:
		return " (" + a.Tier + ")"
	}
	return " (" + a.Tier + ", " + a.Source + ")"
}

func waitNote(ms int64) string {
	if ms < 1000 {
		return ""
	}
	return fmt.Sprintf(", then %s waiting for the reply file", secs(ms))
}

func errSuffix(err error) string {
	if err == nil {
		return ""
	}
	return ": " + err.Error()
}

func tailChars(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[len(r)-n:])
}

func tail(s string, lines int) string {
	parts := strings.Split(s, "\n")
	if len(parts) <= lines {
		return s
	}
	return strings.Join(parts[len(parts)-lines:], "\n")
}
