package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/jystringfellow/kapellmeister/internal/initrepo"
	"github.com/jystringfellow/kapellmeister/internal/loop"
)

const initSavedTimeoutCeiling = 8 * time.Hour

func initAt(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("init", flag.ContinueOnError)
	flags.SetOutput(stderr)
	worktree := flags.String("worktree", ".", "checkout to initialize")
	dryRun := flags.Bool("dry-run", false, "print exact drafts without writing or running")
	asJSON := flags.Bool("json", false, "print setup and verification as JSON")
	verify := flags.String("verify", "", "explicit verify command")
	check := flags.String("check", "", "targeted test command with {args}")
	noRun := flags.Bool("no-run", false, "draft files without running verify")
	timeout := flags.Duration("timeout", 0, "verify timeout (default [test] verify_timeout, then 10m)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("init accepts flags only")
	}
	if flagsSet(flags)["timeout"] && *timeout <= 0 {
		return errors.New("-timeout must be positive")
	}
	if *timeout == 0 {
		*timeout = 10 * time.Minute
		if test, err := loop.ResolveTest(*worktree); err == nil {
			*timeout = test.Timeout(*timeout)
		}
		if *timeout > initSavedTimeoutCeiling {
			return fmt.Errorf("[test] verify_timeout is %s, more than the %s km init uses by itself: pass -timeout to allow it", *timeout, initSavedTimeoutCeiling)
		}
	}
	if set := flagsSet(flags); set["verify"] && *verify == "" || set["check"] && *check == "" {
		return errors.New("-verify and -check must not be empty")
	}
	result, err := initrepo.Init(context.Background(), initrepo.Options{Worktree: *worktree, DryRun: *dryRun, NoRun: *noRun, Verify: *verify, Check: *check, Timeout: *timeout})
	if *asJSON {
		if encodeErr := json.NewEncoder(stdout).Encode(result); encodeErr != nil {
			return encodeErr
		}
	} else {
		printInit(stdout, result, *timeout)
	}
	if err != nil {
		return errNotApproved
	}
	return nil
}

func printInit(out io.Writer, r initrepo.Result, timeout time.Duration) {
	workflows := 0
	for _, hint := range r.Hints {
		if hint.Kind == "ci" && strings.HasPrefix(hint.File, ".github/workflows/") {
			workflows++
		}
	}
	summarised := false
	for _, hint := range r.Hints {
		if workflows > 3 && hint.Kind == "ci" && strings.HasPrefix(hint.File, ".github/workflows/") {
			if !summarised {
				fmt.Fprintf(out, "hint: ci %d workflow files in .github/workflows\n", workflows)
				summarised = true
			}
			continue
		}
		fmt.Fprintf(out, "hint: %s %s", hint.Kind, hint.File)
		if hint.Note != "" {
			fmt.Fprintf(out, " %s", hint.Note)
		}
		fmt.Fprintln(out)
	}
	if len(r.Hints) > 0 {
		fmt.Fprintln(out, "hints are not written to any file")
	}
	allKept := true
	for _, file := range r.Files {
		status := file.Status
		if status == "would_write" {
			status = "would write"
		}
		fmt.Fprintf(out, "%s: %s", file.Path, status)
		if file.Reason != "" {
			fmt.Fprintf(out, ", %s", file.Reason)
		}
		fmt.Fprintln(out)
		if file.Content != "" {
			fmt.Fprint(out, file.Content)
		}
		if file.Status != "kept" {
			allKept = false
		}
	}
	if allKept {
		fmt.Fprintln(out, "nothing written: every file already exists")
	}
	duration := (time.Duration(r.Verify.DurationMS) * time.Millisecond).Round(time.Millisecond)
	switch r.Verify.Status {
	case "unfinished":
		fmt.Fprintf(out, "verify: unfinished, %s\n", r.Verify.Output)
	case "passed":
		fmt.Fprintf(out, "verify: %s passed in %s\n", r.Verify.Command, duration)
	case "failed":
		fmt.Fprintf(out, "verify: %s failed (exit %d) in %s\n%s", r.Verify.Command, r.Verify.ExitCode, duration, r.Verify.Output)
	case "timed_out":
		fmt.Fprintf(out, "verify: %s timed out after %s; pass -timeout D, or set [test] verify_timeout, to give verify longer\n%s", r.Verify.Command, timeout, r.Verify.Output)
	default:
		fmt.Fprintf(out, "verify: skipped (%s)\n", r.Verify.Output)
	}
	if r.Verify.Status == "failed" || r.Verify.Status == "timed_out" {
		fmt.Fprintln(out)
	}
	if r.Error != "" {
		fmt.Fprintln(out, r.Error)
	}
	for _, next := range r.Next {
		fmt.Fprintln(out, "next:", next)
	}
}
