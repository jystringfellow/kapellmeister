package loop

import (
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

type Seat struct {
	Provider string `json:"provider"`
	Profile  string `json:"profile"`
	Model    string `json:"model,omitempty"`
}

func ParseSeat(spec string) (Seat, error) {
	parts := strings.SplitN(spec, ":", 3)
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return Seat{}, fmt.Errorf("seat %q: want provider:profile[:model]", spec)
	}
	s := Seat{Provider: parts[0], Profile: parts[1]}
	if len(parts) == 3 {
		s.Model = parts[2]
	}
	return s, nil
}

func (s Seat) String() string {
	if s.Model == "" {
		return s.Provider + ":" + s.Profile
	}
	return s.Provider + ":" + s.Profile + ":" + s.Model
}

type Seats struct {
	Planner     Seat `json:"planner"`
	Implementer Seat `json:"implementer"`
	Reviewer    Seat `json:"reviewer"`
	Gate        Seat `json:"gate"`
	Classifier  Seat `json:"classifier"`
}

type Step struct {
	Name          string
	Round         int
	Seat          Seat
	Prompt        string
	Dir           string
	ReplyPath     string
	KeepTerminal  bool
	ReuseTerminal string
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
	RunID         string
	Task          string
	Worktree      string
	Seats         Seats
	MaxRounds     int
	StepTimeout   time.Duration
	VerifyCommand string
	CheckCommand  string
	ReplyGrace    time.Duration
	SkipBaseline  bool
	Policy        *Policy
}

type Record struct {
	At           time.Time     `json:"at"`
	RunID        string        `json:"run_id"`
	Round        int           `json:"round,omitempty"`
	Step         string        `json:"step"`
	Seat         *Seat         `json:"seat,omitempty"`
	Tier         string        `json:"tier,omitempty"`
	Effort       string        `json:"effort,omitempty"`
	TierSource   string        `json:"tier_source,omitempty"`
	Decision     *SizeDecision `json:"decision,omitempty"`
	Detail       string        `json:"detail,omitempty"`
	TerminalID   string        `json:"terminal_id,omitempty"`
	Status       string        `json:"status,omitempty"`
	Model        string        `json:"model,omitempty"`
	ModelHonored *bool         `json:"model_honored,omitempty"`
	DurationMS   int64         `json:"duration_ms"`
	Reply        string        `json:"reply,omitempty"`
	ReplyChars   int           `json:"reply_chars,omitempty"`
	ReplyWaitMS  int64         `json:"reply_wait_ms,omitempty"`
	Verdict      string        `json:"verdict,omitempty"`
	ExitCode     *int          `json:"exit_code,omitempty"`
	Attempts     int           `json:"attempts,omitempty"`
	TreeChanged  bool          `json:"tree_changed,omitempty"`
	LastMessage  string        `json:"last_message,omitempty"`
	Screen       string        `json:"screen,omitempty"`
	Error        string        `json:"error,omitempty"`
}

const (
	Approved        = "approved"
	RoundsExhausted = "rounds_exhausted"
	Failed          = "failed"
)

type Result struct {
	RunID    string `json:"run_id"`
	Outcome  string `json:"outcome"`
	Rounds   int    `json:"rounds"`
	Reason   string `json:"reason,omitempty"`
	Worktree string `json:"worktree"`
	RunDir   string `json:"run_dir"`
}

type Engine struct {
	Config   Config
	Stepper  Stepper
	Verify   Verifier
	Records  io.Writer
	Progress io.Writer

	size string
	bump int
}

var verdictRE = regexp.MustCompile(`\b(APPROVE|CHANGES|UNCLEAR)\b`)

func RunDirRel(runID string) string {
	return filepath.Join(".kapellmeister", "runs", runID)
}

