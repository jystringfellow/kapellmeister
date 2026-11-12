package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"github.com/jystringfellow/kapellmeister/internal/cao"
	"github.com/jystringfellow/kapellmeister/internal/ensemble"
	"github.com/jystringfellow/kapellmeister/internal/loop"
)

func parseInterspersed(fs *flag.FlagSet, args []string) ([]string, error) {
	var positionals []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		if fs.NArg() == 0 {
			return positionals, nil
		}
		positionals = append(positionals, fs.Arg(0))
		args = fs.Args()[1:]
	}
}

func reseatCommand(args []string) error {
	fs := flag.NewFlagSet("reseat", flag.ContinueOnError)
	agent := fs.String("agent", "", "replacement agent from km config, or provider:profile[:model], with optional @tier")
	timeout := fs.Duration("timeout", 0, "how long the new occupant gets to close its briefing (default [run] preflight_timeout, else 4m)")
	caoURL := fs.String("cao", "", "cao-server URL (default $CAO_API_BASE_URL, then [cao] url, else http://127.0.0.1:9889)")
	positionals, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if len(positionals) != 2 {
		return errors.New("usage: km reseat SESSION ADDRESS [-agent SPEC] [-timeout D] [-cao URL]")
	}
	dir := filepath.Join(loop.StateDir(), "ensembles")
	standing, err := findStanding(dir, positionals[0])
	if err != nil {
		return err
	}
	index, err := standing.ChairIndex(positionals[1])
	if err != nil {
		return err
	}
	text, err := taskText(standing.Task, "")
	if standing.TaskFile != "" {
		text, err = taskText("", standing.TaskFile)
	}
	if err != nil {
		return err
	}
	s, err := loop.LoadSettings(loop.Home(), standing.Worktree)
	if err != nil {
		return err
	}
	if *timeout == 0 {
		*timeout = s.PreflightTimeout
	}
	ctx, stop := loop.SignalContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()
	client := cao.NewClient(firstNonEmpty(*caoURL, s.CAOURL))
	m := standing.Members()[index]
	if flagsSet(fs)["agent"] {
		m, err = prepareMember(ctx, client, s.Policy, s.Defined, *agent, m)
		if err != nil {
			return fmt.Errorf("chair %s: %w", positionals[1], err)
		}
	}
	km, err := os.Executable()
	if err != nil {
		return err
	}
	result, err := (ensemble.Reseat{Client: client, Standing: standing, Address: positionals[1], Chair: m.Chair, Timeout: *timeout, KM: km, Task: text, StateDir: dir}).Run(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("reseated %s: %s → %s\n", result.Address, firstNonEmpty(result.Old, "none"), result.New)
	return nil
}
