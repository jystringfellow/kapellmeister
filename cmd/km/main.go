package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	"github.com/jystringfellow/kapellmeister/internal/cao"
	"github.com/jystringfellow/kapellmeister/internal/loop"
	"github.com/jystringfellow/kapellmeister/internal/rig"
)

const usage = `usage:
  km run -repo DIR (-task TEXT | -task-file FILE) [-ensemble NAME] [flags]   one pod
  km rig -repo DIR (-task TEXT | -task-file FILE) [flags]   every agent in every seat, one pod per rotation
  km preflight -worktree DIR [-agents LIST] [-tier T]       check each agent starts and answers
  km config [-worktree DIR]                                  show the effective settings and the files they come from`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "run":
		err = runCommand(os.Args[2:])
	case "rig":
		err = rigCommand(os.Args[2:])
	case "preflight":
		err = preflightCommand(os.Args[2:])
	case "config":
		err = configCommand(os.Args[2:])
	default:
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "km:", err)
		os.Exit(1)
	}
}

type common struct {
	fs                                                 *flag.FlagSet
	repo, base, task, taskFile, verify, caoURL, sizing *string
	rounds, retries                                    *int
	timeout, preflight, replyGrace                     *time.Duration
}

func commonFlags(fs *flag.FlagSet) common {
	return common{
		fs:         fs,
		repo:       fs.String("repo", "", "repository clone to branch worktrees from"),
		base:       fs.String("base", "origin/main", "ref each detached worktree starts at"),
		task:       fs.String("task", "", "task text"),
		taskFile:   fs.String("task-file", "", "file holding the task text"),
		verify:     fs.String("verify", "", "command kapellmeister runs in the worktree before planning and after each implementation round; defaults to [test] verify in .kapellmeister/config.toml, then an executable .kapellmeister/verify"),
		rounds:     fs.Int("rounds", 0, "maximum implement/review/gate rounds (default [run] rounds, else 3)"),
		retries:    fs.Int("verify-retries", 0, "times to rerun a failing verify before counting it as failed (default [run] verify_retries, else 1)"),
		preflight:  fs.Duration("preflight-timeout", 0, "how long each seat gets to answer the readiness check; 0 skips it (default [run] preflight_timeout, else 4m)"),
		timeout:    fs.Duration("timeout", 0, "timeout for each agent step (default [run] timeout, else 40m)"),
		replyGrace: fs.Duration("reply-grace", 0, "how long to wait for a reply file after a seat goes idle, before nudging it (default [run] reply_grace, else 15m)"),
		caoURL:     fs.String("cao", "", "cao-server URL (default $CAO_API_BASE_URL, then [cao] url, else http://127.0.0.1:9889)"),
		sizing:     fs.String("sizing", "", "off, shadow or on: whether the size decider's answer picks the implement, review and gate tiers (default [sizing] mode, else shadow)"),
	}
}

func flagsSet(fs *flag.FlagSet) map[string]bool {
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	return set
}

func (c common) settings(worktree string) (loop.Settings, error) {
	s, err := loop.LoadSettings(loop.Home(), worktree)
	if err != nil {
		return s, err
	}
	set := flagsSet(c.fs)
	if set["rounds"] {
		if *c.rounds < 1 {
			return s, errors.New("-rounds must be at least 1")
		}
		s.Rounds = *c.rounds
	}
	if set["verify-retries"] {
		s.VerifyRetries = *c.retries
	}
	if set["timeout"] {
		s.Timeout = *c.timeout
	}
	if set["preflight-timeout"] {
		s.PreflightTimeout = *c.preflight
	}
	if set["reply-grace"] {
		s.ReplyGrace = *c.replyGrace
	}
	if set["cao"] {
		s.CAOURL = *c.caoURL
	}
	if set["sizing"] {
		s.Policy.Sizing.Mode = *c.sizing
		if err := s.Policy.Validate(); err != nil {
			return s, fmt.Errorf("-sizing: %w", err)
		}
	}
	return s, nil
}

