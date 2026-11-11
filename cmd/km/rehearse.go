package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/jystringfellow/kapellmeister/internal/cao"
	"github.com/jystringfellow/kapellmeister/internal/ensemble"
	"github.com/jystringfellow/kapellmeister/internal/loop"
)

func rehearseCommand(args []string) error {
	fs := flag.NewFlagSet("rehearse", flag.ContinueOnError)
	worktree := fs.String("worktree", "", "git worktree the chairs start in")
	task := fs.String("task", "", "task text")
	taskFile := fs.String("task-file", "", "file holding the task text")
	name := fs.String("ensemble", "", ensembleHelp)
	roles := chairFlags(fs, loop.AllRoles()...)
	verify := fs.String("verify", "", "baseline verify command (default [test] verify, then an executable .kapellmeister/verify)")
	timeout := fs.Duration("timeout", 0, "how long each chair gets to close its briefing (default [run] preflight_timeout, else 4m)")
	caoURL := fs.String("cao", "", "cao-server URL (default $CAO_API_BASE_URL, then [cao] url, else http://127.0.0.1:9889)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *worktree == "" {
		return errors.New("-worktree is required")
	}
	text, err := taskText(*task, *taskFile)
	if err != nil {
		return err
	}
	wt, err := worktreeDir(*worktree)
	if err != nil {
		return err
	}
	s, err := loop.LoadSettings(loop.Home(), wt)
	if err != nil {
		return err
	}
	s, chairs, members, err := lineUp(s, wt, *name, roles)
	if err != nil {
		return err
	}
	if *timeout == 0 {
		*timeout = s.PreflightTimeout
	}
	taskPath := ""
	if *taskFile != "" {
		taskPath, err = filepath.Abs(*taskFile)
		if err != nil {
			return err
		}
	}
	verifyCmd, _, err := loop.ResolveHooks(wt, *verify)
	if err != nil {
		return err
	}
	ctx, stop := loop.SignalContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if verifyCmd == "" {
		fmt.Println("no verify command resolved; baseline skipped")
	} else {
		result, err := loop.Retrying(loop.CommandVerifier(verifyCmd), s.VerifyRetries)(ctx, wt)
		if err != nil {
			return fmt.Errorf("baseline verify: %w", err)
		}
		if result.ExitCode != 0 {
			lines := strings.Split(strings.TrimSpace(result.Output), "\n")
			if len(lines) > 20 {
				lines = lines[len(lines)-20:]
			}
			return fmt.Errorf("baseline verify failed with exit code %d (after %d attempt(s)): %s", result.ExitCode, result.Attempts, strings.Join(lines, "\n"))
		}
		fmt.Printf("baseline verify passed: %s\n", verifyCmd)
	}
	km, err := os.Executable()
	if err != nil {
		return err
	}
	policy := s.Policy
	sizeMembers(policy, members)
	client := cao.NewClient(firstNonEmpty(*caoURL, s.CAOURL))
	if err := ensureProfiles(ctx, client, &policy, stepChairs(chairs)); err != nil {
		return err
	}
	ensembleName := firstNonEmpty(*name, defaultEnsembleName)
	fmt.Printf("→ rehearse %s: %d chair(s)\n", ensembleName, len(members))
	report, err := (ensemble.Rehearsal{Client: client, Ensemble: ensembleName, Worktree: wt, Members: members, Timeout: *timeout, KM: km, Task: text, TaskFile: taskPath, GateWhen: s.GateWhen, StateDir: filepath.Join(loop.StateDir(), "ensembles")}).Run(ctx)
	if err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(filepath.Dir(report.Queue), "report.json"), report); err != nil {
		return err
	}
	printChairTable(os.Stdout, report, "BRIEF")
	hint := ""
	if report.Session != "" {
		hint = fmt.Sprintf("take the ensemble down with `km ensemble down %s`", report.Session)
		fmt.Printf("session %s standing; attach with `tmux attach -t %s`; %s\n", report.Session, report.Session, hint)
	}
	if !report.OK {
		failed := failedChairs(report)
		if hint != "" {
			hint = "; " + hint
		}
		return fmt.Errorf("rehearsal failed for %d chair(s): %s%s", len(failed), strings.Join(failed, ", "), hint)
	}
	return nil
}
