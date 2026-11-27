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
	if unpinned := standing.Unpinned(); len(unpinned) > 0 {
		return unpinnedError(standing.Session, unpinned)
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
	s, err := loop.LoadSettings(loop.Home(), standing.Worktree)
	if err != nil {
		return err
	}
	if set["max-rounds"] {
		s.Rounds = *rounds
		s.RoundsConfigured = true
	}
	name, err := ensemble.ScoreName(standing.Ensemble, loop.Home(), standing.Worktree)
	if err != nil {
		return err
	}
	s, err = loadSelectedScore(s, standing.Worktree, name)
	if err != nil {
		return err
	}
	chairs, err := standing.RebuildChairsFor(s.Score.Score)
	if err != nil {
		if !errors.Is(err, ensemble.ErrNoRecordedBinding) {
			spec, found, findErr := ensemble.Find(standing.Ensemble, loop.Home(), standing.Worktree, s.Defined)
			if findErr == nil && found && spec.Fits(s.Score.Score) != nil {
				return fmt.Errorf("%s: %w", spec.Path, err)
			}
		}
		return err
	}
	if err := standing.CheckReseated(s.Score.Score); err != nil {
		return err
	}
	if s.Score.Score.Source != "" {
		s.GateWhen = ""
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
	tested, err := loop.ResolveTest(standing.Worktree)
	if err != nil {
		return err
	}
	printExcluded(os.Stdout, standing.Worktree)
	runID := newRunID()
	runDir := filepath.Join(loop.StateDir(), "runs", runID)
	if err := loop.LowDisk(s.MinFreeMB, standing.Worktree, loop.StateDir()); err != nil {
		return err
	}
	scratch, err := prepareScratch(loop.StateDir(), runID)
	if err != nil {
		return err
	}
	approved := false
	defer func() { settleScratch(os.Stderr, loop.StateDir(), runID, approved) }()
	ctx, stop := loop.SignalContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()
	client := newClient(s.CAOURL, s, os.Stdout)
	missing, err := ensemble.MissingOccupants(ctx, client, standing)
	if err != nil {
		return err
	}
	if len(missing) > 0 {
		return fmt.Errorf("chair(s) %s have no occupant in session %s; take it down with km ensemble down %s and km rehearse again; %s", strings.Join(missing, ", "), standing.Session, standing.Session, reseatHints(standing.Session, missing))
	}
	printSources(os.Stdout, s)
	printScoreFiles(os.Stdout, s.Score.Files, chairs)
	fmt.Printf("ensemble %s (session %s); sizing and tier escalation off\n", standing.Ensemble, standing.Session)
	var members []ensemble.Member
	terminals := map[string]string{}
	for _, chair := range standing.Chairs {
		members = append(members, ensemble.Member{Section: chair.Section, Name: chair.Name, Part: chair.Part, Chair: chair.Binding})
		terminals[chair.Address] = chair.TerminalID
	}
	km, err := os.Executable()
	if err != nil {
		return err
	}
	stepper := ensemble.NewOccupantStepper(loop.CAOStepper{Client: client, Session: standing.Session}, standing)
	reseater := &ensemble.ReviewReseater{
		Client: client, Standing: standing, Stepper: stepper, Timeout: s.PreflightTimeout,
		KM: km, Task: text, StateDir: filepath.Join(loop.StateDir(), "ensembles"),
	}
	sum, err := runEnsemble(ctx, ensembleRun{
		Reseater: reseater,
		Score:    s.Score.Score, ScoreFiles: s.Score.Files, Members: members, Agents: s.Defined, CAOURL: client.BaseURL, Terminals: terminals,
		Command: "play", RunID: runID, StateDir: runDir, Ensemble: standing.Ensemble, EnsembleSource: ensembleSourceOf(s, standing.Worktree, standing.Ensemble), Meter: loop.NewLogMeter(),
		Config: loop.Config{
			ScratchDir: scratch, MinFreeMB: s.MinFreeMB, CAOURL: s.CAOURL, StateDir: loop.StateDir(), RunID: runID, Task: text, Worktree: standing.Worktree, Chairs: chairs, GateWhen: firstNonEmpty(standing.GateWhen, s.GateWhen),
			Lessons: s.Lessons, LessonsTimeout: s.LessonsTimeout,
			LearnedBudget: s.LearnedBudget, BlockerPoll: s.BlockerPoll, MaxRounds: s.Rounds, StepTimeout: s.Timeout, ReplyGrace: s.ReplyGrace, ReplyNudge: s.ReplyNudge, VerifyCommand: verifyCmd, KnownFailures: tested.VerifyKnownFailures, CheckCommand: checkCmd, StrictCommand: tested.Strict, StrictReadyCommand: tested.StrictReady, StrictTimeout: tested.StrictLimit(s.Timeout),
		},
		Watcher: loop.CAOWatcher{Client: client, Session: standing.Session},
		Stepper: stepper,
		Verify:  loop.Retrying(loop.CommandVerifierWithin(verifyCmd, tested.Timeout(s.Timeout)), s.VerifyRetries), Progress: os.Stdout,
	})
	if err != nil {
		return err
	}
	approved = sum.Outcome == loop.Approved
	return finishRun(os.Stdout, sum)
}

func reseatHints(session string, addresses []string) string {
	commands := make([]string, 0, len(addresses))
	for _, address := range addresses {
		commands = append(commands, "`km reseat "+session+" "+address+"`")
	}
	return "or replace the occupant with " + strings.Join(commands, ", ")
}

func unpinnedError(session string, chairs []string) error {
	return fmt.Errorf("chair(s) %s in session %s have no pinned agent definitions; take it down with km ensemble down %s and km rehearse again", strings.Join(chairs, ", "), session, session)
}
