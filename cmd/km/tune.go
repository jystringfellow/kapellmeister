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
	"slices"
	"sort"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/jystringfellow/kapellmeister/internal/cao"
	"github.com/jystringfellow/kapellmeister/internal/initrepo"
	"github.com/jystringfellow/kapellmeister/internal/loop"
	"github.com/jystringfellow/kapellmeister/internal/prove"
	"github.com/jystringfellow/kapellmeister/internal/tune"
)

const defaultTuneVerifyTimeout = 5 * time.Minute
const tuneChairAllowance = 10 * time.Minute
const tuneDerivedBudgetCeiling = 8 * time.Hour

func tuneBudget(limit time.Duration, required tune.Requirements) (breaks int, whole, budget time.Duration) {
	breaks = max(2, len(required.Suites))
	whole = time.Duration(3+breaks) * limit
	return breaks, whole, whole + tuneChairAllowance
}

const tuneSummaryWidth = 80

var tuneClock = time.Now
var runTuneProof = prove.Prove
var runTuneOffline = prove.Offline

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

type tuneRaise struct {
	FromMS int64 `json:"from_ms"`
	ToMS   int64 `json:"to_ms"`
}

type tuneSummary struct {
	ProofKept     *tuneKept           `json:"proof_kept,omitempty"`
	Published     bool                `json:"published"`
	Breaks        []tuneBreak         `json:"breaks"`
	Command       string              `json:"command"`
	LeftOut       []loop.TuneOmission `json:"left_out"`
	Required      []string            `json:"required,omitempty"`
	TimeBounded   bool                `json:"time_bounded,omitempty"`
	TimeoutRaised *tuneRaise          `json:"timeout_raised,omitempty"`
	Excluded      []loop.Exclusion    `json:"excluded,omitempty"`
	NoNetwork     string              `json:"no_network_method,omitempty"`
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
	verifyTimeout := fs.Duration("verify-timeout", 0, "timeout for each verify run inside tune proof (default [test] verify_timeout, then 5m)")
	caoURL := fs.String("cao", "", "CAO URL (default environment, then configuration)")
	launchPath := fs.String("detached-launch", "", "internal detached tune hand-off")
	var require breakFlags
	fs.Var(&require, "require", "suite verify must run, named as its run step in scripts/verify (repeatable; adds to [test] verify_required)")
	timeBounded := fs.Bool("time-bounded", false, "aim verify at two minutes and allow cutting what does not fit (also [test] verify_time_bounded); by default nothing is left out for time")
	roles := chairFlags(fs, "implementer", "reviewer")
	hideFlags(fs, "detached-launch")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := loop.ValidSuites(require); err != nil {
		return fmt.Errorf("-require: %w", err)
	}
	if *guide {
		_, err := fmt.Fprintln(stdout, tune.Require(nil, require, *timeBounded).Procedure())
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
	if flagsSet(fs)["verify-timeout"] && *verifyTimeout <= 0 {
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
	test, err := loop.ResolveTest(root)
	if err != nil {
		return err
	}
	required := tune.Require(test.VerifyRequired, require, test.VerifyTimeBounded || *timeBounded)
	callerLimit := flagsSet(fs)["verify-timeout"] || test.VerifyTimeout != ""
	if *verifyTimeout == 0 {
		*verifyTimeout = test.Timeout(defaultTuneVerifyTimeout)
	}
	breaks, whole, derived := tuneBudget(*verifyTimeout, required)
	sized := fmt.Sprintf("-budget %s holds a whole proof at the %s verify timeout: two clean runs, %d break runs and one run without network can take %s, plus %s for the chairs", *budget, *verifyTimeout, breaks, whole, tuneChairAllowance)
	switch {
	case !flagsSet(fs)["budget"] && derived > tuneDerivedBudgetCeiling:
		return fmt.Errorf("a whole proof at the %s verify timeout needs a budget of %s, more than the %s km sets by itself: pass -budget to allow it, or -verify-timeout for a shorter limit", *verifyTimeout, derived, tuneDerivedBudgetCeiling)
	case !flagsSet(fs)["budget"]:
		*budget = max(*budget, derived)
		sized = fmt.Sprintf("budget %s: a whole proof at the %s verify timeout is two clean runs, %d break runs and one run without network (%s), plus %s for the chairs; pass -budget to set it yourself", *budget, *verifyTimeout, breaks, whole, tuneChairAllowance)
	case *budget < derived:
		sized = fmt.Sprintf("-budget %s does not hold a whole proof at the %s verify timeout: two clean runs, %d break runs and one run without network can take %s, plus %s for the chairs; -budget %s would", *budget, *verifyTimeout, breaks, whole, tuneChairAllowance, derived)
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
	fmt.Fprintln(progress, sized)
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
	if err := tuneSeat(ctx, client, root, s.PreflightTimeout, s.PreflightRetryWait, progress, writingChair, readingChair); err != nil {
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
	task := tune.Task(wt, hintText.String(), command, patches, required)
	hooks, summary, err := tuneHooks(root, wt, head, runID, runDir, patches, setup, progress, *verifyTimeout, callerLimit, started.Add(*budget), required)
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

func tuneHooks(root, wt, head, runID, runDir, patches string, setup map[string]tuneFile, progress io.Writer, verifyTimeout time.Duration, callerLimit bool, deadline time.Time, required tune.Requirements) (*loop.TuneHooks, *tuneSummary, error) {
	baseline, err := tuneTree(wt)
	if err != nil {
		return nil, nil, err
	}
	summary := &tuneSummary{Required: required.Suites, TimeBounded: required.TimeBounded, LeftOut: []loop.TuneOmission{}, Breaks: []tuneBreak{}, Result: prove.Result{Worktree: wt, Status: "not_run", Reason: "no proof was run", Runs: []prove.RunResult{}, Breaks: []prove.BreakResult{}}}
	var proved *tuneProved
	currentRound := 0
	var savedExcluded []loop.Exclusion
	if saved, err := loop.ResolveTest(wt); err == nil {
		savedExcluded = saved.Excluded
	}
	summary.Excluded = slices.Clone(savedExcluded)
	savedTimeout := ""
	if saved, err := loop.ResolveTest(wt); err == nil {
		savedTimeout = saved.VerifyTimeout
	}
	hooks := &loop.TuneHooks{
		PatchDir:    patches,
		ReviewRules: required.ReviewRules(),
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
			if config, err := os.ReadFile(filepath.Join(wt, loop.ConfigFile)); err == nil {
				state[loop.ConfigFile] = fmt.Sprintf("%x", sha256.Sum256([]byte(loop.WithoutExcluded(loop.WithoutVerifyTimeout(string(config))))))
			}
			b, err := json.Marshal(state)
			return string(b), err
		},
		Prove: func(ctx context.Context, reply loop.TuneReply, round int) (loop.TuneProof, error) {
			summary.Command, summary.LeftOut, summary.NoNetwork = reply.Command, reply.LeftOut, ""
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
			unrecorded := ""
			written, writtenErr := loop.ResolveTest(wt)
			if writtenErr == nil {
				unrecorded = unrecordedRequirement(required, written)
			}
			if unrecorded == "" && writtenErr == nil && written.VerifyTimeout != savedTimeout {
				unrecorded = "[test] verify_timeout must not be set by the tune: km saves the limit it proved at"
				if savedTimeout != "" {
					unrecorded = fmt.Sprintf("[test] verify_timeout must stay %q: only the caller and km change it", savedTimeout)
				}
			}
			script, _ := os.ReadFile(filepath.Join(wt, initrepo.ScriptFile))
			if unrecorded == "" && writtenErr == nil && !slices.Equal(written.Excluded, savedExcluded) {
				unrecorded = "[[test.excluded]] is written by km from its own run without network, not by the tune: leave it as it was"
			}
			for _, entry := range savedExcluded {
				if unrecorded == "" && slices.Contains(tune.StepLabels(string(script)), entry.Suite) {
					unrecorded = fmt.Sprintf("suite %q is excluded from verify (%s: %s): remove its step from scripts/verify; only the caller removes an entry from [[test.excluded]]", entry.Suite, entry.Reason, entry.Evidence)
				}
			}
			var result prove.Result
			needsCaller := false
			switch {
			case resolveErr != nil:
				result = prove.Result{Worktree: wt, Status: "refused", ExitStatus: 10, Reason: resolveErr.Error()}
			case strings.TrimPrefix(resolved, "./") != initrepo.ScriptFile:
				result = prove.Result{Worktree: wt, Command: resolved, Status: "refused", ExitStatus: 10, Reason: "tune must configure [test] verify to use scripts/verify"}
			case unrecorded != "":
				result = prove.Result{Worktree: wt, Command: resolved, Status: "refused", ExitStatus: 10, Reason: unrecorded}
			default:
				if later := written.Timeout(prove.DefaultTimeout); verifyTimeout > later {
					fmt.Fprintf(progress, "verify is proved at %s, longer than the %s a later km prove would use, so it is saved as [test] verify_timeout\n", verifyTimeout, later)
					if err := loop.SetVerifyTimeout(wt, verifyTimeout); err != nil {
						return loop.TuneProof{}, err
					}
					savedTimeout = verifyTimeout.String()
				}
				result = runTuneProof(ctx, prove.Options{Worktree: wt, Breaks: recordPatches, Timeout: verifyTimeout})
				for why := required.KeepsSlowTests(); why != "" && cleanRunTimedOut(result); {
					if !callerLimit {
						result.Reason += fmt.Sprintf("; %s, and %s is only the default limit: pass -verify-timeout with the time one verify run needs, and km sizes the budget from it and saves the limit", why, verifyTimeout)
						needsCaller = true
						break
					}
					_, _, derived := tuneBudget(2*verifyTimeout, required)
					if left, whole := max(deadline.Sub(tuneClock()), 0).Round(time.Second), time.Duration(3+len(recordPatches))*2*verifyTimeout; whole > left {
						result.Reason += fmt.Sprintf("; %s, but the timeout is not raised to %s: a proof at that limit can take %s (two clean runs, %d break runs and one run without network) and %s of the budget is left: pass -verify-timeout %s, which sizes the budget at %s, and pass -budget for more", why, 2*verifyTimeout, whole, len(recordPatches), left, 2*verifyTimeout, derived)
						needsCaller = true
						break
					}
					fmt.Fprintf(progress, "verify timed out at %s; %s, so the timeout is raised to %s and saved as [test] verify_timeout\n", verifyTimeout, why, 2*verifyTimeout)
					if summary.TimeoutRaised == nil {
						summary.TimeoutRaised = &tuneRaise{FromMS: verifyTimeout.Milliseconds()}
					}
					verifyTimeout *= 2
					summary.TimeoutRaised.ToMS = verifyTimeout.Milliseconds()
					if err := loop.SetVerifyTimeout(wt, verifyTimeout); err != nil {
						return loop.TuneProof{}, err
					}
					savedTimeout = verifyTimeout.String()
					result = runTuneProof(ctx, prove.Options{Worktree: wt, Breaks: recordPatches, Timeout: verifyTimeout})
				}
				if result.Status == "proved" {
					limit := verifyTimeout
					var labels []string
					for i, run := range result.Runs {
						limit = max(limit, 2*time.Duration(run.DurationMS)*time.Millisecond)
						for _, step := range run.Steps {
							if i == 0 {
								labels = append(labels, step.Label)
							}
						}
					}
					offline := runTuneOffline(ctx, prove.OfflineOptions{Worktree: wt, Timeout: limit, Labels: labels})
					summary.NoNetwork = offline.Method
					commands := tune.StepCommands(string(script))
					var reasons []string
					added := false
					for _, failed := range offline.Failed {
						if slices.Contains(written.VerifyRequired, failed.Suite) {
							reasons = append(reasons, fmt.Sprintf("required suite %q passes with network and fails on a clean copy without it (%s): %s; a required suite cannot be excluded: make it pass without network, or stop requiring it", failed.Suite, offline.Method, failed.Evidence))
							continue
						}
						savedExcluded = append(savedExcluded, loop.Exclusion{Suite: failed.Suite, Reason: "network", Method: offline.Method, Evidence: failed.Evidence, Run: commands[failed.Suite]})
						added = true
						reasons = append(reasons, fmt.Sprintf("suite %q passes with network and fails on a clean copy without it (%s): %s; km recorded it in [[test.excluded]]: remove its step from scripts/verify", failed.Suite, offline.Method, failed.Evidence))
					}
					if added {
						if err := loop.SetExcluded(wt, savedExcluded); err != nil {
							return loop.TuneProof{}, err
						}
						summary.Excluded = slices.Clone(savedExcluded)
					}
					if offline.Partial {
						reasons = append(reasons, fmt.Sprintf("the run wrapper in scripts/verify does not skip the steps named in the file $%s points at, as the wrapper km init writes does, so the steps after that one were not tried without network", initrepo.SkipEnv))
					}
					switch offline.Status {
					case "passed":
						fmt.Fprintf(progress, "verify passes on a clean copy without network (%s)\n", offline.Method)
					case "failed":
						result.Status, result.ExitStatus, result.Reason = "needs_network", 18, strings.Join(reasons, "; ")
					default:
						advice := map[string]string{
							"flaky":   "; nothing was recorded for it: make the step reliable, or leave it out as flaky with this evidence",
							"failure": "; nothing was recorded for it: it passed in the proof, so make the step reliable, or leave it out with this evidence",
						}[offline.Status]
						reasons = append(reasons, fmt.Sprintf("without network (%s): %s%s", offline.Method, offline.Reason, advice))
						result.Status, result.ExitStatus, result.Reason = "no_network_"+offline.Status, 18, strings.Join(reasons, "; ")
					}
				}
			}
			current, err := readTuneSetup(wt)
			if err != nil {
				return loop.TuneProof{}, err
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
			return loop.TuneProof{Proved: result.Status == "proved", Path: proofPath, Status: result.Status, Reason: result.Reason, NeedsCaller: needsCaller}, nil
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
			if written, err := loop.ResolveTest(wt); err != nil || len(written.VerifyRequired) > 0 && !slices.Equal(tune.StepLabels(string(proved.setup[initrepo.ScriptFile].Body)), tune.StepLabels(string(current[initrepo.ScriptFile].Body))) {
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

func cleanRunTimedOut(result prove.Result) bool {
	return result.Status == "timeout" && len(result.Runs) > 0 && result.Runs[len(result.Runs)-1].Status == "timed_out"
}

func unrecordedRequirement(required tune.Requirements, written loop.TestConfig) string {
	for _, suite := range required.Suites {
		if !slices.Contains(written.VerifyRequired, suite) {
			return fmt.Sprintf("[test] verify_required must list %q: a required suite stays recorded for later runs", suite)
		}
	}
	if required.TimeBounded && !written.VerifyTimeBounded {
		return "[test] verify_time_bounded must be true: a time-bounded verify stays recorded for later runs"
	}
	if !required.TimeBounded && written.VerifyTimeBounded {
		return "[test] verify_time_bounded must not be set: only the caller allows a time-bounded verify, with -time-bounded"
	}
	return ""
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

func tuneSeat(ctx context.Context, client *cao.Client, root string, timeout, retryWait time.Duration, out io.Writer, chairs ...loop.Chair) error {
	if timeout == 0 {
		return nil
	}
	for _, chair := range loop.UniqueChairs(chairs...) {
		fmt.Fprintf(out, "→ preflight %s\n", chair)
		results, err := loop.PreflightRetryingAfter(ctx, loop.CAOStepper{Client: client}, root, []loop.Chair{chair}, timeout, retryWait)
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
	proof := t.Result
	proof.Breaks = nil
	for _, patch := range t.Breaks {
		proof.Breaks = append(proof.Breaks, patch.BreakResult)
	}
	printProofDetail(w, proof, detail)
	if len(t.Required) > 0 {
		fmt.Fprintf(w, "  required suites: %s\n", strings.Join(t.Required, ", "))
	}
	if t.TimeBounded {
		fmt.Fprintln(w, "  time-bounded: tests that do not fit the time target may be left out")
	}
	for _, entry := range t.Excluded {
		fmt.Fprintf(w, "  excluded: %s\n", entry)
	}
	if t.NoNetwork != "" {
		fmt.Fprintf(w, "  network cut by: %s\n", t.NoNetwork)
	}
	if raised := t.TimeoutRaised; raised != nil {
		to := time.Duration(raised.ToMS) * time.Millisecond
		fmt.Fprintf(w, "  verify timeout raised from %s to %s and saved as [test] verify_timeout\n", time.Duration(raised.FromMS)*time.Millisecond, to)
	}
	if t.ProofKept != nil {
		fmt.Fprintf(w, "  proof kept from round %d: only comments, blank lines or step labels differ; commands, config, modes and break patches matched\n", t.ProofKept.FromRound)
	}
	if t.Published || s.Outcome == loop.Approved {
		fmt.Fprintf(w, "  left uncommitted in %s\n", s.Worktree)
	}
}

func printLeftOut(w io.Writer, omissions []loop.TuneOmission) {
	if len(omissions) == 0 {
		fmt.Fprintln(w, "  left out: nothing")
		return
	}
	checks := "checks"
	if len(omissions) == 1 {
		checks = "check"
	}
	fmt.Fprintf(w, "  left out: %d %s\n", len(omissions), checks)
	for _, omit := range omissions {
		for _, line := range wrapWords(omit.What, "    ", tuneSummaryWidth) {
			fmt.Fprintln(w, line)
		}
		for _, line := range wrapWords(omit.Why, "      ", tuneSummaryWidth) {
			fmt.Fprintln(w, line)
		}
	}
}

func wrapWords(text, indent string, width int) []string {
	var lines []string
	line := indent
	for _, word := range strings.Fields(text) {
		if line != indent && utf8.RuneCountInString(line)+1+utf8.RuneCountInString(word) > width {
			lines = append(lines, line)
			line = indent
		}
		if line != indent {
			line += " "
		}
		line += word
	}
	if line != indent {
		lines = append(lines, line)
	}
	return lines
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
