package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/jystringfellow/kapellmeister/internal/cao"
	"github.com/jystringfellow/kapellmeister/internal/initrepo"
	"github.com/jystringfellow/kapellmeister/internal/loop"
	"github.com/jystringfellow/kapellmeister/internal/prove"
	"github.com/jystringfellow/kapellmeister/internal/tune"
)

const defaultTuneVerifyTimeout = 5 * time.Minute

var tuneClock = time.Now
var runTuneProof = prove.Prove

const tuneSubjects = "subjects:\n  verify  write and prove this repository's verify"

type tuneBreak struct {
	prove.BreakResult
	Kind string `json:"kind"`
}

type tuneKept struct {
	FromRound  int             `json:"from_round"`
	Round      int             `json:"round"`
	Comparison tune.Comparison `json:"comparison"`
	Compared   []string        `json:"compared"`
}

type tuneProved struct {
	round   int
	setup   map[string]tuneFile
	summary tuneSummary
	patches []string
}

type tuneSummary struct {
	ProofKept *tuneKept           `json:"proof_kept,omitempty"`
	Published bool                `json:"published"`
	Breaks    []tuneBreak         `json:"breaks"`
	Command   string              `json:"command"`
	LeftOut   []loop.TuneOmission `json:"left_out"`
	prove.Result
}

func tuneCommand(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] != "verify" {
		if len(args) > 0 {
			fmt.Fprintf(stderr, "km tune: unknown subject %q\n", args[0])
		}
		fmt.Fprintln(stderr, tuneSubjects)
		return &exitStatusError{code: 2}
	}
	return tuneVerify(nil, args[1:], stdout, stderr)
}