func seatFlags(fs *flag.FlagSet, roles ...string) map[string]*string {
	out := map[string]*string{}
	for _, role := range roles {
		out[role] = fs.String(role, "", role+" seat: an agent ("+strings.Join(agentNames(), ", ")+") or provider:profile[:model], with optional @small|@medium|@large (default [seats] "+role+" in "+loop.UserConfigPath(loop.Home())+")")
	}
	return out
}

func agentNames() []string {
	var names []string
	for _, a := range rig.DefaultAgents {
		names = append(names, a.Name)
	}
	return names
}

func resolveSeat(spec string) (loop.Seat, error) {
	base, tier, err := loop.SplitTier(spec)
	if err != nil {
		return loop.Seat{}, err
	}
	if !strings.Contains(base, ":") {
		agents, err := rig.SelectAgents(base)
		if err != nil {
			return loop.Seat{}, fmt.Errorf("seat %q: want an agent (%s) or provider:profile[:model], with optional @small|@medium|@large", spec, strings.Join(agentNames(), ", "))
		}
		seat := agents[0].Seat
		seat.Tier = tier
		return seat, nil
	}
	return loop.ParseSeat(spec)
}

func seatSpecs(s loop.Settings, flags map[string]*string) map[string]string {
	specs := map[string]string{}
	for role, spec := range s.Seats {
		specs[role] = spec
	}
	for role, value := range flags {
		if *value != "" {
			specs[role] = *value
		}
	}
	return specs
}

func resolveSeats(specs map[string]string) (loop.Seats, error) {
	resolved := map[string]loop.Seat{}
	for _, role := range loop.SeatRoles {
		seat, err := resolveSeat(specs[role])
		if err != nil {
			return loop.Seats{}, fmt.Errorf("%s: %w", role, err)
		}
		resolved[role] = seat
	}
	return loop.Seats{Planner: resolved["planner"], Implementer: resolved["implementer"], Reviewer: resolved["reviewer"], Gate: resolved["gate"], Classifier: resolved["classifier"]}, nil
}

func runSeats(s loop.Settings, ensemble string, roles map[string]*string) (loop.Settings, loop.Seats, error) {
	if ensemble != "" {
		var err error
		s, err = s.WithEnsemble(ensemble)
		if err != nil {
			return s, loop.Seats{}, err
		}
	}
	seats, err := resolveSeats(seatSpecs(s, roles))
	return s, seats, err
}

func runCommand(args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	c := commonFlags(fs)
	worktree := fs.String("worktree", "", "use this existing worktree instead of creating one")
	roles := seatFlags(fs, loop.SeatRoles...)
	ensemble := fs.String("ensemble", "", "named seat set from [ensembles.NAME] in "+loop.UserConfigPath(loop.Home())+"; role flags override it")
	if err := fs.Parse(args); err != nil {
		return err
	}
	text, err := taskText(*c.task, *c.taskFile)
	if err != nil {
		return err
	}
	userSettings, err := c.settings("")
	if err != nil {
		return err
	}
	if _, _, err := runSeats(userSettings, *ensemble, roles); err != nil {
		return err
	}
	wt := *worktree
	if wt == "" {
		if *c.repo == "" {
			return errors.New("-repo or -worktree is required")
		}
		if err := fetchRepo(*c.repo); err != nil {
			return err
		}
		if wt, err = addWorktree(*c.repo, *c.base); err != nil {
			return err
		}
	}
	if wt, err = filepath.EvalSymlinks(wt); err != nil {
		return err
	}
	s, err := c.settings(wt)
	if err != nil {
		return err
	}
	s, seats, err := runSeats(s, *ensemble, roles)
	if err != nil {
		return err
	}
	verifyCmd, checkCmd, err := loop.ResolveHooks(wt, *c.verify)
	if err != nil {
		return err
	}
	policy := s.Policy
	runID := newRunID()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	client := cao.NewClient(s.CAOURL)
	printSources(os.Stdout, s)
	if *ensemble != "" {
		fmt.Printf("ensemble %s\n", *ensemble)
	}
	printPolicy(os.Stdout, &policy)
	if err := ensureProfiles(ctx, client, &policy, stepSeats(seats)); err != nil {
		return err
	}
	if err := preflight(ctx, client, wt, s.PreflightTimeout, defaultSeats(&policy, seats)...); err != nil {
		return err
	}
	sum, err := runPod(ctx, podRun{
		RunID: runID, StateDir: filepath.Join(loop.StateDir(), "runs", runID),
		Config: loop.Config{
			RunID: runID, Task: text, Worktree: wt, Seats: seats,
			MaxRounds: s.Rounds, StepTimeout: s.Timeout, ReplyGrace: s.ReplyGrace, VerifyCommand: verifyCmd, CheckCommand: checkCmd, Policy: &policy,
		},
		Client: client, Verify: loop.Retrying(loop.CommandVerifier(verifyCmd), s.VerifyRetries), Progress: os.Stdout,
	})
	if err != nil {
		return err
	}
	printOutcome(os.Stdout, sum)
	fmt.Print(sum.DiffStat)
	return nil
}

