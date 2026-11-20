package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/jystringfellow/kapellmeister/internal/initrepo"
	"github.com/jystringfellow/kapellmeister/internal/loop"
	"github.com/jystringfellow/kapellmeister/internal/prove"
	"github.com/jystringfellow/kapellmeister/internal/tune"
)

var strictSetupFiles = []string{initrepo.StrictFile, initrepo.StrictReadyFile, loop.ConfigFile}

func tuneStrict(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	started := tuneClock()
	fs := flag.NewFlagSet("tune strict", flag.ContinueOnError)
	fs.SetOutput(stderr)
	worktree := fs.String("worktree", ".", "checkout whose strict to tune")
	guide := fs.Bool("guide", false, "print the procedure without starting a run")
	again := fs.Bool("again", false, "tune an already written strict")
	detach := fs.Bool("detach", false, "continue in a separate process and return its run id")
	asJSON := fs.Bool("json", false, "print the final summary as JSON; with -detach use km wait -json")
	timeout := fs.Duration("timeout", 0, "timeout for each agent step (default [run] timeout)")
	budget := fs.Duration("budget", 30*time.Minute, "time budget for the whole tune run; checked between steps")
	strictTimeout := fs.Duration("strict-timeout", 0, "timeout for each strict run inside tune proof, recorded as [test] strict_timeout (default [test] strict_timeout, then verify_timeout, then 10m)")
	caoURL := fs.String("cao", "", "CAO URL (default environment, then configuration)")
	launchPath := fs.String("detached-launch", "", "internal detached tune hand-off")
	roles := chairFlags(fs, "implementer", "reviewer")
	hideFlags(fs, "detached-launch")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("tune strict accepts flags only")
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
	if flagsSet(fs)["strict-timeout"] && *strictTimeout <= 0 {
		return errors.New("-strict-timeout must be positive")
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
		if err := loop.ValidRunID(launch.RunID); err != nil {
			return fmt.Errorf("detached launch %s: %w", *launchPath, err)
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
	if top, err = filepath.EvalSymlinks(top); err != nil {
		return err
	}
	if top != root {
		return errors.New("worktree must be the checkout's top level")
	}
	head, err := gitOutput(root, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return fmt.Errorf("worktree has no HEAD commit: %w", err)
	}
	verify, _, err := loop.ResolveHooks(root, "")
	if err != nil {
		return err
	}
	if verify == "" {
		return errors.New("no verify setup here; run km init first")
	}
	if script, err := os.ReadFile(filepath.Join(root, initrepo.ScriptFile)); err == nil && strings.TrimPrefix(verify, "./") == initrepo.ScriptFile && initrepo.IsStub(string(script)) {
		return errors.New("verify is not written yet: run km tune verify first; strict holds what verify had to leave out")
	}
	saved, err := loop.ResolveTest(root)
	if err != nil {
		return err
	}
	if !*again && saved.Strict != "" {
		return fmt.Errorf("strict %q is already written; pass -again to tune it anyway", saved.Strict)
	}
	if len(saved.Excluded) == 0 && !saved.VerifyTimeBounded {
		return errors.New("there is nothing for strict to hold: [[test.excluded]] is empty and verify is not time-bounded, so verify already runs what it was given; run km tune verify first if suites are missing from it, or write scripts/strict by hand and prove it with km prove -level strict")
	}
	limit := saved.StrictLimit(saved.Timeout(prove.DefaultTimeout))
	subject := tune.Strict{Excluded: saved.Excluded, TimeBounded: saved.VerifyTimeBounded}
	if flagsSet(fs)["strict-timeout"] {
		limit, subject.Timeout = *strictTimeout, *strictTimeout
	}
	if *guide {
		_, err := fmt.Fprintln(stdout, subject.Procedure())
		return err
	}
	setup, err := readTuneSetup(root, strictSetupFiles...)
	if err != nil {
		return err
	}
	const breaks = 2
	whole := time.Duration(2+breaks) * limit
	derived := whole + tuneChairAllowance
	sized := fmt.Sprintf("-budget %s holds a whole proof at the %s strict timeout: two clean runs and %d break runs can take %s, plus %s for the chairs", *budget, limit, breaks, whole, tuneChairAllowance)
	switch {
	case !flagsSet(fs)["budget"] && derived > tuneDerivedBudgetCeiling:
		return fmt.Errorf("a whole proof at the %s strict timeout needs a budget of %s, more than the %s km sets by itself: pass -budget to allow it, or -strict-timeout for a shorter limit", limit, derived, tuneDerivedBudgetCeiling)
	case !flagsSet(fs)["budget"]:
		*budget = max(*budget, derived)
		sized = fmt.Sprintf("budget %s: a whole proof at the %s strict timeout is two clean runs and %d break runs (%s), plus %s for the chairs; pass -budget to set it yourself", *budget, limit, breaks, whole, tuneChairAllowance)
	case *budget < derived:
		sized = fmt.Sprintf("-budget %s does not hold a whole proof at the %s strict timeout: two clean runs and %d break runs can take %s, plus %s for the chairs; -budget %s would", *budget, limit, breaks, whole, tuneChairAllowance, derived)
	}
	return conductTune(ctx, tuneConduct{
		command: "tune strict", args: args, set: flagsSet(fs), roles: roles, root: root, head: head, launchPath: *launchPath, launch: launch,
		detach: *detach, asJSON: *asJSON, timeout: *timeout, caoURL: *caoURL, budget: *budget, started: started, sized: sized,
		prepare: func(wt string) error {
			path := filepath.Join(wt, initrepo.StrictFile)
			if _, err := os.Lstat(path); !os.IsNotExist(err) {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return err
			}
			return os.WriteFile(path, []byte(initrepo.RenderStrictStub()), 0o755)
		},
		build: func(wt, runID, runDir, patches string, progress io.Writer) (string, *loop.TuneHooks, *tuneSummary, error) {
			hooks, summary, err := strictHooks(root, wt, head, runID, runDir, patches, setup, progress, limit, saved, subject)
			return tune.StrictTask(wt, patches, subject), hooks, summary, err
		},
	}, stdout, stderr)
}

func strictRefusal(wt string, saved, written loop.TestConfig, subject tune.Strict) string {
	if strings.TrimPrefix(written.Strict, "./") != initrepo.StrictFile {
		return "tune must configure [test] strict to use scripts/strict"
	}
	script, err := os.ReadFile(filepath.Join(wt, initrepo.StrictFile))
	if err != nil {
		return "scripts/strict cannot be read: " + err.Error()
	}
	if initrepo.IsStub(string(script)) {
		return "scripts/strict still has the unfinished marker: write its steps and remove the marker"
	}
	plain := func(t loop.TestConfig) loop.TestConfig {
		t.Strict, t.StrictReady, t.StrictTimeout = "", "", ""
		return t
	}
	if !reflect.DeepEqual(plain(saved), plain(written)) {
		return "only [test] strict, strict_ready and strict_timeout may change in the config: verify, its keys and [[test.excluded]] are not the strict tune's to change"
	}
	wantTimeout := saved.StrictTimeout
	if subject.Timeout > 0 {
		wantTimeout = subject.Timeout.String()
	}
	if written.StrictTimeout != wantTimeout {
		if subject.Timeout > 0 {
			return fmt.Sprintf("[test] strict_timeout must be %q: the caller passed -strict-timeout, and the limit stays recorded for later runs", wantTimeout)
		}
		return fmt.Sprintf("[test] strict_timeout must stay %q: only the caller sets it, with -strict-timeout", wantTimeout)
	}
	labels := tune.StepLabels(string(script))
	commands := tune.StepCommands(string(script))
	for _, entry := range saved.Excluded {
		if !slices.Contains(labels, entry.Suite) {
			return fmt.Sprintf("suite %q is excluded from verify and scripts/strict has no run step labelled %q: strict must run every excluded suite (by hand: %s)", entry.Suite, entry.Suite, entry.Run)
		}
		if recorded := strings.TrimSpace(entry.Run); recorded != "" && strings.TrimSpace(commands[entry.Suite]) != recorded {
			return fmt.Sprintf("the step labelled %q in scripts/strict runs %q, not the command recorded for that excluded suite, %q: run the recorded command, or have the caller change the entry's run", entry.Suite, commands[entry.Suite], recorded)
		}
	}
	network := subject.NeedProbe()
	switch {
	case len(network) > 0 && strings.TrimPrefix(written.StrictReady, "./") != initrepo.StrictReadyFile && (saved.StrictReady == "" || written.StrictReady != saved.StrictReady):
		return fmt.Sprintf("strict holds a suite that needs something a machine may lack (%s), so km requires a readiness probe: write scripts/strict-ready and set [test] strict_ready = \"scripts/strict-ready\"", strings.Join(network, ", "))
	case len(network) > 0:
		if fault := loop.ProbeFault(wt, written.StrictReady); fault != "" {
			return "the strict_ready probe km requires is broken: " + fault
		}
	case written.StrictReady != saved.StrictReady:
		return "km decides when strict needs a readiness probe, and this strict holds no suite excluded for network, a service, a credential or another machine: remove [test] strict_ready and scripts/strict-ready"
	}
	return ""
}

func strictHooks(root, wt, head, runID, runDir, patches string, setup map[string]tuneFile, progress io.Writer, limit time.Duration, saved loop.TestConfig, subject tune.Strict) (*loop.TuneHooks, *tuneSummary, error) {
	baseline, err := tuneTree(wt)
	if err != nil {
		return nil, nil, err
	}
	summary := &tuneSummary{LeftOut: []loop.TuneOmission{}, Breaks: []tuneBreak{}, Excluded: saved.Excluded, Result: prove.Result{Worktree: wt, Level: prove.LevelStrict, Status: "not_run", Reason: "no proof was run", Runs: []prove.RunResult{}, Breaks: []prove.BreakResult{}}}
	var proved *tuneProved
	currentRound := 0
	hooks := &loop.TuneHooks{
		Subject:     "strict",
		ReviewBrief: subject.ReviewBrief(),
		PatchDir:    patches,
		Changed: func() ([]string, error) {
			currentRound++
			now, err := tuneTree(wt)
			if err != nil {
				return nil, err
			}
			var changed []string
			for name := range now {
				if !slices.Contains(strictSetupFiles, name) && now[name] != baseline[name] {
					changed = append(changed, name)
				}
			}
			for name := range baseline {
				if _, kept := now[name]; !kept && !slices.Contains(strictSetupFiles, name) {
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
			summary.Command, summary.LeftOut = reply.Command, reply.LeftOut
			var recorded, digests []string
			for i, patch := range reply.Patches {
				path := filepath.Join(runDir, "breaks", fmt.Sprintf("%d-%d-%s.patch", round, i+1, patch.Kind))
				data, err := os.ReadFile(patch.File)
				if err != nil {
					return loop.TuneProof{}, err
				}
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					return loop.TuneProof{}, err
				}
				if err := os.WriteFile(path, data, 0o644); err != nil {
					return loop.TuneProof{}, err
				}
				recorded = append(recorded, path)
			}
			digests, err := tunePatchDigests(reply)
			if err != nil {
				return loop.TuneProof{}, err
			}
			var result prove.Result
			needsCaller := false
			written, err := loop.ResolveTest(wt)
			switch {
			case err != nil:
				result = prove.Result{Worktree: wt, Level: prove.LevelStrict, Status: "refused", ExitStatus: 10, Reason: err.Error()}
			default:
				if refusal := strictRefusal(wt, saved, written, subject); refusal != "" {
					result = prove.Result{Worktree: wt, Level: prove.LevelStrict, Command: written.Strict, Status: "refused", ExitStatus: 10, Reason: refusal}
					break
				}
				result = runTuneProof(ctx, prove.Options{Level: prove.LevelStrict, Worktree: wt, Breaks: recorded, Timeout: limit})
				if cleanRunTimedOut(result) {
					result.Reason += "; strict has no time target, so nothing is cut to fit: pass -strict-timeout with the time one strict run needs, and km sizes the budget from it"
					needsCaller = true
				}
				if result.Status == "proved" {
				ran:
					for _, entry := range saved.Excluded {
						for _, run := range result.Runs {
							if slices.ContainsFunc(run.Steps, func(step prove.Step) bool { return step.Label == entry.Suite }) {
								continue
							}
							result.Status, result.ExitStatus = "suite_not_run", 14
							result.Reason = fmt.Sprintf("excluded suite %q did not run in %s; strict must run every excluded suite", entry.Suite, run.Name)
							if len(run.Steps) == 0 {
								result.Reason = fmt.Sprintf("excluded suite %q did not run in %s: strict recorded no steps; its run wrapper must append each label it runs to the file named by $%s, as the wrapper km writes does", entry.Suite, run.Name, initrepo.StepsEnv)
							}
							break ran
						}
					}
				}
			}
			current, err := readTuneSetup(wt, strictSetupFiles...)
			if err != nil {
				return loop.TuneProof{}, err
			}
			summary.Result = result
			summary.Breaks = nil
			for i, patch := range result.Breaks {
				summary.Breaks = append(summary.Breaks, tuneBreak{BreakResult: patch, Kind: reply.Patches[i].Kind})
			}
			proofPath := filepath.Join(loop.RunDirRel(runID), fmt.Sprintf("prove-%d.txt", round))
			var report bytes.Buffer
			printProof(&report, result)
			fmt.Fprint(progress, report.String())
			if err := os.WriteFile(filepath.Join(wt, proofPath), report.Bytes(), 0o644); err != nil {
				return loop.TuneProof{Path: proofPath}, err
			}
			if result.Status == "proved" {
				proved = &tuneProved{round: round, setup: current, summary: *summary, patches: digests}
			}
			return loop.TuneProof{Proved: result.Status == "proved", Path: proofPath, Status: result.Status, Reason: result.Reason, NeedsCaller: needsCaller}, nil
		},
		Publish: func(round int) error {
			if proved == nil || proved.round != round {
				return errors.New("no proved setup for publication")
			}
			current, err := readTuneSetup(root, strictSetupFiles...)
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
				candidate, err := readTuneSetup(wt, strictSetupFiles...)
				if err != nil {
					return err
				}
				if !reflect.DeepEqual(candidate, proved.setup) {
					return errors.New("setup files changed after proof; checkout left untouched")
				}
			}
			for _, name := range strictSetupFiles {
				file := proved.setup[name]
				if !file.Exists {
					if name == initrepo.StrictReadyFile {
						continue
					}
					return fmt.Errorf("tune removed required setup file %s", name)
				}
				mode := file.Mode
				if name != loop.ConfigFile {
					mode = 0o755
				}
				path := filepath.Join(root, name)
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					return err
				}
				if err := writeAtomic(path, file.Body, mode); err != nil {
					return err
				}
			}
			*summary = proved.summary
			summary.Published = true
			return nil
		},
	}
	return hooks, summary, nil
}
