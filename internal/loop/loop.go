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
	Parts       map[string][]Chair `json:"parts,omitempty"`
	Tester      *Chair             `json:"tester,omitempty"`
	Planner     Chair              `json:"planner"`
	Implementer Chair              `json:"implementer"`
	Reviewers   []Chair            `json:"reviewers"`
	Gate        Chair              `json:"gate"`
	Classifier  Chair              `json:"classifier"`
}

type Step struct {
	MemoryKey     string
	Memory        string
	MemoryReason  string
	Retry         int
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
	usage         *Usage
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
	Known    []KnownFailure
	Mismatch string
	TimedOut bool
	Limit    time.Duration
}

type Verifier func(ctx context.Context, dir string) (VerifyResult, error)

type Config struct {
	StrictCommand      string
	StrictReadyCommand string
	StrictTimeout      time.Duration
	Lessons            bool
	LessonsTimeout     time.Duration
	LearnedBudget      int
	CAOURL             string
	StateDir           string
	ScratchDir         string
	MinFreeMB          int
	BlockerPoll        time.Duration
	GateWhen           string
	RunID              string
	Task               string
	Worktree           string
	Chairs             Chairs
	MaxRounds          int
	Budget             time.Duration
	BudgetStarted      time.Time
	StepTimeout        time.Duration
	VerifyCommand      string
	KnownFailures      []string
	CheckCommand       string
	ReplyGrace         time.Duration
	SkipBaseline       bool
	Policy             *Policy
}

type Record struct {
	Memory        string         `json:"memory,omitempty"`
	MemoryReason  string         `json:"memory_reason,omitempty"`
	Part          string         `json:"part,omitempty"`
	Kept          bool           `json:"kept,omitempty"`
	Retry         int            `json:"retry,omitempty"`
	Paths         []string       `json:"paths,omitempty"`
	PathsMore     int            `json:"paths_more,omitempty"`
	Restored      bool           `json:"restored,omitempty"`
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
	Approved            = "approved"
	ChangesRequested    = "changes_requested"
	Untested            = "untested"
	RoundsExhausted     = "rounds_exhausted"
	BudgetExhausted     = "budget_exhausted"
	VerifyLimit         = "verify_limit"
	BaselineMismatch    = "baseline_mismatch"
	BaselineNeedsCaller = "baseline_needs_caller"
	Failed              = "failed"
	VerdictFailed       = "FAILED"
)

type Restore struct {
	Step      string   `json:"step"`
	Round     int      `json:"round"`
	Chairs    []string `json:"chairs"`
	Paths     []string `json:"paths"`
	PathsMore int      `json:"paths_more,omitempty"`
	Outcome   string   `json:"outcome"`
}

type VerifyRetry struct {
	Round    int `json:"round"`
	Attempts int `json:"attempts"`
}

type Result struct {
	Restores      []Restore     `json:"restores,omitempty"`
	VerifyRetried []VerifyRetry `json:"verify_retried,omitempty"`
	Strict        *StrictResult `json:"strict,omitempty"`
	failure       error
	Usage         *Usage         `json:"usage,omitempty"`
	Lessons       *LessonsReport `json:"lessons,omitempty"`
	Proposed      []Lesson       `json:"-"`
	RunID         string         `json:"run_id"`
	Outcome       string         `json:"outcome"`
	Rounds        int            `json:"rounds"`
	Reason        string         `json:"reason,omitempty"`
	Worktree      string         `json:"worktree"`
	RunDir        string         `json:"run_dir"`

	MeasurementsNotRun []string `json:"measurements_not_run,omitempty"`

	KnownFailures        []string `json:"known_failures,omitempty"`
	KnownFailuresPassing []string `json:"known_failures_passing,omitempty"`
}

type ActiveStep struct {
	label     string
	Part      string    `json:"part,omitempty"`
	Round     int       `json:"round,omitempty"`
	Terminal  string    `json:"terminal,omitempty"`
	Step      string    `json:"step"`
	ChairName string    `json:"chair_name,omitempty"`
	Chair     string    `json:"chair"`
	Started   time.Time `json:"started"`
}

