package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/jystringfellow/kapellmeister/internal/loop"
	"github.com/jystringfellow/kapellmeister/internal/prove"
)

type breakFlags []string

func (b *breakFlags) String() string         { return fmt.Sprint([]string(*b)) }
func (b *breakFlags) Set(value string) error { *b = append(*b, value); return nil }

func proveAt(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("prove", flag.ContinueOnError)
	flags.SetOutput(stderr)
	worktree := flags.String("worktree", ".", "checkout whose verify to prove")
	timeout := flags.Duration("timeout", 0, "timeout for each run of the command (default [test] verify_timeout, then 10m; at -level strict, [test] strict_timeout first)")
	level := flags.String("level", prove.LevelVerify, "level to prove: verify ([test] verify) or strict ([test] strict)")
	asJSON := flags.Bool("json", false, "print runs, patches and verdict as JSON")
	var patches breakFlags
	flags.Var(&patches, "break", "patch that must make verify fail (repeatable)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("prove accepts flags only")
	}
	if *level != prove.LevelVerify && *level != prove.LevelStrict {
		return fmt.Errorf("-level must be %s or %s, not %q", prove.LevelVerify, prove.LevelStrict, *level)
	}
	if flagsSet(flags)["timeout"] && *timeout <= 0 {
		return errors.New("-timeout must be positive")
	}
	ctx, stop := loop.SignalContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()
	result := prove.Prove(ctx, prove.Options{Level: *level, Worktree: *worktree, Breaks: patches, Timeout: *timeout})
	if result.Status == "timeout" {
		limit := fmt.Sprint(time.Duration(result.TimeoutMS) * time.Millisecond)
		if slices.ContainsFunc(result.Breaks, func(b prove.BreakResult) bool { return b.Status == "timeout" }) {
			limit = fmt.Sprintf("%s, the break timeout", time.Duration(result.BreakTimeoutMS)*time.Millisecond)
		}
		key := "verify"
		if result.Level == prove.LevelStrict {
			key = "strict"
		}
		result.Reason += fmt.Sprintf(" at %s; pass -timeout D, or set [test] %s_timeout, to give %s longer", limit, key, key)
	}
	if *asJSON {
		if err := json.NewEncoder(stdout).Encode(result); err != nil {
			return err
		}
	} else {
		for _, entry := range result.Excluded {
			fmt.Fprintf(stdout, "verify excludes %s\n", entry)
		}
		printProof(stdout, result)
	}
	if result.ExitStatus != 0 {
		return &exitStatusError{code: result.ExitStatus}
	}
	return nil
}

func printProof(out io.Writer, r prove.Result) {
	printProofDetail(out, r, true)
}

func printKnownFailures(out io.Writer, indent string, failing, passing []string) {
	if len(failing) > 0 {
		fmt.Fprintf(out, "%sknown failures (verify passes while these fail): %s\n", indent, strings.Join(failing, ", "))
	}
	for _, label := range passing {
		fmt.Fprintf(out, "%sknown failure %q now passes: remove it from [test] verify_known_failures and make it a run step\n", indent, label)
	}
}

func printProofDetail(out io.Writer, r prove.Result, detail bool) {
	for _, run := range r.Runs {
		fmt.Fprintf(out, "%s: %s (exit %d) in %s\n", run.Name, run.Status, run.ExitCode, time.Duration(run.DurationMS)*time.Millisecond)
		if detail && run.Status != "passed" && run.Output != "" {
			fmt.Fprintln(out, run.Output)
		}
	}
	for _, flaky := range r.Flaky {
		fmt.Fprintf(out, "flaky step: %s (failed in %s, passed in %s)\n", flaky.Step, flaky.FailedIn, flaky.PassedIn)
	}
	if r.BreakTimeoutMS > r.TimeoutMS {
		fmt.Fprintf(out, "break timeout raised from %s to %s: twice the slowest clean run\n", time.Duration(r.TimeoutMS)*time.Millisecond, time.Duration(r.BreakTimeoutMS)*time.Millisecond)
	}
	printKnownFailures(out, "", r.KnownFailures, nil)
	for _, label := range r.KnownFailuresFlaky {
		fmt.Fprintf(out, "known failure %q is flaky: it failed in one clean run and passed in the other\n", label)
	}
	for _, b := range r.Breaks {
		fmt.Fprintf(out, "break %s: %s", b.Patch, b.Status)
		if b.ExitCode != nil && b.DurationMS != nil {
			fmt.Fprintf(out, " (exit %d) in %s", *b.ExitCode, time.Duration(*b.DurationMS)*time.Millisecond)
		}
		fmt.Fprintln(out)
		if !detail {
			continue
		}
		if b.Status == "breaks" {
			level := r.Level
			if level == "" {
				level = prove.LevelVerify
			}
			fmt.Fprintf(out, "%s failed, as required\n", level)
		}
		if b.Reason != "" {
			fmt.Fprintln(out, b.Reason)
		}
		if b.Output != "" {
			fmt.Fprintln(out, b.Output)
		}
	}
	fmt.Fprintf(out, "%s: %s\n", r.Status, r.Reason)
}