func rigCommand(args []string) error {
	fs := flag.NewFlagSet("rig", flag.ContinueOnError)
	c := commonFlags(fs)
	agents := fs.String("agents", "", "agents to rotate through the planner, implementer, reviewer and gate seats (default [rig] agents, else "+strings.Join(agentNames(), ",")+")")
	classifier := seatFlags(fs, "classifier")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *c.repo == "" {
		return errors.New("-repo is required")
	}
	text, err := taskText(*c.task, *c.taskFile)
	if err != nil {
		return err
	}
	userSettings, err := c.settings("")
	if err != nil {
		return err
	}
	selected, err := rig.SelectAgents(firstNonEmpty(*agents, strings.Join(userSettings.RigAgents, ",")))
	if err != nil {
		return err
	}
	cls, err := resolveSeat(seatSpecs(userSettings, classifier)["classifier"])
	if err != nil {
		return err
	}
	pods, err := rig.Rotation(selected, cls)
	if err != nil {
		return err
	}

	rigID := newRunID()
	rigDir := filepath.Join(loop.StateDir(), "rigs", rigID)
	if err := os.MkdirAll(rigDir, 0o755); err != nil {
		return err
	}
	if err := fetchRepo(*c.repo); err != nil {
		return err
	}
	worktrees := make([]string, len(pods))
	for i := range pods {
		wt, err := addWorktree(*c.repo, *c.base)
		if err != nil {
			return err
		}
		if worktrees[i], err = filepath.EvalSymlinks(wt); err != nil {
			return err
		}
	}
	s, err := c.settings(worktrees[0])
	if err != nil {
		return err
	}

	fmt.Printf("km rig %s\n  state  %s\n", rigID, rigDir)
	for i, p := range pods {
		fmt.Printf("  %s  %s\n        %s\n", p.Name, p.CastLine(), worktrees[i])
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	client := cao.NewClient(s.CAOURL)
	verifyCmd, checkCmd, err := loop.ResolveHooks(worktrees[0], *c.verify)
	if err != nil {
		return err
	}
	policy := s.Policy
	printSources(os.Stdout, s)
	printPolicy(os.Stdout, &policy)
	var all []loop.Seat
	needed := map[string][]loop.Seat{}
	for _, p := range pods {
		all = append(all, defaultSeats(&policy, p.Seats)...)
		for k, v := range stepSeats(p.Seats) {
			needed[k] = append(needed[k], v...)
		}
	}
	if err := ensureProfiles(ctx, client, &policy, needed); err != nil {
		return err
	}
	if err := preflight(ctx, client, worktrees[0], s.PreflightTimeout, all...); err != nil {
		return err
	}
	verify := loop.Retrying(loop.CommandVerifier(verifyCmd), s.VerifyRetries)
	if verifyCmd != "" {
		fmt.Printf("→ verify[baseline] %s\n", verifyCmd)
		start := time.Now()
		v, err := verify(ctx, worktrees[0])
		if err != nil {
			return err
		}
		_ = os.WriteFile(filepath.Join(rigDir, "verify-baseline.txt"), []byte(v.Output), 0o644)
		fmt.Printf("  exit %d in %s (%d attempt(s))\n", v.ExitCode, time.Since(start).Round(time.Second), v.Attempts)
		if v.ExitCode != 0 {
			return fmt.Errorf("verify already fails on %s before any change; see %s", *c.base, filepath.Join(rigDir, "verify-baseline.txt"))
		}
	}

	var verifyMu, outMu sync.Mutex
	sums := make([]podSummary, len(pods))
	errs := make([]error, len(pods))
	start := time.Now()
	var wg sync.WaitGroup
	for i, p := range pods {
		wg.Add(1)
		go func() {
			defer wg.Done()
			runID := rigID + "-" + p.Name
			sums[i], errs[i] = runPod(ctx, podRun{
				Pod: p.Name, Cast: p.Cast, RunID: runID, StateDir: filepath.Join(rigDir, p.Name),
				Config: loop.Config{
					RunID: runID, Task: text, Worktree: worktrees[i], Seats: p.Seats,
					MaxRounds: s.Rounds, StepTimeout: s.Timeout, ReplyGrace: s.ReplyGrace, VerifyCommand: verifyCmd, CheckCommand: checkCmd, SkipBaseline: true, Policy: &policy,
				},
				Client: client, Verify: loop.Serialized(verify, &verifyMu),
				Progress: &prefixWriter{mu: &outMu, w: os.Stdout, prefix: "[" + p.Name + "] "},
			})
		}()
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			sums[i] = podSummary{Pod: pods[i].Name, Cast: pods[i].Cast, Result: loop.Result{Outcome: loop.Failed, Reason: err.Error(), Worktree: worktrees[i]}}
		}
	}
	if err := writeJSON(filepath.Join(rigDir, "rig.json"), map[string]any{
		"rig_id": rigID, "task": text, "verify": verifyCmd, "check": checkCmd, "duration_ms": time.Since(start).Milliseconds(), "pods": sums,
	}); err != nil {
		return err
	}
	fmt.Printf("\nrig %s finished in %s\n\n", rigID, time.Since(start).Round(time.Second))
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "POD\tPLANNER\tIMPLEMENTER\tREVIEWER\tGATE\tOUTCOME\tROUNDS\tTIME\tDIFF")
	for _, s := range sums {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%d\t%s\t%s\n", s.Pod, s.Cast["planner"], s.Cast["implementer"], s.Cast["reviewer"], s.Cast["gate"],
			s.Outcome, s.Rounds, (time.Duration(s.DurationMS) * time.Millisecond).Round(time.Second), shortStat(s.DiffStat))
	}
	tw.Flush()
	for _, s := range sums {
		if s.Reason != "" {
			fmt.Printf("%s: %s\n", s.Pod, s.Reason)
		}
	}
	return nil
}