func tuneVerify(ctx context.Context, args []string, stdout, stderr io.Writer) (runErr error) {
	started := tuneClock()
	fs := flag.NewFlagSet("tune verify", flag.ContinueOnError)
	fs.SetOutput(stderr)
	worktree := fs.String("worktree", ".", "checkout whose verify to tune")
	guide := fs.Bool("guide", false, "print the procedure without starting a run")
	again := fs.Bool("again", false, "tune an already written verify")
	detach := fs.Bool("detach", false, "continue in a separate process and return its run id")
	asJSON := fs.Bool("json", false, "print the final summary as JSON; with -detach use km wait -json")
	timeout := fs.Duration("timeout", 0, "timeout for each agent step (default [run] timeout)")
	budget := fs.Duration("budget", 30*time.Minute, "time budget for the whole tune run; checked between steps")
	verifyTimeout := fs.Duration("verify-timeout", defaultTuneVerifyTimeout, "timeout for each verify run inside tune proof")
	caoURL := fs.String("cao", "", "CAO URL (default environment, then configuration)")
	launchPath := fs.String("detached-launch", "", "internal detached tune hand-off")
	roles := chairFlags(fs, "implementer", "reviewer")
	for _, arg := range args {
		if arg == "-guide" || arg == "--guide" || arg == "-guide=true" || arg == "--guide=true" {
			_, err := fmt.Fprintln(stdout, tune.VerifyProcedure)
			return err
		}
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *guide {
		_, err := fmt.Fprintln(stdout, tune.VerifyProcedure)
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("tune verify accepts flags only")
	}
	if *detach && *asJSON {
		return errors.New("-json and -detach do not go together; use km wait -json")
	}
	if flagsSet(fs)["timeout"] && *timeout <= 0 {
		return errors.New("-timeout must be positive")
	}
	if *budget <= 0 {
		return errors.New("-budget must be positive")
	}
	if *verifyTimeout <= 0 {
		return errors.New("-verify-timeout must be positive")
	}
	var launch detachedLaunchRecord
	if *launchPath != "" {
		data, err := os.ReadFile(*launchPath)
		if err != nil {
			return err
		}
		if err := json.Unmarshal(data, &launch); err != nil {
			return err
		}
		*worktree = launch.Checkout
	}
	root, err := worktreeDir(*worktree)
	if err != nil {
		return err
	}
	top, err := gitOutput(root, "rev-parse", "--show-toplevel")
	if err != nil {
		return fmt.Errorf("worktree is not a Git checkout with a HEAD commit: %w", err)
	}
	top, err = filepath.EvalSymlinks(top)
	if err != nil {
		return err
	}
	if top != root {
		return errors.New("worktree must be the checkout's top level")
	}
	head, err := gitOutput(root, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return fmt.Errorf("worktree has no HEAD commit: %w", err)
	}
	command, _, err := loop.ResolveHooks(root, "")
	if err != nil {
		return err
	}
	if command == "" {
		return errors.New("no verify setup here; run km init first")
	}
	setup, err := readTuneSetup(root)
	if err != nil {
		return err
	}
	if !*again && (strings.TrimPrefix(command, "./") != initrepo.ScriptFile || !initrepo.IsStub(string(setup[initrepo.ScriptFile].Body))) {
		return fmt.Errorf("verify %q is already written; pass -again to tune it anyway", command)
	}
	if strings.TrimPrefix(command, "./") == loop.VerifyHook {
		return fmt.Errorf("verify uses %s; move it to scripts/verify and update [test] verify before tuning", loop.VerifyHook)
	}
	s, err := loop.LoadSettings(loop.Home(), root)
	if err != nil {
		return err
	}
	set := flagsSet(fs)
	if set["cao"] {
		s.CAOURL = *caoURL
	}
	if set["timeout"] {
		s.Timeout = *timeout
	}
	chairs, err := tuneChairs(s, roles)
	if err != nil {
		return err
	}
	if ctx == nil {
		var stop context.CancelFunc
		ctx, stop = loop.SignalContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
		defer stop()
	}
	progress := stdout
	if *asJSON {
		progress = stderr
	}
	client := newClient(s.CAOURL, s, progress)
	if err := tunePreflight(ctx, client, chairs); err != nil {
		return err
	}
	if err := ensureProfilesTo(ctx, client, &s.Policy, map[string][]loop.Chair{"implement": {chairs.Implementer}, "review": {chairs.Reviewers[0]}}, progress); err != nil {
		return err
	}
	writingChair, _ := s.Policy.Chair("implement", chairs.Implementer, "", 0)
	readingChair, _ := s.Policy.Chair("review", chairs.Reviewers[0], "", 0)
	runID := newRunID()
	if launch.RunID != "" {
		runID = launch.RunID
	}
	runDir, err := filepath.Abs(filepath.Join(loop.StateDir(), "runs", runID))
	if err != nil {
		return err
	}
	if *detach && *launchPath == "" {
		handle, err := startDetached(ctx, args, runDir, detachedLaunchRecord{Command: "tune verify", RunID: runID, Worktree: root, Checkout: root}, nil)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(stdout, "run       %s\npid       %d\nworktree  %s\noutput    %s\n", handle.RunID, handle.PID, handle.Worktree, handle.Output)
		return err
	}
	if *launchPath != "" {
		state := initialRunState(runID, "tune verify", root, "", s.Rounds, time.Now())
		defer func() { recordDetachedFailure(ctx, runDir, launch, state, runErr) }()
		if err := writeRunState(runDir, state); err != nil {
			return err
		}
	}
	if err := tuneSeat(ctx, client, root, s.PreflightTimeout, progress, writingChair, readingChair); err != nil {
		return err
	}
	wt, err := addWorktree(root, head, s.WorktreeDir)
	if err != nil {
		return err
	}
	keep := true
	defer func() {
		if keep {
			fmt.Fprintf(stderr, "km: the worktree was kept at %s\n", wt)
		} else if _, err := gitOutput(root, "worktree", "remove", "--force", wt); err != nil {
			fmt.Fprintln(stderr, "km: removing tune worktree:", err)
		}
	}()
	if err := prove.OverlaySetup(root, wt); err != nil {
		return err
	}
	if err := loop.PrepareRunDir(wt, runID); err != nil {
		return err
	}
	scratch, err := prepareScratch(loop.StateDir(), runID)
	if err != nil {
		return err
	}
	patches := filepath.Join(scratch, "breaks")
	if err := os.MkdirAll(patches, 0755); err != nil {
		return err
	}
	hints, err := initrepo.Hints(wt)
	if err != nil {
		return err
	}
	var hintText strings.Builder
	for _, h := range hints {
		fmt.Fprintf(&hintText, "%v\n", h)
	}
	task := tune.Task(wt, hintText.String(), command, patches)
	hooks, summary, err := tuneHooks(root, wt, head, runID, runDir, patches, setup, progress, *verifyTimeout)
	if err != nil {
		return err
	}
	podiumChair := writingChair
	podium, err := client.CreateSession(ctx, "km-"+runID, cao.Launch{Provider: podiumChair.Provider, Profile: podiumChair.Profile, Model: podiumChair.Model, WorkingDirectory: wt})
	if err != nil {
		return err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		if err := client.DeleteSession(cleanup, podium.SessionName); err != nil {
			fmt.Fprintln(stderr, "km: deleting tune session:", err)
		}
	}()
	sum, err := runEnsemble(ctx, ensembleRun{
		Command: "tune verify", RunID: runID, StateDir: runDir, Meter: loop.NewLogMeter(), Tune: hooks, TuneSummary: summary, TuneCheckout: root,
		Config:  loop.Config{RunID: runID, Task: task, Worktree: wt, Chairs: chairs, MaxRounds: s.Rounds, Budget: *budget, BudgetStarted: started, StepTimeout: s.Timeout, ReplyGrace: s.ReplyGrace, Policy: &s.Policy, CAOURL: s.CAOURL, StateDir: loop.StateDir(), ScratchDir: scratch, BlockerPoll: s.BlockerPoll},
		Watcher: loop.CAOWatcher{Client: client, Session: podium.SessionName}, Stepper: loop.CAOStepper{Client: client, Session: podium.SessionName}, Progress: progress,
	})
	if err != nil {
		return err
	}
	keep = sum.Outcome != loop.Approved
	if *asJSON {
		if err := json.NewEncoder(stdout).Encode(sum); err != nil {
			return err
		}
		if keep {
			return &exitStatusError{code: 1}
		}
		return nil
	}
	return finishRunDetail(stdout, sum, false)
}

func tuneHooks(root, wt, head, runID, runDir, patches string, setup map[string]tuneFile, progress io.Writer, verifyTimeout time.Duration) (*loop.TuneHooks, *tuneSummary, error) {
	baseline, err := tuneTree(wt)
	if err != nil {
		return nil, nil, err
	}
	summary := &tuneSummary{LeftOut: []loop.TuneOmission{}, Breaks: []tuneBreak{}, Result: prove.Result{Worktree: wt, Status: "not_run", Reason: "no proof was run", Runs: []prove.RunResult{}, Breaks: []prove.BreakResult{}}}
	var proved *tuneProved
	currentRound := 0
	hooks := &loop.TuneHooks{
		PatchDir: patches,
		Changed: func() ([]string, error) {
			currentRound++
			now, err := tuneTree(wt)
			if err != nil {
				return nil, err
			}
			names := map[string]bool{}
			for name := range baseline {
				names[name] = true
			}
			for name := range now {
				names[name] = true
			}
			var changed []string
			for name := range names {
				if name == initrepo.ScriptFile || name == loop.ConfigFile {
					continue
				}
				if now[name] != baseline[name] {
					changed = append(changed, name)
				}
			}
			sort.Strings(changed)
			return changed, nil
		},
		Snapshot: func() (string, error) {
			state, err := tuneTree(wt)
			if err != nil {
				return "", err
			}
			b, err := json.Marshal(state)
			return string(b), err
		},
		Prove: func(ctx context.Context, reply loop.TuneReply, round int) (loop.TuneProof, error) {
			current, err := readTuneSetup(wt)
			if err != nil {
				return loop.TuneProof{}, err
			}
			summary.Command, summary.LeftOut = reply.Command, reply.LeftOut
			recordPatches := []string{}
			patchDigests := []string{}
			for i, patch := range reply.Patches {
				path := filepath.Join(runDir, "breaks", fmt.Sprintf("%d-%d-%s.patch", round, i+1, patch.Kind))
				data, err := os.ReadFile(patch.File)
				if err != nil {
					return loop.TuneProof{}, err
				}
				if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
					return loop.TuneProof{}, err
				}
				if err := os.WriteFile(path, data, 0644); err != nil {
					return loop.TuneProof{}, err
				}
				recordPatches = append(recordPatches, path)
				patchDigests = append(patchDigests, fmt.Sprintf("%s:%x", patch.Kind, sha256.Sum256(data)))
			}
			resolved, _, resolveErr := loop.ResolveHooks(wt, "")
			var result prove.Result
			switch {
			case resolveErr != nil:
				result = prove.Result{Worktree: wt, Status: "refused", ExitStatus: 10, Reason: resolveErr.Error()}
			case strings.TrimPrefix(resolved, "./") != initrepo.ScriptFile:
				result = prove.Result{Worktree: wt, Command: resolved, Status: "refused", ExitStatus: 10, Reason: "tune must configure [test] verify to use scripts/verify"}
			default:
				result = runTuneProof(ctx, prove.Options{Worktree: wt, Breaks: recordPatches, Timeout: verifyTimeout})
			}
			summary.ProofKept = nil
			summary.Result = result
			summary.Breaks = nil
			for i, patch := range result.Breaks {
				summary.Breaks = append(summary.Breaks, tuneBreak{BreakResult: patch, Kind: reply.Patches[i].Kind})
			}
			proofPath := filepath.Join(loop.RunDirRel(runID), fmt.Sprintf("prove-%d.txt", round))
			var report bytes.Buffer
			printProof(&report, result)
			fmt.Fprint(progress, report.String())
			if err := os.WriteFile(filepath.Join(wt, proofPath), report.Bytes(), 0644); err != nil {
				return loop.TuneProof{Path: proofPath}, err
			}
			if result.Status == "proved" {
				proved = &tuneProved{round: round, setup: current, summary: *summary, patches: patchDigests}
			}
			return loop.TuneProof{Proved: result.Status == "proved", Path: proofPath, Status: result.Status, Reason: result.Reason}, nil
		},
		Keep: func(reply loop.TuneReply, round int) (bool, string, string, error) {
			if proved == nil {
				return false, "", "", nil
			}
			current, err := readTuneSetup(wt)
			if err != nil {
				return false, "", "", err
			}
			if !reflect.DeepEqual(current[loop.ConfigFile], proved.setup[loop.ConfigFile]) || current[initrepo.ScriptFile].Mode != proved.setup[initrepo.ScriptFile].Mode || !current[initrepo.ScriptFile].Exists {
				return false, "", "", nil
			}
			comparison, same := tune.SameCommands(string(proved.setup[initrepo.ScriptFile].Body), string(current[initrepo.ScriptFile].Body))
			digests, err := tunePatchDigests(reply)
			if err != nil {
				return false, "", "", err
			}
			if !same || !reflect.DeepEqual(digests, proved.patches) {
				return false, "", "", nil
			}
			kept := &tuneKept{FromRound: proved.round, Round: round, Comparison: comparison, Compared: []string{"scripts/verify normalized commands (sha256)", "scripts/verify mode", ".kapellmeister/config.toml bytes and mode", "ordered break patch bytes and kinds (sha256)"}}
			data, err := json.MarshalIndent(kept, "", "  ")
			if err != nil {
				return false, "", "", err
			}
			path := filepath.Join(loop.RunDirRel(runID), fmt.Sprintf("prove-%d.txt", round))
			detail := fmt.Sprintf("proof kept from round %d: only comments, blank lines or step labels differ; commands, config, modes and break patches matched", proved.round)
			if err := os.WriteFile(filepath.Join(wt, path), append([]byte(detail+"\n"), data...), 0644); err != nil {
				return false, "", "", err
			}
			*summary = proved.summary
			summary.Command, summary.LeftOut, summary.ProofKept = reply.Command, reply.LeftOut, kept
			proved = &tuneProved{round: round, setup: current, summary: *summary, patches: digests}
			return true, path, detail, nil
		},
		Publish: func(round int) error {
			if proved == nil || proved.round != round {
				return errors.New("no proved setup for publication")
			}

			current, err := readTuneSetup(root)
			if err != nil {
				return err
			}
			if !reflect.DeepEqual(current, setup) {
				return errors.New("setup files changed while tuning; checkout left untouched")
			}
			now, err := gitOutput(root, "rev-parse", "HEAD")
			if err != nil {
				return err
			}
			if now != head {
				return errors.New("checkout HEAD moved while tuning; checkout left untouched")
			}
			if round == currentRound {
				candidate, err := readTuneSetup(wt)
				if err != nil {
					return err
				}
				if !reflect.DeepEqual(candidate, proved.setup) {
					return errors.New("setup files changed after proof; checkout left untouched")
				}
			}
			if err := publishTuneSetup(root, proved.setup); err != nil {
				return err
			}
			*summary = proved.summary
			summary.Published = true
			return nil
		},
	}
	return hooks, summary, nil
}

