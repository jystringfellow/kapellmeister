package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
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
	timeout := flags.Duration("timeout", 10*time.Minute, "timeout for each verify run")
	asJSON := flags.Bool("json", false, "print runs, patches and verdict as JSON")
	var patches breakFlags
	flags.Var(&patches, "break", "patch that must make verify fail (repeatable)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("prove accepts flags only")
	}
	if *timeout <= 0 {
		return errors.New("-timeout must be positive")
	}
	ctx, stop := loop.SignalContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()
	result := prove.Prove(ctx, prove.Options{Worktree: *worktree, Breaks: patches, Timeout: *timeout})
	if *asJSON {
		if err := json.NewEncoder(stdout).Encode(result); err != nil {
			return err
		}
	} else {
		printProof(stdout, result)
	}
	if result.ExitStatus != 0 {
		return &exitStatusError{code: result.ExitStatus}
	}
	return nil
}

func printProof(out io.Writer, r prove.Result) {
	for _, run := range r.Runs {
		fmt.Fprintf(out, "%s: %s (exit %d) in %s\n", run.Name, run.Status, run.ExitCode, time.Duration(run.DurationMS)*time.Millisecond)
		if run.Status != "passed" && run.Output != "" {
			fmt.Fprintln(out, run.Output)
		}
	}
	for _, b := range r.Breaks {
		fmt.Fprintf(out, "break %s: %s (exit %d) in %s\n", b.Patch, b.Status, b.ExitCode, time.Duration(b.DurationMS)*time.Millisecond)
		if b.Status == "breaks" {
			fmt.Fprintln(out, "verify failed, as required")
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