func preflightCommand(args []string) error {
	fs := flag.NewFlagSet("preflight", flag.ContinueOnError)
	worktree := fs.String("worktree", "", "git worktree to start the seats in")
	agents := fs.String("agents", "", "agents to check (default [rig] agents, else "+strings.Join(agentNames(), ",")+")")
	timeout := fs.Duration("timeout", 0, "how long each seat gets to answer (default [run] preflight_timeout, else 4m)")
	tier := fs.String("tier", "", "check each agent on this tier's model and effort (small, medium or large) instead of its profile's own")
	caoURL := fs.String("cao", "", "cao-server URL (default $CAO_API_BASE_URL, then [cao] url, else http://127.0.0.1:9889)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *worktree == "" {
		return errors.New("-worktree is required")
	}
	s, err := loop.LoadSettings(loop.Home(), *worktree)
	if err != nil {
		return err
	}
	set := flagsSet(fs)
	if set["timeout"] {
		s.PreflightTimeout = *timeout
	}
	selected, err := rig.SelectAgents(firstNonEmpty(*agents, strings.Join(s.RigAgents, ",")))
	if err != nil {
		return err
	}
	client := cao.NewClient(firstNonEmpty(*caoURL, s.CAOURL))
	var seats []loop.Seat
	for _, a := range selected {
		seats = append(seats, a.Seat)
	}
	if *tier != "" {
		pinned := loop.Policy{Tiers: s.Policy.Tiers, Steps: map[string]loop.StepPolicy{"plan": {Tier: *tier, Min: *tier, Max: *tier}}, Sizing: s.Policy.Sizing}
		if err := pinned.Validate(); err != nil {
			return err
		}
		if err := ensureProfiles(context.Background(), client, &pinned, map[string][]loop.Seat{"plan": seats}); err != nil {
			return err
		}
		for i, seat := range seats {
			seats[i], _ = pinned.At(seat, *tier)
		}
	}
	return preflight(context.Background(), client, *worktree, s.PreflightTimeout, seats...)
}