func (e *Engine) Run(ctx context.Context) (Result, error) {
	cfg := e.Config
	res := Result{RunID: cfg.RunID, Worktree: cfg.Worktree, RunDir: filepath.Join(cfg.Worktree, RunDirRel(cfg.RunID))}
	if err := PrepareRunDir(cfg.Worktree, cfg.RunID); err != nil {
		return res, err
	}

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
	if _, err := e.step(ctx, Step{Name: "plan", Seat: cfg.Seats.Planner, Prompt: planPrompt(cfg.Task, plan, askSize), ReplyPath: plan}); err != nil {
		return e.fail(res, err), nil
	}
	e.decideSize(ctx, plan)

	implementer := ""
	var implementerSeat Seat
	misses := 0
	defer func() {
		if implementer != "" {
			_ = e.Stepper.Release(context.WithoutCancel(ctx), implementer)
		}
	}()

	var feedback []string
	for round := 1; round <= cfg.MaxRounds; round++ {
		res.Rounds = round
		summary := e.relf("implement-%d.md", round)
		if seat, _ := e.resolve("implement", cfg.Seats.Implementer); implementer != "" && seat != implementerSeat {
			_ = e.Stepper.Release(context.WithoutCancel(ctx), implementer)
			implementer = ""
		}
		implementerSeat, _ = e.resolve("implement", cfg.Seats.Implementer)
		out, err := e.step(ctx, Step{
			Name: "implement", Round: round, Seat: cfg.Seats.Implementer,
			Prompt:    implementPrompt(cfg.Task, plan, summary, cfg.CheckCommand, feedback),
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

		review := e.relf("review-%d.md", round)
		if _, err := e.readOnlyStep(ctx, Step{
			Name: "review", Round: round, Seat: cfg.Seats.Reviewer,
			Prompt:    reviewPrompt(cfg.Task, plan, summary, cfg.VerifyCommand, verify, verifyPath, review),
			ReplyPath: review,
		}); err != nil {
			return e.fail(res, err), nil
		}
		reviewVerdict, err := e.classify(ctx, round, "review", review)
		if err != nil {
			return e.fail(res, err), nil
		}
		feedback = []string{review, verifyPath}
		if verify.ExitCode != 0 || reviewVerdict != "APPROVE" {
			misses++
			e.escalate(round, misses)
			continue
		}

		gate := e.relf("gate-%d.md", round)
		if _, err := e.readOnlyStep(ctx, Step{
			Name: "gate", Round: round, Seat: cfg.Seats.Gate,
			Prompt:    gatePrompt(cfg.Task, plan, summary, review, cfg.VerifyCommand, verify, verifyPath, gate),
			ReplyPath: gate,
		}); err != nil {
			return e.fail(res, err), nil
		}
		gateVerdict, err := e.classify(ctx, round, "gate", gate)
		if err != nil {
			return e.fail(res, err), nil
		}
		if gateVerdict == "APPROVE" {
			res.Outcome = Approved
			return res, nil
		}
		feedback = []string{gate, review, verifyPath}
		misses++
		e.escalate(round, misses)
	}
	res.Outcome = RoundsExhausted
	return res, nil
}

func (e *Engine) fail(res Result, err error) Result {
	res.Outcome = Failed
	res.Reason = err.Error()
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
	if strings.HasPrefix(name, "classify-") {
		return "classify"
	}
	return name
}

func (e *Engine) resolve(key string, seat Seat) (Seat, Assignment) {
	want, bump := "", 0
	switch key {
	case "implement":
		want, bump = e.size, e.bump
	case "review", "gate":
		want = e.size
	}
	return e.Config.Policy.Seat(key, seat, want, bump)
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
	_, before := e.resolve("implement", e.Config.Seats.Implementer)
	e.bump = misses / after
	seat, now := e.resolve("implement", e.Config.Seats.Implementer)
	if now.Tier == before.Tier {
		return
	}
	detail := fmt.Sprintf("%d rounds without approval: implementer %s → %s", misses, before.Tier, now.Tier)
	e.record(Record{RunID: e.Config.RunID, Round: round, Step: "escalate", Seat: &seat, Tier: now.Tier, Effort: now.Effort, TierSource: now.Source, Detail: detail})
	e.progress("  escalate: %s", detail)
}

func (e *Engine) step(ctx context.Context, s Step) (StepResult, error) {
	if s.Assignment == (Assignment{}) {
		s.Seat, s.Assignment = e.resolve(stepKey(s.Name), s.Seat)
	}
	s.Dir = e.Config.Worktree
	s.Label = TerminalLabel(e.Config.RunID, s.Name)
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

var stepRoles = map[string]string{"plan": "planner", "implement": "implementer", "review": "reviewer", "gate": "gate"}

func TerminalLabel(runID, stepName string) string {
	role := stepName
	if r, ok := stepRoles[stepName]; ok {
		role = r
	} else if strings.HasPrefix(stepName, "classify-") {
		role = "classifier"
	}
	return "km " + runID + " · " + role
}

var errNoReply = errors.New("no reply file")

func (e *Engine) attempt(ctx context.Context, s Step) (StepResult, error) {
	seat := s.Seat
	rec := Record{RunID: e.Config.RunID, Round: s.Round, Step: s.Name, Seat: &seat, Reply: s.ReplyPath,
		Tier: s.Assignment.Tier, Effort: s.Assignment.Effort, TierSource: s.Assignment.Source}
	e.progress("→ %s %s%s", label(s.Name, s.Round), seat, tierNote(s.Assignment))
	start := time.Now()
	out, err := e.Stepper.Run(ctx, s)
	rec.DurationMS = time.Since(start).Milliseconds()
	rec.TerminalID, rec.Status, rec.Model, rec.ModelHonored = out.TerminalID, out.Status, out.ReportedModel, out.ModelHonored
	if err == nil {
		var reply string
		var waited time.Duration
		reply, waited, err = e.awaitReply(ctx, s.ReplyPath)
		rec.ReplyChars, rec.ReplyWaitMS = len(reply), waited.Milliseconds()
	}
	if err != nil {
		rec.Error = err.Error()
		rec.LastMessage = tailChars(out.LastMessage, 800)
		if sc, ok := e.Stepper.(Screener); ok && out.TerminalID != "" {
			if _, screen, serr := sc.Screen(context.WithoutCancel(ctx), out.TerminalID); serr == nil {
				rec.Screen = tailChars(screen, 1500)
			}
		}
	}
	e.record(rec)
	e.progress("  %s in %s%s%s", statusOf(out, err), secs(rec.DurationMS), waitNote(rec.ReplyWaitMS), errSuffix(err))
	return out, err
}

func (e *Engine) readOnlyStep(ctx context.Context, s Step) (StepResult, error) {
	before, err := e.treeState()
	if err != nil {
		return StepResult{}, err
	}
	out, err := e.step(ctx, s)
	if err != nil {
		return out, err
	}
	after, err := e.treeState()
	if err != nil {
		return out, err
	}
	if before != after {
		e.record(Record{RunID: e.Config.RunID, Round: s.Round, Step: s.Name, TreeChanged: true, Error: "worktree modified"})
		return out, fmt.Errorf("%s seat %s modified the worktree", s.Name, s.Seat)
	}
	return out, nil
}

func (e *Engine) classify(ctx context.Context, round int, subject, path string) (string, error) {
	verdictPath := strings.TrimSuffix(path, filepath.Ext(path)) + ".verdict"
	out, err := e.step(ctx, Step{
		Name: "classify-" + subject, Round: round, Seat: e.Config.Seats.Classifier,
		Prompt: classifyPrompt(subject, path, verdictPath), ReplyPath: verdictPath,
	})
	if err != nil {
		return "", err
	}
	text, _ := e.readReply(verdictPath)
	verdict := firstVerdict(text, out.LastMessage)
	e.record(Record{RunID: e.Config.RunID, Round: round, Step: "verdict-" + subject, Verdict: verdict, Reply: path})
	e.progress("  %s verdict: %s", subject, verdict)
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
		rec.Error = err.Error()
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
			return "", waited, err
		case <-time.After(wait):
		}
	}
}

func (e *Engine) readReply(rel string) (string, error) {
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
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, out)
	}
	return string(out), nil
}

func (e *Engine) record(rec Record) {
	if e.Records == nil {
		return
	}
	rec.At = time.Now().UTC()
	data, _ := json.Marshal(rec)
	_, _ = e.Records.Write(append(data, '\n'))
}

func (e *Engine) progress(format string, args ...any) {
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
