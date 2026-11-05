package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/jystringfellow/kapellmeister/internal/cao"
	"github.com/jystringfellow/kapellmeister/internal/ensemble"
	"github.com/jystringfellow/kapellmeister/internal/loop"
)

const defaultEnsembleName = "default"

func auditionCommand(args []string) error {
	fs := flag.NewFlagSet("audition", flag.ContinueOnError)
	worktree := fs.String("worktree", "", "git worktree the chairs start in")
	name := fs.String("ensemble", "", "named ensemble from [ensembles.NAME] in "+loop.UserConfigPath(loop.Home())+"; role flags override it (default: your [chairs])")
	roles := chairFlags(fs, loop.ChairRoles...)
	timeout := fs.Duration("timeout", 0, "how long each chair gets to close its cue (default [run] preflight_timeout, else 4m)")
	keep := fs.Bool("keep", false, "leave the ensemble's CAO session running after the audition")
	caoURL := fs.String("cao", "", "cao-server URL (default $CAO_API_BASE_URL, then [cao] url, else http://127.0.0.1:9889)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *worktree == "" {
		return errors.New("-worktree is required")
	}
	wt, err := filepath.EvalSymlinks(*worktree)
	if err != nil {
		return err
	}
	s, err := loop.LoadSettings(loop.Home(), wt)
	if err != nil {
		return err
	}
	s, chairs, err := runChairs(s, *name, roles)
	if err != nil {
		return err
	}
	if err := checkPins(s.Policy, chairSpecs(s, roles), chairs); err != nil {
		return err
	}
	if *timeout == 0 {
		*timeout = s.PreflightTimeout
	}
	km, err := os.Executable()
	if err != nil {
		return err
	}
	policy := s.Policy
	members := ensemble.MembersOf(chairs)
	for i, m := range members {
		members[i].Chair, _ = policy.Chair(roleSteps[m.Part], m.Chair, "", 0)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	client := cao.NewClient(firstNonEmpty(*caoURL, s.CAOURL))
	if err := ensureProfiles(ctx, client, &policy, stepChairs(chairs)); err != nil {
		return err
	}
	ensembleName := firstNonEmpty(*name, defaultEnsembleName)
	fmt.Printf("→ audition %s: %d chair(s)\n", ensembleName, len(members))
	report, err := ensemble.Audition{Client: client, Ensemble: ensembleName, Worktree: wt, Members: members, Timeout: *timeout, KM: km, Keep: *keep}.Run(ctx)
	if err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(filepath.Dir(report.Queue), "report.json"), report); err != nil {
		return err
	}
	printAudition(os.Stdout, report, *keep)
	if !report.OK {
		var failed []string
		for _, c := range report.Chairs {
			if !c.OK {
				failed = append(failed, c.Address)
			}
		}
		return fmt.Errorf("audition failed for %d chair(s): %s", len(failed), strings.Join(failed, ", "))
	}
	return nil
}

func printAudition(w *os.File, r ensemble.Report, kept bool) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "CHAIR\tAGENT\tBOOT\tCUE\tRESULT")
	for _, c := range r.Chairs {
		result := "ok"
		if !c.OK {
			result = c.Problem
		}
		cue := "-"
		if c.CueMS > 0 {
			cue = millis(c.CueMS)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", c.Address, c.Chair, millis(c.BootMS), cue, result)
	}
	tw.Flush()
	if kept && r.Session != "" {
		fmt.Fprintf(w, "session %s kept; attach with `tmux attach -t %s`\n", r.Session, r.Session)
	}
}

func millis(ms int64) string {
	return (time.Duration(ms) * time.Millisecond).Round(100 * time.Millisecond).String()
}

func cueCommand(args []string) error {
	if len(args) == 0 || args[0] != "done" {
		return errors.New("usage: km cue done -queue FILE CUE [NOTE]")
	}
	fs := flag.NewFlagSet("cue done", flag.ContinueOnError)
	queue := fs.String("queue", "", "the cue queue file named in the cue")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if *queue == "" || fs.NArg() == 0 {
		return errors.New("usage: km cue done -queue FILE CUE [NOTE]")
	}
	if err := ensemble.Close(*queue, fs.Arg(0), os.Getenv("CAO_TERMINAL_ID"), strings.Join(fs.Args()[1:], " ")); err != nil {
		return err
	}
	fmt.Println("cue", fs.Arg(0), "closed")
	return nil
}