func configCommand(args []string) error {
	fs := flag.NewFlagSet("config", flag.ContinueOnError)
	worktree := fs.String("worktree", ".", "repository whose .kapellmeister/config.toml to layer over "+loop.UserConfigPath(loop.Home()))
	if err := fs.Parse(args); err != nil {
		return err
	}
	s, err := loop.LoadSettings(loop.Home(), *worktree)
	if err != nil {
		return err
	}
	printSources(os.Stdout, s)
	fmt.Printf("cao    %s\nstate  %s\nrun    rounds %d, verify retries %d, step timeout %s, reply grace %s, preflight %s\n",
		s.CAOURL, loop.StateDir(), s.Rounds, s.VerifyRetries, s.Timeout, s.ReplyGrace, s.PreflightTimeout)
	var seats []string
	for _, role := range loop.SeatRoles {
		seats = append(seats, role+"="+s.Seats[role])
	}
	fmt.Printf("seats  %s\nrig    %s\n", strings.Join(seats, " "), strings.Join(s.RigAgents, ","))
	printEnsembles(os.Stdout, s)
	printPolicy(os.Stdout, &s.Policy)
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "\nPROVIDER\tTIER\tMODEL\tEFFORT")
	for _, provider := range sortedKeys(s.Policy.Tiers) {
		for _, t := range loop.TierNames {
			spec, ok := s.Policy.Tiers[provider][t]
			if !ok {
				continue
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", provider, t, spec.Model, firstNonEmpty(spec.Effort, "-"))
		}
	}
	return tw.Flush()
}

func printEnsembles(w io.Writer, s loop.Settings) {
	for _, name := range sortedKeys(s.Ensembles) {
		ensemble := s.Ensembles[name]
		specs := ensemble.Seats()
		var seats []string
		for _, role := range loop.SeatRoles {
			if spec, ok := specs[role]; ok {
				seats = append(seats, role+"="+spec)
			}
		}
		if ensemble.Escalate != nil {
			seats = append(seats, fmt.Sprintf("escalate=%t", *ensemble.Escalate))
		}
		fmt.Fprintf(w, "ensemble %s  %s\n", name, strings.Join(seats, " "))
	}
}

func printSources(w io.Writer, s loop.Settings) {
	if len(s.Files) == 0 {
		fmt.Fprintln(w, "config built-in defaults (no config files found)")
		return
	}
	fmt.Fprintf(w, "config %s\n", strings.Join(s.Files, ", then "))
}

func printPolicy(w io.Writer, p *loop.Policy) {
	var steps []string
	for _, key := range loop.StepKeys {
		s := p.Steps[key]
		if s.Min == s.Max {
			steps = append(steps, key+" "+s.Tier)
		} else {
			steps = append(steps, fmt.Sprintf("%s %s (%s–%s)", key, s.Tier, s.Min, s.Max))
		}
	}
	sizing := fmt.Sprintf("sizing %s by %s", p.Sizing.Mode, p.Sizing.Decider)
	if after := p.EscalateAfter(); after > 0 {
		sizing += fmt.Sprintf("; the implementer moves up a tier after every %d rounds without approval", after)
	}
	fmt.Fprintf(w, "tiers  %s\n       %s\n", strings.Join(steps, ", "), sizing)
}