func tuneChairs(s loop.Settings, roles map[string]*string) (loop.Chairs, error) {
	specs := chairSpecs(s, roles)
	resolved := map[string]loop.Chair{}
	for _, role := range []string{"implementer", "reviewer"} {
		spec := specs[role]
		if spec == "" {
			return loop.Chairs{}, fmt.Errorf("no chair for %s: name an agent in [agents] available (for example available = [\"claude\"]) in %s", role, loop.UserConfigPath(loop.Home()))
		}
		if role == "reviewer" {
			spec = loop.ChairList(spec)[0]
		}
		chair, err := s.Defined.ResolveChair(spec)
		if err != nil {
			return loop.Chairs{}, fmt.Errorf("%s: %w", role, err)
		}
		if err := checkPin(s.Policy, role, role, spec, chair); err != nil {
			return loop.Chairs{}, err
		}
		resolved[role] = chair
	}
	return loop.Chairs{Implementer: resolved["implementer"], Reviewers: []loop.Chair{resolved["reviewer"]}}, nil
}

func tunePreflight(ctx context.Context, client *cao.Client, chairs loop.Chairs) error {
	installed, err := client.Profiles(ctx)
	if err != nil {
		return fmt.Errorf("cao-server is not reachable at %s; start it with cao-server", client.BaseURL)
	}
	have := map[string]bool{}
	for _, p := range installed {
		have[p.Name] = p.Loadable
	}
	check := func(chair loop.Chair) error {
		if !have[chair.Profile] {
			fix := "cao install"
			for _, agent := range loop.BuiltInAgents() {
				if agent.Chair.Profile == chair.Profile && agent.Chair.Provider == chair.Provider {
					fix = "km profiles install " + agent.Name
				}
			}
			return fmt.Errorf("profile %s is not installed or loadable in CAO; run %s", chair.Profile, fix)
		}
		if _, err := client.ProfilePermissions(ctx, chair.Profile); err != nil {
			return fmt.Errorf("profile %s cannot be read in CAO; repair it with cao install", chair.Profile)
		}
		return nil
	}
	for _, chair := range []loop.Chair{chairs.Implementer, chairs.Reviewers[0]} {
		if err := check(chair); err != nil {
			return err
		}
	}
	return nil
}

