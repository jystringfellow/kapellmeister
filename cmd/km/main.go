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
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/jystringfellow/kapellmeister/internal/cao"
	"github.com/jystringfellow/kapellmeister/internal/loop"
)

const usage = `usage:
  km run -repo DIR [-base BRANCH] (-task TEXT | -task-file FILE) [-ensemble NAME] [flags]   one ensemble
  km audition -worktree DIR [-ensemble NAME] [-keep]       boot the ensemble, cue every chair once, tear down
  km rehearse -worktree DIR (-task TEXT | -task-file FILE) [-ensemble NAME]  brief the ensemble and leave it standing
  km play SESSION [-max-rounds N] [-verify CMD] [-timeout D] [-cao URL]  run the score on a standing ensemble
  km reseat SESSION ADDRESS [-agent SPEC] [-timeout D] [-cao URL]  replace one chair's occupant
  km ensemble list                                         list standing ensembles
  km ensemble down [-cao URL] SESSION                       take down a standing ensemble
  km report [-json] [-since DATE] [-repository NAME] [-max-depth N] [-max-entries N] [DIR...]   summarize run records
  km status [-json] [-max-depth N] [-max-entries N] [DIR...]   show the runs in progress
  km cue done -queue FILE CUE [NOTE]                         close a cue (chairs run this)
  km preflight -worktree DIR [-agents LIST] [-tier T]       check each agent starts and answers
  km config [-worktree DIR]                                  show the effective settings and the files they come from`

func main() {
	os.Exit(execute(os.Args[1:], os.Stderr))
}

func execute(args []string, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, usage)
		return 2
	}
	var err error
	switch args[0] {
	case "run":
		err = runCommand(args[1:])
	case "audition":
		err = auditionCommand(args[1:])
	case "rehearse":
		err = rehearseCommand(args[1:])
	case "play":
		err = playCommand(args[1:])
	case "reseat":
		err = reseatCommand(args[1:])
	case "ensemble":
		err = ensembleCommand(args[1:], os.Stdout)
	case "report":
		err = reportCommand(args[1:], os.Stdout, stderr)
	case "status":
		err = statusCommand(args[1:], os.Stdout, stderr)
	case "cue":
		err = cueCommand(args[1:])
	case "preflight":
		err = preflightCommand(args[1:])
	case "config":
		err = configCommand(args[1:])
	default:
		fmt.Fprintln(stderr, usage)
		return 2
	}
	if errors.Is(err, errNotApproved) {
		return 1
	}
	if err != nil {
		var named *loop.SignalError
		if signalled := loop.LastSignal(); signalled != nil && errors.Is(err, context.Canceled) && !errors.As(err, &named) {
			err = fmt.Errorf("%s: %w", signalled, err)
		}
		fmt.Fprintln(stderr, "km:", err)
		return 1
	}
	return 0
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
		base:       fs.String("base", "", "branch on the remote to start the worktree from (default main); not with -worktree"),
		task:       fs.String("task", "", "task text"),
		taskFile:   fs.String("task-file", "", "file holding the task text"),
		verify:     fs.String("verify", "", "command kapellmeister runs in the worktree before planning and after each implementation round; defaults to [test] verify in .kapellmeister/config.toml, then an executable .kapellmeister/verify"),
		rounds:     fs.Int("rounds", 0, "maximum implement/review/gate rounds (default [run] rounds, else 3)"),
		retries:    fs.Int("verify-retries", 0, "times to rerun a failing verify before counting it as failed (default [run] verify_retries, else 1)"),
		preflight:  fs.Duration("preflight-timeout", 0, "how long each chair gets to answer the readiness check; 0 skips it (default [run] preflight_timeout, else 4m)"),
		timeout:    fs.Duration("timeout", 0, "timeout for each agent step (default [run] timeout, else 60m)"),
		replyGrace: fs.Duration("reply-grace", 0, "how long to wait for a reply file after a chair goes idle, before nudging it (default [run] reply_grace, else 15m)"),
		caoURL:     fs.String("cao", "", "cao-server URL (default $CAO_API_BASE_URL, then [cao] url, else http://127.0.0.1:9889)"),
		sizing:     fs.String("sizing", "", "off, shadow or on: whether the size decider's answer picks the implement, review, gate and test tiers (default [sizing] mode, else shadow)"),
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
		out[role] = fs.String(role, "", role+" chair: an agent from km config, or provider:profile[:model], with optional @small|@medium|@large (default [chairs] "+role+" in "+loop.UserConfigPath(loop.Home())+")"+listHelp)
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

