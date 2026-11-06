package main

import (
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
	"text/tabwriter"
	"time"

	"github.com/jystringfellow/kapellmeister/internal/cao"
	"github.com/jystringfellow/kapellmeister/internal/loop"
)

const usage = `usage:
  km run -repo DIR (-task TEXT | -task-file FILE) [-ensemble NAME] [flags]   one ensemble
  km audition -worktree DIR [-ensemble NAME] [-keep]       boot the ensemble, cue every chair once, tear down
  km rehearse -worktree DIR (-task TEXT | -task-file FILE) [-ensemble NAME]  brief the ensemble and leave it standing
  km play SESSION [-max-rounds N] [-verify CMD] [-timeout D] [-cao URL]  run the score on a standing ensemble
  km ensemble list                                         list standing ensembles
  km ensemble down [-cao URL] SESSION                       take down a standing ensemble
  km cue done -queue FILE CUE [NOTE]                         close a cue (chairs run this)
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
	case "audition":
		err = auditionCommand(os.Args[2:])
	case "rehearse":
		err = rehearseCommand(os.Args[2:])
	case "play":
		err = playCommand(os.Args[2:])
	case "ensemble":
		err = ensembleCommand(os.Args[2:], os.Stdout)
	case "cue":
		err = cueCommand(os.Args[2:])
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
		preflight:  fs.Duration("preflight-timeout", 0, "how long each chair gets to answer the readiness check; 0 skips it (default [run] preflight_timeout, else 4m)"),
		timeout:    fs.Duration("timeout", 0, "timeout for each agent step (default [run] timeout, else 40m)"),
		replyGrace: fs.Duration("reply-grace", 0, "how long to wait for a reply file after a chair goes idle, before nudging it (default [run] reply_grace, else 15m)"),
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

func chairFlags(fs *flag.FlagSet, roles ...string) map[string]*string {
	out := map[string]*string{}
	for _, role := range roles {
		listHelp := ""
		if role == "reviewer" {
			listHelp = "; accepts a comma-separated list replacing all configured reviewers"
		}
		out[role] = fs.String(role, "", role+" chair: an agent ("+strings.Join(loop.AgentNames(), ", ")+") or provider:profile[:model], with optional @small|@medium|@large (default [chairs] "+role+" in "+loop.UserConfigPath(loop.Home())+")"+listHelp)
	}
	return out
}

func chairSpecs(s loop.Settings, flags map[string]*string) map[string]string {
	specs := map[string]string{}
	for role, spec := range s.Chairs {
		specs[role] = spec
	}
	for role, value := range flags {
		if *value != "" {
			specs[role] = *value
		}
	}
	return specs
}

func resolveChairs(specs map[string]string) (loop.Chairs, error) {
	resolved := map[string]loop.Chair{}
	var reviewers []loop.Chair
	for _, role := range loop.ChairRoles {
		if role == "reviewer" {
			list := loop.ChairList(specs[role])
			for i, spec := range list {
				chair, err := loop.ResolveChair(spec)
				if err != nil {
					return loop.Chairs{}, fmt.Errorf("%s: %w", loop.ReviewerName(i, len(list)), err)
				}
				reviewers = append(reviewers, chair)
			}
			continue
		}
		if strings.Contains(specs[role], ",") {
			return loop.Chairs{}, fmt.Errorf("%s: chair %q: only reviewer takes a list of chairs", role, specs[role])
		}
		chair, err := loop.ResolveChair(specs[role])
		if err != nil {
			return loop.Chairs{}, fmt.Errorf("%s: %w", role, err)
		}
		resolved[role] = chair
	}
	return loop.Chairs{Planner: resolved["planner"], Implementer: resolved["implementer"], Reviewers: reviewers, Gate: resolved["gate"], Classifier: resolved["classifier"]}, nil
}

func runChairs(s loop.Settings, ensemble string, roles map[string]*string) (loop.Settings, loop.Chairs, error) {
	if ensemble != "" {
		var err error
		s, err = s.WithEnsemble(ensemble)
		if err != nil {
			return s, loop.Chairs{}, err
		}
	}
	chairs, err := resolveChairs(chairSpecs(s, roles))
	return s, chairs, err
}

var roleSteps = map[string]string{"planner": "plan", "implementer": "implement", "reviewer": "review", "gate": "gate", "classifier": "classify"}

func checkPins(policy loop.Policy, specs map[string]string, chairs loop.Chairs) error {
	for _, role := range loop.ChairRoles {
		group := stepChairs(chairs)[roleSteps[role]]
		list := []string{specs[role]}
		if role == "reviewer" {
			list = loop.ChairList(specs[role])
		}
		for i, chair := range group {
			name := role
			if role == "reviewer" {
				name = loop.ReviewerName(i, len(group))
			}
			if err := checkPin(policy, role, name, list[i], chair); err != nil {
				return err
			}
		}
	}
	return nil
}

func checkPin(policy loop.Policy, part, name, spec string, chair loop.Chair) error {
	if chair.Tier == "" {
		return nil
	}
	tiers := policy.Tiers[chair.Provider]
	if len(tiers) == 0 {
		return fmt.Errorf("%s: chair %q: no [tiers.%s] to pin a tier against", name, spec, chair.Provider)
	}
	_, assigned := policy.Chair(roleSteps[part], chair, "", 0)
	if _, ok := tiers[assigned.Tier]; !ok {
		return fmt.Errorf("%s: chair %q: [tiers.%s] has no %s tier to pin against", name, spec, chair.Provider, assigned.Tier)
	}
	return nil
}

func runCommand(args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	c := commonFlags(fs)
	worktree := fs.String("worktree", "", "use this existing worktree instead of creating one")
	roles := chairFlags(fs, loop.ChairRoles...)
	ensemble := fs.String("ensemble", "", ensembleHelp)
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
	if _, _, _, err := lineUp(userSettings, firstNonEmpty(*worktree, *c.repo), *ensemble, roles); err != nil {
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
	if wt, err = worktreeDir(wt); err != nil {
		return err
	}
	s, err := c.settings(wt)
	if err != nil {
		return err
	}
	s, chairs, _, err := lineUp(s, wt, *ensemble, roles)
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
	if err := ensureProfiles(ctx, client, &policy, stepChairs(chairs)); err != nil {
		return err
	}
	if err := preflight(ctx, client, wt, s.PreflightTimeout, defaultChairs(&policy, chairs)...); err != nil {
		return err
	}
	sum, err := runEnsemble(ctx, ensembleRun{
		RunID: runID, StateDir: filepath.Join(loop.StateDir(), "runs", runID),
		Config: loop.Config{
			RunID: runID, Task: text, Worktree: wt, Chairs: chairs, GateWhen: s.GateWhen,
			MaxRounds: s.Rounds, StepTimeout: s.Timeout, ReplyGrace: s.ReplyGrace, VerifyCommand: verifyCmd, CheckCommand: checkCmd, Policy: &policy,
		},
		Stepper: loop.CAOStepper{Client: client}, Verify: loop.Retrying(loop.CommandVerifier(verifyCmd), s.VerifyRetries), Progress: os.Stdout,
	})
	if err != nil {
		return err
	}
	printOutcome(os.Stdout, sum)
	fmt.Print(sum.DiffStat)
	return nil
}

func preflightCommand(args []string) error {
	fs := flag.NewFlagSet("preflight", flag.ContinueOnError)
	worktree := fs.String("worktree", "", "git worktree to start the chairs in")
	agents := fs.String("agents", "", "agents to check (default "+strings.Join(loop.AgentNames(), ",")+")")
	timeout := fs.Duration("timeout", 0, "how long each chair gets to answer (default [run] preflight_timeout, else 4m)")
	tier := fs.String("tier", "", "check each agent on this tier's model and effort (small, medium or large) instead of its profile's own")
	caoURL := fs.String("cao", "", "cao-server URL (default $CAO_API_BASE_URL, then [cao] url, else http://127.0.0.1:9889)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *worktree == "" {
		return errors.New("-worktree is required")
	}
	wt, err := worktreeDir(*worktree)
	if err != nil {
		return err
	}
	s, err := loop.LoadSettings(loop.Home(), wt)
	if err != nil {
		return err
	}
	set := flagsSet(fs)
	if set["timeout"] {
		s.PreflightTimeout = *timeout
	}
	selected, err := loop.SelectAgents(firstNonEmpty(*agents, strings.Join(loop.AgentNames(), ",")))
	if err != nil {
		return err
	}
	client := cao.NewClient(firstNonEmpty(*caoURL, s.CAOURL))
	var chairs []loop.Chair
	for _, a := range selected {
		chairs = append(chairs, a.Chair)
	}
	if *tier != "" {
		pinned := loop.Policy{Tiers: s.Policy.Tiers, Steps: map[string]loop.StepPolicy{"plan": {Tier: *tier, Min: *tier, Max: *tier}}, Sizing: s.Policy.Sizing}
		if err := pinned.Validate(); err != nil {
			return err
		}
		if err := ensureProfiles(context.Background(), client, &pinned, map[string][]loop.Chair{"plan": chairs}); err != nil {
			return err
		}
		for i, chair := range chairs {
			chairs[i], _ = pinned.At(chair, *tier)
		}
	}
	return preflight(context.Background(), client, wt, s.PreflightTimeout, chairs...)
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
	printChairs(os.Stdout, s)
	printEnsembles(os.Stdout, s)
	printEnsembleFiles(os.Stdout, loop.Home(), *worktree)
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

func printChairs(w io.Writer, s loop.Settings) {
	var chairs []string
	for _, role := range loop.ChairRoles {
		chairs = append(chairs, role+"="+s.Chairs[role])
	}
	fmt.Fprintf(w, "chairs  %s gate_when=%s\n", strings.Join(chairs, " "), s.GateWhen)
}

func printEnsembles(w io.Writer, s loop.Settings) {
	for _, name := range sortedKeys(s.Ensembles) {
		ensemble := s.Ensembles[name]
		specs := ensemble.Chairs()
		var chairs []string
		for _, role := range loop.ChairRoles {
			if spec, ok := specs[role]; ok {
				chairs = append(chairs, role+"="+spec)
			}
		}
		if ensemble.Escalate != nil {
			chairs = append(chairs, fmt.Sprintf("escalate=%t", *ensemble.Escalate))
		}
		if ensemble.GateWhen != "" {
			chairs = append(chairs, "gate_when="+ensemble.GateWhen)
		}
		fmt.Fprintf(w, "ensemble %s  %s\n", name, strings.Join(chairs, " "))
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

func stepChairs(s loop.Chairs) map[string][]loop.Chair {
	return map[string][]loop.Chair{
		"plan": {s.Planner}, "implement": {s.Implementer}, "review": s.Reviewers, "gate": {s.Gate}, "classify": {s.Classifier},
	}
}

func defaultChairs(p *loop.Policy, s loop.Chairs) []loop.Chair {
	var out []loop.Chair
	for _, key := range loop.StepKeys {
		for _, chair := range stepChairs(s)[key] {
			resolved, _ := p.Chair(key, chair, "", 0)
			out = append(out, resolved)
		}
	}
	return out
}

func ensureProfiles(ctx context.Context, client *cao.Client, p *loop.Policy, steps map[string][]loop.Chair) error {
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

func preflight(ctx context.Context, client *cao.Client, worktree string, timeout time.Duration, chairs ...loop.Chair) error {
	if timeout == 0 {
		return nil
	}
	unique := loop.UniqueChairs(chairs...)
	fmt.Printf("→ preflight %d chair(s)\n", len(unique))
	results, err := loop.Preflight(ctx, loop.CAOStepper{Client: client}, worktree, unique, timeout)
	if err != nil {
		return err
	}
	var blocked []string
	for _, r := range results {
		d := (time.Duration(r.DurationMS) * time.Millisecond).Round(time.Second)
		if r.OK {
			fmt.Printf("  ok      %s (%s)%s\n", r.Chair, d, modelNote(r))
			continue
		}
		fmt.Printf("  BLOCKED %s (%s): %s\n", r.Chair, d, firstNonEmpty(r.Blocker, r.Error))
		hint := r.Chair.String()
		if r.Session != "" {
			hint += fmt.Sprintf(": resolve it with `tmux attach -t %s`, then detach with Ctrl-b d", r.Session)
		}
		blocked = append(blocked, hint)
	}
	if len(blocked) > 0 {
		return fmt.Errorf("preflight failed for %d chair(s):\n  %s", len(blocked), strings.Join(blocked, "\n  "))
	}
	return nil
}

func modelNote(r loop.PreflightResult) string {
	if r.Chair.Model == "" {
		return ""
	}
	if r.Honored != nil && !*r.Honored {
		return fmt.Sprintf("; CAO did not apply the requested model (reported %q)", r.Model)
	}
	if r.Model != "" && r.Model != r.Chair.Model {
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

type ensembleRun struct {
	Command  string
	RunID    string
	StateDir string
	Config   loop.Config
	Stepper  loop.Stepper
	Verify   loop.Verifier
	Progress io.Writer
}

type ensembleSummary struct {
	loop.Result
	DurationMS int64  `json:"duration_ms"`
	DiffStat   string `json:"diff_stat"`
}

func runEnsemble(ctx context.Context, p ensembleRun) (ensembleSummary, error) {
	if err := os.MkdirAll(p.StateDir, 0o755); err != nil {
		return ensembleSummary{}, err
	}
	telemetry, err := os.Create(filepath.Join(p.StateDir, "telemetry.jsonl"))
	if err != nil {
		return ensembleSummary{}, err
	}
	defer telemetry.Close()
	if err := writeJSON(filepath.Join(p.StateDir, "config.json"), p.Config); err != nil {
		return ensembleSummary{}, err
	}
	fmt.Fprintf(p.Progress, "km %s %s\n  worktree  %s\n  state     %s\n", firstNonEmpty(p.Command, "run"), p.RunID, p.Config.Worktree, p.StateDir)
	eng := &loop.Engine{Config: p.Config, Stepper: p.Stepper, Verify: p.Verify, Records: telemetry, Progress: p.Progress}
	start := time.Now()
	res, err := eng.Run(ctx)
	if err != nil {
		return ensembleSummary{}, err
	}
	if err := copyDir(res.RunDir, filepath.Join(p.StateDir, "replies")); err != nil {
		fmt.Fprintln(os.Stderr, "km: copying replies:", err)
	}
	stat, _ := exec.Command("git", "-C", p.Config.Worktree, "diff", "HEAD", "--stat").CombinedOutput()
	sum := ensembleSummary{Result: res, DurationMS: time.Since(start).Milliseconds(), DiffStat: string(stat)}
	if err := writeJSON(filepath.Join(p.StateDir, "result.json"), sum); err != nil {
		return sum, err
	}
	return sum, nil
}

func printOutcome(w io.Writer, s ensembleSummary) {
	line := fmt.Sprintf("%s after %d round(s) in %s", s.Outcome, s.Rounds, (time.Duration(s.DurationMS) * time.Millisecond).Round(time.Second))
	if s.Reason != "" {
		line += ": " + s.Reason
	}
	fmt.Fprintln(w, "\n"+line)
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