func tuneSeat(ctx context.Context, client *cao.Client, root string, timeout time.Duration, out io.Writer, chairs ...loop.Chair) error {
	if timeout == 0 {
		return nil
	}
	for _, chair := range loop.UniqueChairs(chairs...) {
		fmt.Fprintf(out, "→ preflight %s\n", chair)
		results, err := loop.Preflight(ctx, loop.CAOStepper{Client: client}, root, []loop.Chair{chair}, timeout)
		if err != nil {
			return err
		}
		r := results[0]
		if note := timeoutRetryNote(r, timeout); note != "" {
			fmt.Fprintf(out, "  %s%s\n", chair, note)
		}
		if !r.OK {
			if r.TimedOut {
				return errors.New(preflightTimeoutMessage(r, timeout))
			}
			fix := "check the provider's login and permissions"
			if r.Session != "" {
				fix = "resolve it with `tmux attach -t " + r.Session + "`, then detach with Ctrl-b d"
			}
			return fmt.Errorf("chair %s cannot answer preflight: %s; %s", chair, firstNonEmpty(r.Blocker, r.Error), fix)
		}
	}
	return nil
}

type tuneFile struct {
	Body   []byte
	Mode   os.FileMode
	Exists bool
}

func readTuneSetup(root string) (map[string]tuneFile, error) {
	result := map[string]tuneFile{}
	for _, name := range []string{initrepo.ScriptFile, loop.ConfigFile} {
		parent := filepath.Dir(filepath.Join(root, name))
		if info, err := os.Lstat(parent); err == nil && !info.IsDir() {
			return nil, fmt.Errorf("setup parent %s must be a directory", parent)
		} else if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
		path := filepath.Join(root, name)
		info, err := os.Lstat(path)
		if os.IsNotExist(err) {
			result[name] = tuneFile{}
			continue
		}
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("setup file %s must be a regular file", name)
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		result[name] = tuneFile{Body: body, Mode: info.Mode().Perm(), Exists: true}
	}
	return result, nil
}

