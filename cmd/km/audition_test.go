package main

import (
	"path/filepath"
	"testing"

	"github.com/jystringfellow/kapellmeister/internal/ensemble"
	"github.com/jystringfellow/kapellmeister/internal/loop"
)

func TestCueDoneClosesTheCueAsTheChairsCAOTerminal(t *testing.T) {
	t.Setenv("CAO_TERMINAL_ID", "16780b6d")
	queue := filepath.Join(t.TempDir(), "cues.jsonl")
	if err := cueCommand([]string{"done", "-queue", queue, "audition-gate-gate", "nothing to report"}); err != nil {
		t.Fatal(err)
	}
	closed, err := ensemble.Closures(queue)
	if err != nil {
		t.Fatal(err)
	}
	if c := closed["audition-gate-gate"]; c.Terminal != "16780b6d" || c.Note != "nothing to report" {
		t.Errorf("closure = %+v", c)
	}
}

func TestCueDoneNeedsAQueueAndACue(t *testing.T) {
	for _, args := range [][]string{{"done", "audition-x"}, {"done", "-queue", "q.jsonl"}, {"open", "-queue", "q.jsonl", "x"}} {
		if err := cueCommand(args); err == nil {
			t.Errorf("cue %v succeeded, want an error", args)
		}
	}
}

func TestAuditionNeedsAWorktree(t *testing.T) {
	t.Setenv(loop.HomeEnv, t.TempDir())
	if err := auditionCommand([]string{"-cao", unreachableCAO}); err == nil || err.Error() != "-worktree is required" {
		t.Errorf("audition error = %v, want -worktree is required", err)
	}
}

func TestAuditionRejectsAPinOnAProviderWithoutATierTableBeforeAnyCAOCall(t *testing.T) {
	t.Setenv(loop.HomeEnv, t.TempDir())
	err := auditionCommand([]string{"-worktree", t.TempDir(), "-cao", unreachableCAO, "-reviewer", "myprov:myprof@large"})
	want := `reviewer: chair "myprov:myprof@large": no [tiers.myprov] to pin a tier against`
	if err == nil || err.Error() != want {
		t.Errorf("audition error = %v, want %s", err, want)
	}
}

func TestAuditionRejectsAnUnknownEnsembleBeforeAnyCAOCall(t *testing.T) {
	t.Setenv(loop.HomeEnv, t.TempDir())
	if err := auditionCommand([]string{"-worktree", t.TempDir(), "-cao", unreachableCAO, "-ensemble", "nope"}); err == nil {
		t.Error("audition of an unknown ensemble succeeded")
	}
}
