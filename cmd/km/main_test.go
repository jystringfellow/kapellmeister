package main

import (
	"flag"
	"os"
	"testing"
	"time"

	"github.com/jystringfellow/kapellmeister/internal/loop"
)

func TestFlagsOverrideConfigOnlyWhenGiven(t *testing.T) {
	home := t.TempDir()
	t.Setenv(loop.HomeEnv, home)
	t.Setenv(loop.CAOURLEnv, "")
	if err := os.WriteFile(loop.UserConfigPath(home), []byte("[run]\ntimeout = \"1h\"\nrounds = 5\n\n[seats]\nreviewer = \"agy\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	c := commonFlags(fs)
	roles := seatFlags(fs, loop.SeatRoles...)
	if err := fs.Parse([]string{"-rounds", "2", "-sizing", "on", "-implementer", "claude_code:km-claude:claude-fable-5-1"}); err != nil {
		t.Fatal(err)
	}
	s, err := c.settings("")
	if err != nil {
		t.Fatal(err)
	}
	if s.Rounds != 2 || s.Timeout != time.Hour || s.Policy.Sizing.Mode != loop.SizingOn {
		t.Fatalf("settings = rounds %d timeout %s sizing %s", s.Rounds, s.Timeout, s.Policy.Sizing.Mode)
	}
	seats, err := resolveSeats(seatSpecs(s, roles))
	if err != nil {
		t.Fatal(err)
	}
	if seats.Reviewer != (loop.Seat{Provider: "antigravity_cli", Profile: "km-agy"}) {
		t.Fatalf("reviewer = %+v, want the configured agent", seats.Reviewer)
	}
	if seats.Implementer != (loop.Seat{Provider: "claude_code", Profile: "km-claude", Model: "claude-fable-5-1"}) {
		t.Fatalf("implementer = %+v, want the flag", seats.Implementer)
	}
	if seats.Planner != (loop.Seat{Provider: "claude_code", Profile: "km-claude"}) {
		t.Fatalf("planner = %+v, want the default agent", seats.Planner)
	}
}

func TestUnknownSeatsAndBadSizingFlagsAreRejected(t *testing.T) {
	t.Setenv(loop.HomeEnv, t.TempDir())
	if _, err := resolveSeat("gemini"); err == nil {
		t.Fatal("accepted an unknown agent")
	}
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	c := commonFlags(fs)
	if err := fs.Parse([]string{"-sizing", "maybe"}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.settings(""); err == nil {
		t.Fatal("accepted -sizing maybe")
	}
}