func publishTuneSetup(root string, setup map[string]tuneFile) error {
	for _, name := range []string{initrepo.ScriptFile, loop.ConfigFile} {
		file := setup[name]
		if !file.Exists {
			return fmt.Errorf("tune removed required setup file %s", name)
		}
	}
	for _, name := range []string{initrepo.ScriptFile, loop.ConfigFile} {
		file := setup[name]
		mode := file.Mode
		if name == initrepo.ScriptFile {
			mode = 0755
		}
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			return err
		}
		if err := writeAtomic(path, file.Body, mode); err != nil {
			return err
		}
	}
	return nil
}

func tuneTree(root string) (map[string]string, error) {
	files, err := exec.Command("git", "-C", root, "ls-files", "-z", "--cached", "--others", "--exclude-standard").Output()
	if err != nil {
		return nil, err
	}
	state := map[string]string{}
	for _, name := range strings.Split(string(files), "\x00") {
		if name == "" {
			continue
		}
		path := filepath.Join(root, name)
		info, err := os.Lstat(path)
		if os.IsNotExist(err) {
			state[name] = "deleted"
			continue
		}
		if err != nil {
			return nil, err
		}
		var body []byte
		if info.Mode()&os.ModeSymlink != 0 {
			link, err := os.Readlink(path)
			if err != nil {
				return nil, err
			}
			body = []byte(link)
		} else if info.Mode().IsRegular() {
			body, err = os.ReadFile(path)
			if err != nil {
				return nil, err
			}
		}
		state[name] = fmt.Sprintf("%s:%x", info.Mode(), sha256.Sum256(body))
	}
	head, err := gitOutput(root, "rev-parse", "HEAD")
	if err != nil {
		return nil, err
	}
	state[".git/HEAD"] = head
	index, err := gitOutput(root, "diff", "--cached", "--binary", "HEAD")
	if err != nil {
		return nil, err
	}
	state[".git/index"] = index
	return state, nil
}

