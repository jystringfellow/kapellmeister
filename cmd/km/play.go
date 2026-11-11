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

	"github.com/jystringfellow/kapellmeister/internal/cao"
	"github.com/jystringfellow/kapellmeister/internal/ensemble"
	"github.com/jystringfellow/kapellmeister/internal/loop"
)

func playCommand(args []string) error {
	fs := flag.NewFlagSet("play", flag.ContinueOnError)
	rounds := fs.Int("max-rounds", 0, "maximum implement/review/gate rounds (default [run] rounds, else 3)")
	verify := fs.String("verify", "", "verify command (default [test] verify, then an executable .kapellmeister/verify)")
	timeout := fs.Duration("timeout", 0, "how long each step gets (default [run] timeout, else 60m)")
	caoURL := fs.String("cao", "", "cao-server URL (default $CAO_API_BASE_URL, then [cao] url, else http://127.0.0.1:9889)")
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		args = append(append([]string{}, args[1:]...), args[0])
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: km play SESSION [-max-rounds N] [-verify CMD] [-timeout D] [-cao URL]")
	}
	set := flagsSet(fs)
	if set["max-rounds"] && *rounds < 1 {
		return errors.New("-max-rounds must be at least 1")
	}
	standing, err := findStanding(filepath.Join(loop.StateDir(), "ensembles"), fs.Arg(0))
	if err != nil {
		return err
	}
	defer func() {
		fmt.Printf("ensemble %s still standing; take it down with km ensemble down %s\n", standing.Session, standing.Session)
	}()
	text, err := taskText(standing.Task, "")
	if standing.TaskFile != "" {
		text, err = taskText("", standing.TaskFile)
	}
	if err != nil {
		return err
	}
	if unbriefed := standing.Unbriefed(); len(unbriefed) > 0 {
		return fmt.Errorf("chair(s) %s were not briefed when session %s was rehearsed; take it down with km ensemble down %s and km rehearse again; %s", strings.Join(unbriefed, ", "), standing.Session, standing.Session, reseatHints(standing.Session, unbriefed))
	}
	chairs, err := standing.RebuildChairs()
	if err != nil {
		return err
	}
	s, err := loop.LoadSettings(loop.Home(), standing.Worktree)
	if err != nil {
		return err
	}
	if set["max-rounds"] {
		s.Rounds = *rounds
	}
	if set["timeout"] {
		s.Timeout = *timeout
	}
	if set["cao"] {
		s.CAOURL = *caoURL
	}
	verifyCmd, checkCmd, err := loop.ResolveHooks(standing.Worktree, *verify)
	if err != nil {
		return err
	}
	runID := newRunID()
	runDir := filepath.Join(loop.StateDir(), "runs", runID)
	scratch, err := prepareScratch(loop.StateDir(), runID)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	client := cao.NewClient(s.CAOURL)
	missing, err := ensemble.MissingOccupants(ctx, client, standing)
	if err != nil {
		return err
	}
	if len(missing) > 0 {
		return fmt.Errorf("chair(s) %s have no occupant in session %s; take it down with km ensemble down %s and km rehearse again; %s", strings.Join(missing, ", "), standing.Session, standing.Session, reseatHints(standing.Session, missing))
	}
	printSources(os.Stdout, s)
	fmt.Printf("ensemble %s (session %s); sizing and tier escalation off\n", standing.Ensemble, standing.Session)
	sum, err := runEnsemble(ctx, ensembleRun{
		Command: "play", RunID: runID, StateDir: runDir, Ensemble: standing.Ensemble, Meter: loop.NewLogMeter(),
		Config: loop.Config{
			ScratchDir: scratch, RunID: runID, Task: text, Worktree: standing.Worktree, Chairs: chairs, GateWhen: firstNonEmpty(standing.GateWhen, s.GateWhen),
			Lessons: s.Lessons, LessonsTimeout: s.LessonsTimeout,
			BlockerPoll: s.BlockerPoll, MaxRounds: s.Rounds, StepTimeout: s.Timeout, ReplyGrace: s.ReplyGrace, VerifyCommand: verifyCmd, CheckCommand: checkCmd,
		},
		Watcher: loop.CAOWatcher{Client: client, Session: standing.Session},
		Stepper: ensemble.NewOccupantStepper(loop.CAOStepper{Client: client, Session: standing.Session}, standing),
		Verify:  loop.Retrying(loop.CommandVerifier(verifyCmd), s.VerifyRetries), Progress: os.Stdout,
	})
	if err != nil {
		return err
	}
	return finishRun(os.Stdout, sum)
}

func reseatHints(session string, addresses []string) string {
	commands := make([]string, 0, len(addresses))
	for _, address := range addresses {
		commands = append(commands, "`km reseat "+session+" "+address+"`")
	}
	return "or replace the occupant with " + strings.Join(commands, ", ")
}