func stepSeats(s loop.Seats) map[string][]loop.Seat {
	return map[string][]loop.Seat{
		"plan": {s.Planner}, "implement": {s.Implementer}, "review": {s.Reviewer}, "gate": {s.Gate}, "classify": {s.Classifier},
	}
}

func defaultSeats(p *loop.Policy, s loop.Seats) []loop.Seat {
	var out []loop.Seat
	for _, key := range loop.StepKeys {
		for _, seat := range stepSeats(s)[key] {
			resolved, _ := p.Seat(key, seat, "", 0)
			out = append(out, resolved)
		}
	}
	return out
}

func ensureProfiles(ctx context.Context, client *cao.Client, p *loop.Policy, steps map[string][]loop.Seat) error {
	variants := p.EffortVariants(steps)
	if len(variants) == 0 {
		return nil
	}
	installed, err := client.Profiles(ctx)
	if err != nil {
		return fmt.Errorf("listing CAO profiles: %w", err)
	}
	have := map[string]bool{}
	for _, prof := range installed {
		have[prof.Name] = prof.Loadable
	}
	dir := filepath.Join(loop.StateDir(), "profiles")
	for _, v := range variants {
		if have[v.Name()] {
			continue
		}
		source, err := loop.EffortProfileSource(v.Provider, v.Base, v.Effort)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		path := filepath.Join(dir, v.Name()+".md")
		if err := os.WriteFile(path, []byte(source), 0o644); err != nil {
			return err
		}
		if out, err := exec.CommandContext(ctx, "cao", "install", path, "--provider", v.Provider).CombinedOutput(); err != nil {
			return fmt.Errorf("cao install %s: %w: %s", path, err, out)
		}
		fmt.Printf("→ installed CAO profile %s from %s\n", v.Name(), path)
	}
	return nil
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func preflight(ctx context.Context, client *cao.Client, worktree string, timeout time.Duration, seats ...loop.Seat) error {
	if timeout == 0 {
		return nil
	}
	unique := loop.UniqueSeats(seats...)
	fmt.Printf("→ preflight %d seat(s)\n", len(unique))
	results, err := loop.Preflight(ctx, loop.CAOStepper{Client: client}, worktree, unique, timeout)
	if err != nil {
		return err
	}
	var blocked []string
	for _, r := range results {
		d := (time.Duration(r.DurationMS) * time.Millisecond).Round(time.Second)
		if r.OK {
			fmt.Printf("  ok      %s (%s)%s\n", r.Seat, d, modelNote(r))
			continue
		}
		fmt.Printf("  BLOCKED %s (%s): %s\n", r.Seat, d, firstNonEmpty(r.Blocker, r.Error))
		hint := r.Seat.String()
		if r.Session != "" {
			hint += fmt.Sprintf(": resolve it with `tmux attach -t %s`, then detach with Ctrl-b d", r.Session)
		}
		blocked = append(blocked, hint)
	}
	if len(blocked) > 0 {
		return fmt.Errorf("preflight failed for %d seat(s):\n  %s", len(blocked), strings.Join(blocked, "\n  "))
	}
	return nil
}

func modelNote(r loop.PreflightResult) string {
	if r.Seat.Model == "" {
		return ""
	}
	if r.Honored != nil && !*r.Honored {
		return fmt.Sprintf("; CAO did not apply the requested model (reported %q)", r.Model)
	}
	if r.Model != "" && r.Model != r.Seat.Model {
		return fmt.Sprintf("; CAO reported model %q", r.Model)
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

type podRun struct {
	Pod      string
	Cast     map[string]string
	RunID    string
	StateDir string
	Config   loop.Config
	Client   *cao.Client
	Verify   loop.Verifier
	Progress io.Writer
}

type podSummary struct {
	Pod  string            `json:"pod,omitempty"`
	Cast map[string]string `json:"cast,omitempty"`
	loop.Result
	DurationMS int64  `json:"duration_ms"`
	DiffStat   string `json:"diff_stat"`
}

func runPod(ctx context.Context, p podRun) (podSummary, error) {
	if err := os.MkdirAll(p.StateDir, 0o755); err != nil {
		return podSummary{}, err
	}
	telemetry, err := os.Create(filepath.Join(p.StateDir, "telemetry.jsonl"))
	if err != nil {
		return podSummary{}, err
	}
	defer telemetry.Close()
	if err := writeJSON(filepath.Join(p.StateDir, "config.json"), p.Config); err != nil {
		return podSummary{}, err
	}
	if p.Pod == "" {
		fmt.Fprintf(p.Progress, "km run %s\n  worktree  %s\n  state     %s\n", p.RunID, p.Config.Worktree, p.StateDir)
	}
	eng := &loop.Engine{Config: p.Config, Stepper: loop.CAOStepper{Client: p.Client}, Verify: p.Verify, Records: telemetry, Progress: p.Progress}
	start := time.Now()
	res, err := eng.Run(ctx)
	if err != nil {
		return podSummary{}, err
	}
	if err := copyDir(res.RunDir, filepath.Join(p.StateDir, "replies")); err != nil {
		fmt.Fprintln(os.Stderr, "km: copying replies:", err)
	}
	stat, _ := exec.Command("git", "-C", p.Config.Worktree, "diff", "HEAD", "--stat").CombinedOutput()
	sum := podSummary{Pod: p.Pod, Cast: p.Cast, Result: res, DurationMS: time.Since(start).Milliseconds(), DiffStat: string(stat)}
	if err := writeJSON(filepath.Join(p.StateDir, "result.json"), sum); err != nil {
		return sum, err
	}
	if p.Pod != "" {
		printOutcome(p.Progress, sum)
	}
	return sum, nil
}

func printOutcome(w io.Writer, s podSummary) {
	line := fmt.Sprintf("%s after %d round(s) in %s", s.Outcome, s.Rounds, (time.Duration(s.DurationMS) * time.Millisecond).Round(time.Second))
	if s.Reason != "" {
		line += ": " + s.Reason
	}
	fmt.Fprintln(w, "\n"+line)
}

func shortStat(stat string) string {
	lines := strings.Split(strings.TrimSpace(stat), "\n")
	if len(lines) == 0 || lines[len(lines)-1] == "" {
		return "-"
	}
	return strings.TrimSpace(lines[len(lines)-1])
}

type prefixWriter struct {
	mu     *sync.Mutex
	w      io.Writer
	prefix string
}

func (p *prefixWriter) Write(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out bytes.Buffer
	for _, line := range strings.SplitAfter(string(b), "\n") {
		if line == "" {
			continue
		}
		if line != "\n" {
			out.WriteString(p.prefix)
		}
		out.WriteString(line)
	}
	if _, err := p.w.Write(out.Bytes()); err != nil {
		return 0, err
	}
	return len(b), nil
}

func taskText(task, file string) (string, error) {
	switch {
	case task != "" && file != "":
		return "", errors.New("use -task or -task-file, not both")
	case file != "":
		data, err := os.ReadFile(file)
		return string(data), err
	case task != "":
		return task, nil
	}
	return "", errors.New("-task or -task-file is required")
}

func fetchRepo(repo string) error {
	for _, args := range [][]string{{"fetch", "origin", "--prune"}, {"worktree", "prune"}} {
		if out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			return fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, out)
		}
	}
	return nil
}

func addWorktree(repo, base string) (string, error) {
	wt, err := os.MkdirTemp("", "wt-km-")
	if err != nil {
		return "", err
	}
	if wt, err = filepath.EvalSymlinks(wt); err != nil {
		return "", err
	}
	if out, err := exec.Command("git", "-C", repo, "worktree", "add", "--detach", wt, base).CombinedOutput(); err != nil {
		return "", fmt.Errorf("git worktree add: %w: %s", err, out)
	}
	return wt, nil
}

func newRunID() string {
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	return time.Now().Format("20060102-150405") + "-" + hex.EncodeToString(b)
}

func writeJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

func copyDir(src, dst string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(src, e.Name()))
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dst, e.Name()), data, 0o644); err != nil {
			return err
		}
	}
	return nil
}