type Engine struct {
	kept            map[string]*keptChair
	keptOrder       []string
	Score           Score
	score           Score
	Now             func() time.Time
	snapshotDir     string
	snapshotObjects string
	keepSnapshot    bool
	restores        []Restore
	verifyRetried   []VerifyRetry
	knownFailures   []KnownFailure
	Meter           UsageMeter
	usage           Usage
	gateRound       int
	strictBefore    map[string]string
	measurements    []string
	measured        int
	Watcher         Watcher
	watch           watchState
	Config          Config
	Stepper         Stepper
	Verify          Verifier
	Records         io.Writer
	Progress        io.Writer
	Playing         func(round int, active []ActiveStep)

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
	e.score = e.Score
	if len(e.score.Steps) == 0 {
		e.score = DefaultScore()
	}
	if e.Config.Policy != nil {
		policy, bindErr := e.Config.Policy.ForScore(e.score)
		if bindErr != nil {
			return res, bindErr
		}
		e.Config.Policy = &policy
	}
	defer e.cleanupSnapshot()
	e.restores, e.verifyRetried, e.knownFailures = nil, nil, nil
	defer func() {
		if res.Outcome != BaselineMismatch {
			res.KnownFailures, res.KnownFailuresPassing = knownFailureBaseline(e.Config.KnownFailures, e.knownFailures)
		}
	}()
	defer func() { res.Restores, res.VerifyRetried = e.restores, e.verifyRetried }()
	e.mu.Lock()
	e.usage = Usage{}
	e.kept, e.keptOrder = map[string]*keptChair{}, nil
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
	e.measurements, e.measured = nil, 0
	e.strictBefore = e.strictScriptStates()
	res, err = e.play(ctx)
	if err == nil && e.Config.StrictCommand != "" {
		switch res.Outcome {
		case Approved:
			res = e.strict(ctx, res)
		case Untested:
			res = e.strictNotReached(res)
		}
	}
	e.reportUnrunMeasurements(&res)
	if res.Outcome == Failed {
		res.Reason = cancellationReason(ctx, res.failure)
	}
	if cause := SignalOf(ctx); cause != nil {
		e.record(Record{RunID: e.Config.RunID, Step: "interrupted", Error: cause.Error()})
	}
	if e.Config.Lessons && e.playingScore().LessonsBy != "" {
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
	var verify VerifyResult
	var verifyPath string

	if cfg.VerifyCommand != "" && e.Verify != nil && !cfg.SkipBaseline {
		baselinePath := e.rel("verify-0.txt")
		baseline, err := e.runVerify(ctx, 0, baselinePath)
		if err != nil {
			return e.fail(res, err), nil
		}
		if baseline.Mismatch != "" {
			res.Outcome, res.Reason = BaselineMismatch, baseline.Mismatch
			e.progress("  %s", res.Reason)
			return res, nil
		}
		if baseline.TimedOut {
			return e.fail(res, fmt.Errorf("verify did not finish within %s before any change, so km stopped it: make verify faster or raise [test] verify_timeout; see %s\n%s", baseline.Limit, baselinePath, tail(strings.TrimRight(baseline.Output, "\n"), 20))), nil
		}
		if !e.playingScore().EditsWorktree() && (baseline.ExitCode == 126 || baseline.ExitCode == 127) {
			return e.fail(res, fmt.Errorf("verify could not run (exit %d); see %s\n%s", baseline.ExitCode, baselinePath, tail(strings.TrimRight(baseline.Output, "\n"), 20))), nil
		}
		if baseline.ExitCode != 0 && e.playingScore().EditsWorktree() {
			return e.fail(res, fmt.Errorf("verify already fails before any change (exit %d); see %s\n%s", baseline.ExitCode, baselinePath, tail(strings.TrimRight(baseline.Output, "\n"), 20))), nil
		}
		if !e.playingScore().EditsWorktree() {
			verify, verifyPath = baseline, baselinePath
		}
	}

	return e.walkScore(ctx, res, verify, verifyPath)
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

func (e *Engine) chairTimeout(chair Chair) time.Duration {
	if chair.Timeout != 0 {
		return chair.Timeout
	}
	return e.Config.StepTimeout
}

func (e *Engine) testRound(ctx context.Context, step Step) (verdict string, err error) {
	defer func() {
		e.reportLeftRunning(step.Round)
		if err == nil && verdict == "APPROVE" {
			e.clearScratch()
		}
	}()
	errs, err := e.readOnlySteps(ctx, []Step{step})
	if err != nil {
		return "", err
	}
	if errs[0] != nil {
		if errors.Is(errs[0], context.Canceled) && SignalOf(ctx) != nil {
			return "", &testInterruptedError{Path: step.ReplyPath, Signal: SignalOf(ctx)}
		}
		if errors.Is(errs[0], context.DeadlineExceeded) && ctx.Err() == nil {
			return "", &testTimeoutError{Path: step.ReplyPath, Err: errs[0]}
		}
		return "", errs[0]
	}
	return e.verdict(ctx, step.Round, step.Name, step.ReplyPath, "")
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

func stepKey(name string) string { return DefaultScore().PolicyKey(name) }

func (e *Engine) playingScore() Score {
	if len(e.score.Steps) != 0 {
		return e.score
	}
	if len(e.Score.Steps) != 0 {
		return e.Score
	}
	return DefaultScore()
}

func (e *Engine) resolve(name string, chair Chair) (Chair, Assignment) {
	score := e.playingScore()
	want, bump := "", 0
	step, ok := score.Step(name)
	if !ok && name == "lessons" {
		step, ok = score.Step(score.PolicyKey(name))
	}
	if ok {
		if step.Sized {
			want = e.size
		}
		if step.Escalate {
			bump = e.bump
		}
	}
	return e.Config.Policy.Chair(score.PolicyKey(name), chair, want, bump)
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
		d.Size, d.Reason, err = CommandSize(ctx, sizing.Command, e.Config.Worktree, SizeRequest{Task: e.Config.Task, Plan: plan, Tiers: SizeNames})
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
	score := e.playingScore()
	for _, step := range score.Steps {
		if !step.Escalate {
			continue
		}
		chairs := e.Config.Chairs.Of(step.Part)
		if len(chairs) == 0 {
			continue
		}
		_, before := e.resolve(step.Name, chairs[0])
		bump := e.bump
		e.bump = misses / after
		chair, now := e.resolve(step.Name, chairs[0])
		e.bump = bump
		if now.Tier == before.Tier {
			continue
		}
		detail := fmt.Sprintf("%d rounds without approval: %s %s → %s", misses, step.Part, before.Tier, now.Tier)
		e.record(Record{RunID: e.Config.RunID, Round: round, Step: "escalate", Part: step.Part, Chair: &chair, Tier: now.Tier, Effort: now.Effort, TierSource: now.Source, Detail: detail})
		e.progress("  escalate: %s", detail)
	}
	e.bump = misses / after
}

func (e *Engine) step(ctx context.Context, s Step) (StepResult, error) {
	if s.Assignment == (Assignment{}) {
		s.Chair, s.Assignment = e.resolve(s.Name, s.Chair)
	}
	if s.Assignment.Tier != "" && s.Chair.Model == "" {
		return StepResult{}, fmt.Errorf("[tiers.%s] has no %s tier to pin against", s.Chair.Provider, s.Assignment.Tier)
	}
	s.Dir = e.Config.Worktree
	labelName := firstSet(s.ChairName, s.Name)
	if strings.HasPrefix(s.Name, "classify-") {
		labelName = s.Name
	}
	s.Label = "km " + e.Config.RunID + " · " + firstSet(e.playingScore().StepPart(labelName), labelName)
	if s.Timeout == 0 {
		s.Timeout = s.Chair.Timeout
	}
	if s.Timeout == 0 {
		s.Timeout = e.Config.StepTimeout
	}
	if e.playingScore().Resumes(s.Name) {
		s.MemoryKey = e.playingScore().StepPart(s.Name)
	}
	seat := e.prepareMemory(ctx, &s)
	out, err := e.attempt(ctx, s)
	if s.ReuseTerminal != "" && errors.Is(err, ErrReuseRefused) && ctx.Err() == nil && seat != nil {
		old := s.ReuseTerminal
		_ = e.Stepper.Release(context.WithoutCancel(ctx), old)
		s.ReuseTerminal, s.Memory = "", string(MemoryFresh)
		s.MemoryReason = fmt.Sprintf("terminal %s could not be reused: %s", old, err)
		e.mu.Lock()
		seat.terminal = ""
		e.mu.Unlock()
		out, err = e.attempt(ctx, s)
	}
	if err != nil && out.TerminalID != "" && errors.Is(err, errNoReply) {
		nudge := s
		nudge.Name, nudge.ReuseTerminal = s.Name+"-nudge", out.TerminalID
		nudge.Prompt = nudgePrompt(s.ReplyPath)
		var nudged StepResult
		nudged, err = e.attempt(ctx, nudge)
		if nudged.TerminalID != "" {
			out.TerminalID = nudged.TerminalID
		}
		if out.usage != nil && nudged.usage != nil {
			combined := *out.usage
			if combined.Tokens != nil && nudged.usage.Tokens != nil {
				tokens := combined.Tokens.add(*nudged.usage.Tokens)
				combined.Tokens = &tokens
			}
			combined.Known = combined.Known && nudged.usage.Known
			out.usage = &combined
		}
		if err == nil {
			out.Status, out.LastMessage = nudged.Status, nudged.LastMessage
		}
	}
	if seat != nil {
		e.finishMemory(ctx, s, seat, out, err)
		return out, err
	}
	if out.TerminalID != "" && (!s.KeepTerminal || err != nil) && s.ReuseTerminal == "" {
		_ = e.Stepper.Release(context.WithoutCancel(ctx), out.TerminalID)
		if s.KeepTerminal {
			out.TerminalID = ""
		}
	}
	return out, err
}

var stepRoles = func() map[string]string {
	score := DefaultScore()
	parts := map[string]string{"lessons": score.LessonsBy}
	for _, step := range score.Steps {
		parts[step.Name] = step.Part
	}
	return parts
}()

func PartOf(stepName string) string {
	if part, ok := stepRoles[stepName]; ok {
		return part
	}
	if strings.HasPrefix(stepName, "classify-") {
		return DefaultScore().Classifier
	}
	return stepName
}

func TerminalLabel(runID, stepName string) string {
	return "km " + runID + " · " + PartOf(stepName)
}

var errNoReply = errors.New("no reply file")

func (e *Engine) attempt(ctx context.Context, s Step) (StepResult, error) {
	chair := s.Chair
	rec := Record{Memory: s.Memory, MemoryReason: s.MemoryReason, Part: e.playingScore().StepPart(s.Name), Retry: s.Retry, RunID: e.Config.RunID, Round: s.Round, Step: s.Name, ChairName: s.ChairName, Chair: &chair, Reply: s.ReplyPath,
		Tier: s.Assignment.Tier, Effort: s.Assignment.Effort, TierSource: s.Assignment.Source}
	retryNote := ""
	if s.Retry > 0 {
		retryNote = " (retry)"
	}
	e.progress("→ %s %s%s%s", strings.TrimSpace(label(s.Name, s.Round)+" "+s.ChairName), chair, tierNote(s.Assignment), retryNote)
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
	finish := e.beginStep(s.Round, ActiveStep{Part: e.playingScore().StepPart(s.Name), Step: s.Name, ChairName: s.ChairName, Chair: s.Chair.Address, Terminal: s.ReuseTerminal, label: s.Label})
	defer finish()
	start := time.Now()
	finishWatch := e.watchStep(s)
	out, err := e.Stepper.Run(ctx, s)
	e.rememberFinishedTerminal(out.TerminalID)
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
		out.usage = &usage
	}
	if s.ReuseTerminal != "" && errors.Is(err, ErrReuseRefused) && ctx.Err() == nil && s.MemoryKey != "" && e.playingScore().Index(s.Name) >= 0 {
		return out, err
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
	rec.Kept = s.KeepTerminal && err == nil && out.TerminalID != ""
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
	snapshot, err := e.takeSnapshot()
	if err != nil {
		return nil, err
	}
	steps = append([]Step(nil), steps...)
	s := steps[0]
	names := make([]string, len(steps))
	for i, step := range steps {
		names[i] = strings.TrimSpace(step.ChairName + " " + step.Chair.String())
	}
	who := s.Name + " chair " + s.Chair.String()
	again := s.Name
	if len(steps) > 1 {
		who = s.Name + " chairs " + strings.Join(names, ", ")
		again = "them"
	}
	for attempt := 0; attempt < 2; attempt++ {
		errs := make([]error, len(steps))
		var wg sync.WaitGroup
		for i, step := range steps {
			wg.Go(func() {
				_, errs[i] = e.step(ctx, step)
				if errs[i] == nil && then != nil {
					then(i)
				}
			})
		}
		wg.Wait()
		after, inspectErr := e.treeState()
		tree, treeErr := e.currentSnapshotTree(snapshot)
		if inspectErr == nil && treeErr == nil && after == snapshot.State && tree == snapshot.Tree {
			return errs, nil
		}
		head, headErr := e.gitOutput("rev-parse", "HEAD")
		if headErr != nil {
			return nil, headErr
		}
		e.record(Record{RunID: e.Config.RunID, Round: s.Round, Step: s.Name, TreeChanged: true, Error: "worktree modified"})
		if head != snapshot.Head {
			return nil, fmt.Errorf("%s modified the worktree; HEAD moved", who)
		}
		paths := []string{"(paths unavailable)"}
		more := 0
		if treeErr == nil {
			var pathErr error
			paths, more, pathErr = e.snapshotPaths(snapshot, tree)
			if pathErr != nil {
				paths = []string{"(paths unavailable)"}
			}
		}
		suffix := ""
		if more > 0 {
			suffix = fmt.Sprintf(" (+%d more)", more)
		}
		e.progress("  %s changed the worktree: %s%s", who, strings.Join(paths, ", "), suffix)
		if err := e.restoreSnapshot(snapshot); err != nil {
			return nil, err
		}
		canRetry := attempt == 0 && ctx.Err() == nil
		for _, err := range errs {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				canRetry = false
			}
		}
		outcome, detail := "failed", "work restored; step failed without retry"
		if canRetry {
			outcome, detail = "retried", "work restored; step retried"
		} else if attempt > 0 {
			detail = "work restored; failed after a second change"
		}
		e.restores = append(e.restores, Restore{Step: s.Name, Round: s.Round, Chairs: names, Paths: paths, PathsMore: more, Outcome: outcome})
		chair := s.Chair
		e.record(Record{RunID: e.Config.RunID, Round: s.Round, Step: "restore", ChairName: s.ChairName, Chair: &chair, Paths: paths, PathsMore: more, Restored: true, Detail: who + ": " + detail})
		if !canRetry {
			action := "failing the run"
			if s.Name == "lessons" {
				action = "discarding lesson proposals"
			}
			if attempt > 0 {
				e.progress("  %s changed the worktree again; restored the work; %s", who, action)
				return nil, fmt.Errorf("%s modified the worktree; the work was restored after a second change", who)
			}
			e.progress("  restored the work from the snapshot; %s", action)
			return nil, fmt.Errorf("%s modified the worktree; the work was restored", who)
		}
		e.progress("  restored the work from the snapshot; running %s again, told not to change the worktree", again)
		for i := range steps {
			for _, path := range []string{steps[i].ReplyPath, strings.TrimSuffix(steps[i].ReplyPath, filepath.Ext(steps[i].ReplyPath)) + ".verdict"} {
				if err := os.Remove(filepath.Join(e.Config.Worktree, path)); err != nil && !errors.Is(err, os.ErrNotExist) {
					return nil, err
				}
			}
			steps[i].Retry = 1
			steps[i].Prompt += "\n" + readOnlyRetryPrompt(RunDirRel(e.Config.RunID))
		}
	}
	panic("unreachable")
}

type review struct {
	Name    string
	Chair   Chair
	Path    string
	Verdict string
	Err     error
}

func (e *Engine) reviewRound(ctx context.Context, spec ScoreStep, round int, hand handoff) ([]review, error) {
	chairs := e.Config.Chairs.Of(spec.Part)
	if len(chairs) == 0 {
		return nil, errors.New("no reviewers configured")
	}
	reviews := make([]review, len(chairs))
	steps := make([]Step, len(chairs))
	for i, chair := range chairs {
		name, chairName := reviewChairName(spec.Part, i, len(chairs)), ""
		path := e.stepReply(spec.Name, round)
		if len(chairs) > 1 {
			chairName = name
			path = strings.TrimSuffix(path, ".md") + fmt.Sprintf("-%d.md", i+1)
		}
		reviews[i] = review{Name: name, Chair: chair, Path: path}
		steps[i] = Step{Name: spec.Name, Round: round, Chair: chair, ChairName: chairName,
			Prompt: e.brief(spec, hand, path, chair), ReplyPath: path}
	}
	classifyErrs := make([]error, len(chairs))
	errs, err := e.readOnlyStepsThen(ctx, steps, func(i int) {
		reviews[i].Verdict, classifyErrs[i] = e.verdict(ctx, round, spec.Name, reviews[i].Path, steps[i].ChairName)
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
		e.record(Record{RunID: e.Config.RunID, Round: round, Step: "verdict-" + spec.Name, ChairName: steps[i].ChairName,
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
		if step, ok := e.playingScore().Step(subject); ok && step.Kind == KindTest {
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
		Name: "classify-" + subject, Round: round, Chair: e.classifierChair(), ChairName: chairName,
		Prompt: e.classifyBrief(subject, path, verdictPath), ReplyPath: verdictPath,
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
	if err == nil && v.ExitCode == 0 {
		if v.Mismatch = KnownFailureMismatch(e.Config.KnownFailures, v.Known); v.Mismatch != "" {
			v.ExitCode = 16
			v.Output = strings.TrimRight(v.Output, "\n") + "\nverify refused: " + v.Mismatch + "\n"
		}
	}
	code := v.ExitCode
	rec := Record{RunID: e.Config.RunID, Round: round, Step: verifyStepName(round), DurationMS: time.Since(start).Milliseconds(), ExitCode: &code, Attempts: v.Attempts, Reply: path}
	if err != nil {
		rec.Error = cancellationReason(ctx, err)
	}
	e.record(rec)
	if err != nil {
		return v, err
	}
	if v.TimedOut {
		e.progress("  did not finish within %s; stopped after %s", v.Limit, secs(rec.DurationMS))
	} else {
		e.progress("  exit %d in %s%s", code, secs(rec.DurationMS), attemptsNote(v.Attempts))
	}
	if code == 0 && v.Attempts > 1 {
		e.verifyRetried = append(e.verifyRetried, VerifyRetry{Round: round, Attempts: v.Attempts})
	}
	if code == 0 {
		e.knownFailures = v.Known
	}
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
	staged, err := e.gitOutput("diff", "--cached", "--binary")
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(head + "\x00" + status + "\x00" + diff + "\x00" + staged))
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
	step.Round = round
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
	if rec.Part == "" && rec.BlockedStep != "" {
		rec.Part = e.playingScore().StepPart(rec.BlockedStep)
	}
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
			if r.ExitCode == 0 || r.TimedOut || attempt > retries {
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

const verifySkipEnv = "KAPELLMEISTER_VERIFY_SKIP"

func CommandVerifier(command string) Verifier {
	return CommandVerifierWithin(command, 0)
}

func CommandVerifierWithin(command string, limit time.Duration) Verifier {
	return func(ctx context.Context, dir string) (VerifyResult, error) {
		var env []string
		var known func() []KnownFailure
		if record, recordErr := os.MkdirTemp("", "km-verify-steps-"); recordErr == nil {
			defer os.RemoveAll(record)
			steps := filepath.Join(record, "steps")
			env = append(os.Environ(), VerifyStepsEnv+"="+steps)
			known = func() []KnownFailure { return ReadKnownFailures(steps) }
		}
		var out lockedBuffer
		ran := RunShell(ctx, dir, command, env, limit, &out)
		result := VerifyResult{ExitCode: ran.ExitCode, Output: out.String()}
		switch {
		case ctx.Err() != nil:
			return VerifyResult{Output: out.String()}, ctx.Err()
		case ran.TimedOut:
			result.TimedOut, result.Limit = true, limit
			result.Output = strings.TrimRight(result.Output, "\n") + fmt.Sprintf("\nkm: the command did not finish within %s; km stopped it and every process it started\n", limit)
			return result, nil
		case ran.Err != nil:
			return VerifyResult{Output: out.String()}, ran.Err
		}
		if known != nil {
			result.Known = known()
		}
		return result, nil
	}
}

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
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

func (c Chairs) Of(part string) []Chair {
	switch part {
	case "planner":
		return []Chair{c.Planner}
	case "implementer":
		return []Chair{c.Implementer}
	case "reviewer":
		return c.Reviewers
	case "gate":
		return []Chair{c.Gate}
	case "classifier":
		return []Chair{c.Classifier}
	case "tester":
		if c.Tester != nil {
			return []Chair{*c.Tester}
		}
		return nil
	}
	return c.Parts[part]
}