func resolveChairs(agents loop.Agents, specs map[string]string) (loop.Chairs, error) {
	var missing []string
	for _, role := range loop.ChairRoles {
		if strings.TrimSpace(specs[role]) == "" {
			missing = append(missing, role)
		}
	}
	if len(missing) > 0 {
		return loop.Chairs{}, fmt.Errorf("no chair for %s: name an agent in [agents] available (for example available = [\"claude\"]) or set [chairs] in %s", strings.Join(missing, ", "), loop.UserConfigPath(loop.Home()))
	}
	resolved := map[string]loop.Chair{}
	var reviewers []loop.Chair
	for _, role := range loop.AllRoles() {
		if role == "tester" && specs[role] == "" {
			continue
		}
		if role == "reviewer" {
			list := loop.ChairList(specs[role])
			for i, spec := range list {
				chair, err := agents.ResolveChair(spec)
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
		chair, err := agents.ResolveChair(specs[role])
		if err != nil {
			return loop.Chairs{}, fmt.Errorf("%s: %w", role, err)
		}
		resolved[role] = chair
	}
	chairs := loop.Chairs{Planner: resolved["planner"], Implementer: resolved["implementer"], Reviewers: reviewers, Gate: resolved["gate"], Classifier: resolved["classifier"]}
	if tester, ok := resolved["tester"]; ok {
		chairs.Tester = &tester
	}
	return chairs, nil
}

func runChairs(s loop.Settings, ensemble string, roles map[string]*string) (loop.Settings, loop.Chairs, error) {
	if ensemble != "" {
		var err error
		s, err = s.WithEnsemble(ensemble)
		if err != nil {
			return s, loop.Chairs{}, err
		}
	}
	chairs, err := resolveChairs(s.Defined, chairSpecs(s, roles))
	return s, chairs, err
}

var roleSteps = map[string]string{"planner": "plan", "implementer": "implement", "reviewer": "review", "gate": "gate", "classifier": "classify", "tester": "test"}

func checkPins(policy loop.Policy, specs map[string]string, chairs loop.Chairs) error {
	for _, role := range loop.AllRoles() {
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
	roles := chairFlags(fs, loop.AllRoles()...)
	ensemble := fs.String("ensemble", "", ensembleHelp)
	if err := fs.Parse(args); err != nil {
		return err
	}
	set := flagsSet(fs)
	if set["base"] {
		if set["worktree"] {
			return errors.New("-base and -worktree do not go together: -worktree plays on the checkout it is given")
		}
		if err := validBaseBranch(*c.base); err != nil {
			return err
		}
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
	baseBranch := ""
	if wt == "" {
		if *c.repo == "" {
			return errors.New("-repo or -worktree is required")
		}
		if err := fetchRepo(*c.repo); err != nil {
			return err
		}
		baseBranch = firstNonEmpty(*c.base, "main")
		commit, err := resolveBase(*c.repo, baseBranch)
		if err != nil {
			return err
		}
		if wt, err = addWorktree(*c.repo, commit); err != nil {
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
	s, chairs, members, err := lineUp(s, wt, *ensemble, roles)
	if err != nil {
		return err
	}
	seated, err := addressedChairs(wt, *ensemble, chairs, members)
	if err != nil {
		return err
	}
	verifyCmd, checkCmd, err := loop.ResolveHooks(wt, *c.verify)
	if err != nil {
		return err
	}
	policy := s.Policy
	runID := newRunID()
	runDir := filepath.Join(loop.StateDir(), "runs", runID)
	scratch, err := prepareScratch(loop.StateDir(), runID)
	if err != nil {
		return err
	}
	testerEnv := ""
	if chairs.Tester != nil {
		testerEnv, err = writeTesterEnv(scratch, s.CAOURL)
		if err != nil {
			return err
		}
	}
	ctx, stop := loop.SignalContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
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
	podiumChair, _ := policy.Chair("classify", chairs.Classifier, "", 0)
	podium, err := client.CreateSession(ctx, "km-"+runID, cao.Launch{Provider: podiumChair.Provider, Profile: podiumChair.Profile, Model: podiumChair.Model, WorkingDirectory: wt})
	if err != nil {
		return err
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		if err := client.DeleteSession(cleanupCtx, podium.SessionName); err != nil {
			fmt.Fprintf(os.Stderr, "km: deleting session %s: %v\n", podium.SessionName, err)
		}
	}()
	sum, err := runEnsemble(ctx, ensembleRun{
		RunID: runID, StateDir: runDir, Ensemble: *ensemble, BaseBranch: baseBranch, Meter: loop.NewLogMeter(),
		Config: loop.Config{
			ScratchDir: scratch, TesterEnvFile: testerEnv, CAOURL: s.CAOURL, StateDir: loop.StateDir(), RunID: runID, Task: text, Worktree: wt, Chairs: seated, GateWhen: s.GateWhen,
			Lessons: s.Lessons, LessonsTimeout: s.LessonsTimeout,
			LearnedBudget: s.LearnedBudget, BlockerPoll: s.BlockerPoll, MaxRounds: s.Rounds, StepTimeout: s.Timeout, ReplyGrace: s.ReplyGrace, VerifyCommand: verifyCmd, CheckCommand: checkCmd, Policy: &policy,
		},
		Watcher: loop.CAOWatcher{Client: client, Session: podium.SessionName},
		Stepper: loop.CAOStepper{Client: client, Session: podium.SessionName}, Verify: loop.Retrying(loop.CommandVerifier(verifyCmd), s.VerifyRetries), Progress: os.Stdout,
	})
	if err != nil {
		return err
	}
	return finishRun(os.Stdout, sum)
}

func preflightCommand(args []string) error {
	ctx, stop := loop.SignalContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()
	fs := flag.NewFlagSet("preflight", flag.ContinueOnError)
	worktree := fs.String("worktree", "", "git worktree to start the chairs in")
	agents := fs.String("agents", "", "agents to check (default [agents] available in the user config)")
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
	names := firstNonEmpty(*agents, strings.Join(s.Agents, ","))
	if names == "" {
		return fmt.Errorf("no agents to preflight: name an agent in [agents] available (for example available = [\"claude\"]) in %s, or pass -agents", loop.UserConfigPath(loop.Home()))
	}
	selected, err := s.Defined.Select(names)
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
		if err := ensureProfiles(ctx, client, &pinned, map[string][]loop.Chair{"plan": chairs}); err != nil {
			return err
		}
		for i, chair := range chairs {
			chairs[i], _ = pinned.At(chair, *tier)
		}
	}
	return preflight(ctx, client, wt, s.PreflightTimeout, chairs...)
}

func configCommand(args []string) error {
	fs := flag.NewFlagSet("config", flag.ContinueOnError)
	worktree := fs.String("worktree", ".", "repository whose .kapellmeister/config.toml to layer over "+loop.UserConfigPath(loop.Home()))
	caoURL := fs.String("cao", "", "cao-server URL (default configured CAO URL)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	s, err := loop.LoadSettings(loop.Home(), *worktree)
	if err != nil {
		return err
	}
	s.CAOURL = firstNonEmpty(*caoURL, s.CAOURL)
	if _, err := resolveChairs(s.Defined, s.Chairs); err != nil {
		return err
	}
	printSources(os.Stdout, s)
	fmt.Printf("cao    %s\nstate  %s\nrun    rounds %d, verify retries %d, step timeout %s, reply grace %s, preflight %s, blocker poll %s, lessons %s, learned budget %d bytes\n",
		s.CAOURL, loop.StateDir(), s.Rounds, s.VerifyRetries, s.Timeout, s.ReplyGrace, s.PreflightTimeout, s.BlockerPoll, lessonsSetting(s), s.LearnedBudget)
	printChairs(os.Stdout, s)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	unguarded, err := printConfigPermissions(ctx, os.Stdout, cao.NewClient(s.CAOURL), s)
	if err != nil {
		return err
	}
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
	if err := tw.Flush(); err != nil {
		return err
	}
	printPermissionNote(os.Stdout, unguarded)
	return nil
}

func lessonsSetting(s loop.Settings) string {
	if !s.Lessons {
		return "off"
	}
	return "on, limit " + s.LessonsTimeout.String()
}

func printChairs(w io.Writer, s loop.Settings) {
	var chairs []string
	for _, role := range loop.AllRoles() {
		if role == "tester" && s.Chairs[role] == "" {
			continue
		}
		chairs = append(chairs, role+"="+s.Chairs[role])
	}
	fmt.Fprintf(w, "chairs  %s gate_when=%s\n", strings.Join(chairs, " "), s.GateWhen)
}

func printEnsembles(w io.Writer, s loop.Settings) {
	for _, name := range sortedKeys(s.Ensembles) {
		ensemble := s.Ensembles[name]
		specs := ensemble.Chairs()
		var chairs []string
		for _, role := range loop.AllRoles() {
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
	steps := map[string][]loop.Chair{
		"plan": {s.Planner}, "implement": {s.Implementer}, "review": s.Reviewers, "gate": {s.Gate}, "classify": {s.Classifier},
	}
	if s.Tester != nil {
		steps["test"] = []loop.Chair{*s.Tester}
	}
	return steps
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
	owners := map[string]string{}
	for _, v := range variants {
		if owner, ok := owners[v.Name()]; ok && owner != v.Provider {
			return fmt.Errorf("effort profile %s is shared by providers %s and %s", v.Name(), owner, v.Provider)
		}
		owners[v.Name()] = v.Provider
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
	bases := map[string]cao.ProfilePermissions{}
	type pendingProfile struct{ name, provider, base, source string }
	var pending []pendingProfile
	for _, v := range variants {
		base, ok := bases[v.Base]
		if !ok {
			base, err = client.ProfilePermissions(ctx, v.Base)
			if err != nil {
				var httpErr *cao.HTTPError
				if errors.As(err, &httpErr) && httpErr.Status == http.StatusNotFound {
					return fmt.Errorf("profile %s is not installed in CAO; it is the base of effort profile %s; install it with cao install, or set effort = \"inherit\" under [tiers.%s] to use it as is", v.Base, v.Name(), v.Provider)
				}
				return fmt.Errorf("reading CAO profile %s: %w", v.Base, err)
			}
			bases[v.Base] = base
		}
		if have[v.Name()] {
			variant, err := client.ProfilePermissions(ctx, v.Name())
			if err != nil {
				return fmt.Errorf("reading CAO profile %s: %w", v.Name(), err)
			}
			if samePermissions(base, variant) {
				continue
			}
			if !strings.HasPrefix(variant.Description, "kapellmeister chair at ") {
				return fmt.Errorf("profile %s exists and was not generated by kapellmeister; km will not replace it; remove it, or set effort = \"inherit\"", v.Name())
			}
		}
		source, err := loop.EffortProfileSource(v.Provider, v.Base, v.Effort, loop.Permissions{PermissionMode: base.PermissionMode, AllowedTools: base.AllowedTools, CodexProfile: base.CodexProfile})
		if err != nil {
			return err
		}
		pending = append(pending, pendingProfile{v.Name(), v.Provider, v.Base, source})
	}
	if len(pending) == 0 {
		return nil
	}
	home, err := caoHome()
	if err != nil {
		return err
	}
	if err := proveCAOHome(ctx, client, home, pending[0].name); err != nil {
		return err
	}
	for _, v := range pending {
		if err := guardProfile(home, v.name, v.source); err != nil {
			return err
		}
	}
	for _, v := range pending {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		path := filepath.Join(dir, v.name+".md")
		if err := os.WriteFile(path, []byte(v.source), 0o644); err != nil {
			return err
		}
		if out, err := exec.CommandContext(ctx, "cao", "install", path, "--provider", v.provider).CombinedOutput(); err != nil {
			return fmt.Errorf("cao install %s: %w: %s", path, err, out)
		}
		if have[v.name] {
			fmt.Printf("→ reinstalled CAO profile %s: permissions now match %s\n", v.name, v.base)
		} else {
			fmt.Printf("→ installed CAO profile %s from %s\n", v.name, path)
		}
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
	unseated := 0
	for _, r := range results {
		unseated += r.Unseated
		for _, problem := range r.TeardownErrors {
			fmt.Fprintf(os.Stderr, "preflight teardown failed for %s\n", problem)
		}
	}
	if ctx.Err() != nil {
		fmt.Printf("preflight interrupted; took down %d session(s)\n", unseated)
		if cause := loop.SignalOf(ctx); cause != nil {
			return cause
		}
		return ctx.Err()
	}
	if err != nil {
		return err
	}
	var blocked []string
	for _, r := range results {
		d := (time.Duration(r.DurationMS) * time.Millisecond).Round(time.Second)
		if r.OK {
			fmt.Printf("  ok      %s (%s)%s\n", r.Chair, d, modelNote(r)+seatRetryNote(r))
			continue
		}
		fmt.Printf("  BLOCKED %s (%s): %s\n", r.Chair, d, firstNonEmpty(r.Blocker, r.Error)+seatRetryNote(r))
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

func seatRetryNote(r loop.PreflightResult) string {
	if r.SeatRetries == 0 {
		return ""
	}
	return fmt.Sprintf("; seat retried %d time(s) after %s", r.SeatRetries, r.SeatRetryError)
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
	Meter      loop.UsageMeter
	BaseBranch string
	Ensemble   string
	Watcher    loop.Watcher
	Command    string
	RunID      string
	StateDir   string
	Config     loop.Config
	Stepper    loop.Stepper
	Verify     loop.Verifier
	Progress   io.Writer
}

type ensembleSummary struct {
	loop.Result
	DurationMS int64  `json:"duration_ms"`
	DiffStat   string `json:"diff_stat"`
	Ensemble   string `json:"ensemble,omitempty"`
	Repository string `json:"repository,omitempty"`
	BaseBranch string `json:"base_branch,omitempty"`
	Base       string `json:"base"`
	Head       string `json:"head,omitempty"`
	HeadMoved  bool   `json:"head_moved,omitempty"`
}

type recordedLesson struct {
	RunID      string `json:"run_id"`
	Repository string `json:"repository"`
	Ensemble   string `json:"ensemble"`
	loop.Lesson
}

func runEnsemble(ctx context.Context, p ensembleRun) (sum ensembleSummary, runErr error) {
	base, err := gitOutput(p.Config.Worktree, "rev-parse", "HEAD")
	if err != nil {
		return ensembleSummary{}, err
	}
	if err := os.MkdirAll(p.StateDir, 0o755); err != nil {
		return ensembleSummary{}, err
	}
	telemetry, err := os.Create(filepath.Join(p.StateDir, "telemetry.jsonl"))
	if err != nil {
		return ensembleSummary{}, err
	}
	defer telemetry.Close()
	start := time.Now()
	origin, _ := gitOutput(p.Config.Worktree, "config", "--get", "remote.origin.url")
	repository := repositoryOf(origin)
	var res loop.Result
	resultWritten := false
	defer func() {
		caught := recover()
		if !resultWritten {
			reason := "run ended without a result"
			if runErr != nil {
				reason = runErr.Error()
			}
			if signalled := loop.SignalOf(ctx); signalled != nil {
				reason = signalled.Error() + ": " + reason
			} else if ctx.Err() != nil {
				reason = "interrupted: " + ctx.Err().Error() + ": " + reason
			}
			if caught != nil {
				reason = fmt.Sprintf("panic: %v", caught)
			}
			res.RunID, res.Worktree = p.RunID, p.Config.Worktree
			if res.RunDir == "" {
				res.RunDir = filepath.Join(p.Config.Worktree, loop.RunDirRel(p.RunID))
			}
			res.Outcome, res.Reason = loop.Failed, reason
			sum = ensembleSummary{Result: res, DurationMS: time.Since(start).Milliseconds(), Ensemble: p.Ensemble, Repository: repository, Base: base, BaseBranch: p.BaseBranch}
			if err := writeJSON(filepath.Join(p.StateDir, "result.json"), sum); err != nil {
				fmt.Fprintln(os.Stderr, "km: writing result.json:", err)
			}
		}
		if caught != nil {
			panic(caught)
		}
	}()
	if err := json.NewEncoder(telemetry).Encode(loop.Record{At: start.UTC(), RunID: p.RunID, Step: "start"}); err != nil {
		return ensembleSummary{}, err
	}
	if err := writeJSON(filepath.Join(p.StateDir, "config.json"), p.Config); err != nil {
		return ensembleSummary{}, err
	}
	fmt.Fprintf(p.Progress, "km %s %s\n  worktree  %s\n", firstNonEmpty(p.Command, "run"), p.RunID, p.Config.Worktree)
	if p.BaseBranch != "" {
		fmt.Fprintf(p.Progress, "  base      %s @ %s\n", p.BaseBranch, base)
	}
	fmt.Fprintf(p.Progress, "  state     %s\n", p.StateDir)
	eng := &loop.Engine{Meter: p.Meter, Watcher: p.Watcher, Config: p.Config, Stepper: p.Stepper, Verify: p.Verify, Records: telemetry, Progress: p.Progress}
	host, _ := os.Hostname()
	token, _ := processStart(os.Getpid())
	began := start.UTC()
	state := runState{RunID: p.RunID, Command: firstNonEmpty(p.Command, "run"), Ensemble: p.Ensemble, Repository: repository, Started: began, Updated: began, MaxRounds: p.Config.MaxRounds, PID: os.Getpid(), Host: host, ProcessStart: token, Playing: []loop.ActiveStep{}}
	warned := false
	publish := func() {
		if err := writeRunState(p.StateDir, state); err != nil && !warned {
			fmt.Fprintln(os.Stderr, "km: recording current state:", plain(err.Error()))
			warned = true
		}
	}
	publish()
	eng.Playing = func(round int, active []loop.ActiveStep) {
		state.Round, state.Playing, state.Updated = round, active, time.Now().UTC()
		publish()
	}
	res, err = eng.Run(ctx)
	if err != nil {
		return ensembleSummary{}, err
	}
	if err := copyDir(res.RunDir, filepath.Join(p.StateDir, "replies")); err != nil {
		fmt.Fprintln(os.Stderr, "km: copying replies:", err)
	}
	head, err := gitOutput(p.Config.Worktree, "rev-parse", "HEAD")
	if err != nil {
		return ensembleSummary{}, err
	}
	stat, err := gitDiffStat(p.Config.Worktree, base)
	if err != nil {
		return ensembleSummary{}, err
	}
	if res.Lessons != nil {
		lessons := make([]recordedLesson, 0, len(res.Proposed))
		for _, lesson := range res.Proposed {
			lessons = append(lessons, recordedLesson{RunID: p.RunID, Repository: repository, Ensemble: p.Ensemble, Lesson: lesson})
		}
		if err := writeJSON(filepath.Join(p.StateDir, "lessons.json"), lessons); err != nil {
			fmt.Fprintln(os.Stderr, "km: writing lessons.json:", err)
			res.Lessons = &loop.LessonsReport{Reason: "writing lessons.json: " + err.Error()}
		}
	}
	sum = ensembleSummary{Result: res, DurationMS: time.Since(start).Milliseconds(), DiffStat: stat, Ensemble: p.Ensemble, Repository: repository, Base: base, BaseBranch: p.BaseBranch}
	if head != base {
		sum.Head, sum.HeadMoved = head, true
	}
	if err := writeJSON(filepath.Join(p.StateDir, "result.json"), sum); err != nil {
		return sum, err
	}
	resultWritten = true
	return sum, nil
}

var scpRemote = regexp.MustCompile(`^(?:[^@/]+@)?([^:/@]+):([^@]+)$`)

func repositoryOf(remote string) string {
	var host, path string
	if u, err := url.Parse(remote); err == nil && u.Host != "" {
		host, path = u.Hostname(), u.Path
	} else if m := scpRemote.FindStringSubmatch(remote); m != nil && !strings.Contains(remote, "://") {
		host, path = m[1], m[2]
	}
	path = strings.TrimSuffix(strings.Trim(path, "/"), ".git")
	if host == "" || path == "" {
		return ""
	}
	return strings.ToLower(host) + "/" + path
}

func gitDiffStat(worktree, base string) (string, error) {
	dir, err := os.MkdirTemp("", "km-diff-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	indexPath, err := gitOutput(worktree, "rev-parse", "--path-format=absolute", "--git-path", "index")
	if err != nil {
		return "", err
	}
	index, err := os.ReadFile(indexPath)
	steps := [][]string{{"add", "--intent-to-add", "--all"}, {"diff", base, "--stat"}}
	if errors.Is(err, os.ErrNotExist) {
		steps = append([][]string{{"read-tree", "HEAD"}}, steps...)
	} else if err != nil {
		return "", err
	} else if err := os.WriteFile(filepath.Join(dir, "index"), index, 0600); err != nil {
		return "", err
	}
	var output []byte
	for _, args := range steps {
		cmd := exec.Command("git", append([]string{"-c", "core.splitIndex=false", "-C", worktree}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_INDEX_FILE="+filepath.Join(dir, "index"))
		output, err = cmd.CombinedOutput()
		if err != nil {
			return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, output)
		}
	}
	return string(output), nil
}

var errNotApproved = errors.New("run was not approved")

func finishRun(w io.Writer, sum ensembleSummary) error {
	printOutcome(w, sum)
	fmt.Fprint(w, sum.DiffStat)
	printHeadMoved(w, sum)
	if sum.Outcome != loop.Approved {
		return errNotApproved
	}
	return nil
}

func printHeadMoved(w io.Writer, sum ensembleSummary) {
	if !sum.HeadMoved {
		return
	}
	base, _ := gitOutput(sum.Worktree, "rev-parse", "--short", sum.Base)
	head, _ := gitOutput(sum.Worktree, "rev-parse", "--short", sum.Head)
	subject, _ := gitOutput(sum.Worktree, "log", "-1", "--format=%s", sum.Head)
	fmt.Fprintf(w, "  HEAD moved: %s → %s %q — the run's changes are in that commit\n", base, head, subject)
}

func gitOutput(worktree string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", worktree}, args...)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, &stderr)
	}
	return strings.TrimSpace(string(output)), nil
}

func prepareScratch(stateDir, runID string) (string, error) {
	scratch, err := filepath.Abs(filepath.Join(stateDir, "scratch", runID))
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(scratch, 0o755); err != nil {
		return "", err
	}
	inside, err := gitOutput(scratch, "rev-parse", "--is-inside-work-tree")
	if err == nil && inside == "true" {
		top, err := gitOutput(scratch, "rev-parse", "--show-toplevel")
		_ = os.RemoveAll(scratch)
		if err != nil {
			return "", err
		}
		return "", fmt.Errorf("scratch directory %s is inside the git work tree %s; point KAPELLMEISTER_STATE_DIR outside any repository", scratch, top)
	}
	return scratch, nil
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
	resolved, err := filepath.EvalSymlinks(wt)
	if err != nil {
		os.RemoveAll(wt)
		return "", err
	}
	wt = resolved
	if out, err := exec.Command("git", "-C", repo, "worktree", "add", "--detach", "--", wt, base).CombinedOutput(); err != nil {
		os.RemoveAll(wt)
		return "", fmt.Errorf("git worktree add: %w: %s", err, out)
	}
	return wt, nil
}

func newRunID() string {
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	return time.Now().UTC().Format("20060102-150405") + "-" + hex.EncodeToString(b)
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
		var data []byte
		if e.Name() == "lessons-reply.json" {
			text, err := loop.ReadLessonsReply(filepath.Join(src, e.Name()))
			if err != nil {
				continue
			}
			data = []byte(text)
		} else {
			data, err = os.ReadFile(filepath.Join(src, e.Name()))
			if err != nil {
				return err
			}
		}
		if err := os.WriteFile(filepath.Join(dst, e.Name()), data, 0o644); err != nil {
			return err
		}
	}
	return nil
}