func printTune(w io.Writer, s ensembleSummary, detail bool) {
	t := s.Tune
	fmt.Fprintf(w, "  tune verify: %s\n", t.Command)
	if len(t.LeftOut) == 0 {
		fmt.Fprintln(w, "  left out: nothing")
	}
	for _, omit := range t.LeftOut {
		fmt.Fprintf(w, "  left out: %s — %s\n", omit.What, omit.Why)
	}
	proof := t.Result
	proof.Breaks = nil
	for _, patch := range t.Breaks {
		proof.Breaks = append(proof.Breaks, patch.BreakResult)
	}
	printProofDetail(w, proof, detail)
	if t.ProofKept != nil {
		fmt.Fprintf(w, "  proof kept from round %d: only comments, blank lines or step labels differ; commands, config, modes and break patches matched\n", t.ProofKept.FromRound)
	}
	if t.Published || s.Outcome == loop.Approved {
		fmt.Fprintf(w, "  left uncommitted in %s\n", s.Worktree)
	}
}

func tunePatchDigests(reply loop.TuneReply) ([]string, error) {
	digests := make([]string, 0, len(reply.Patches))
	for _, p := range reply.Patches {
		data, err := os.ReadFile(p.File)
		if err != nil {
			return nil, err
		}
		digests = append(digests, fmt.Sprintf("%s:%x", p.Kind, sha256.Sum256(data)))
	}
	return digests, nil
}
