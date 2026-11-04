package main

import (
	"bytes"
	"flag"
	"os"
	"strings"
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

func TestAgentNamesAcceptAPinnedTier(t *testing.T) {
	for _, tc := range []struct{ spec, provider, profile, tier string }{
		{"claude@large", "claude_code", "km-claude", "large"},
		{"codex@small", "codex", "km-codex", "small"},
		{"copilot@medium", "copilot_cli", "km-copilot", "medium"},
		{"claude_code:custom@large", "claude_code", "custom", "large"},
	} {
		t.Run(tc.spec, func(t *testing.T) {
			seat, err := resolveSeat(tc.spec)
			if err != nil || seat != (loop.Seat{Provider: tc.provider, Profile: tc.profile, Tier: tc.tier}) {
				t.Fatalf("%s: %+v %v", tc.spec, seat, err)
			}
		})
	}
	for _, tc := range []struct{ spec, want string }{
		{"claude@huge", "small, medium, large"},
		{"codex@", "small, medium, large"},
		{"claude_code:km-claude:claude-fable-5-1@large", "explicit model"},
	} {
		t.Run(tc.spec, func(t *testing.T) {
			if _, err := resolveSeat(tc.spec); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("%s: %v", tc.spec, err)
			}
		})
	}
}

func ensembleSettings(t *testing.T) loop.Settings {
	t.Helper()
	home := t.TempDir()
	t.Setenv(loop.HomeEnv, home)
	if err := os.WriteFile(loop.UserConfigPath(home), []byte(`[seats]
reviewer = "agy"
[ensembles.thorough]
reviewer = "claude@large"
gate = "claude@large"
escalate = false
[ensembles.careful]
implementer = "codex@small"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := loop.LoadSettings(home, "")
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestARoleFlagOverridesTheSelectedEnsemble(t *testing.T) {
	s := ensembleSettings(t)
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	roles := seatFlags(fs, loop.SeatRoles...)
	if err := fs.Parse([]string{"-reviewer", "codex"}); err != nil {
		t.Fatal(err)
	}
	selected, seats, err := runSeats(s, "thorough", roles)
	if err != nil {
		t.Fatal(err)
	}
	if seats.Reviewer != (loop.Seat{Provider: "codex", Profile: "km-codex"}) || seats.Gate != (loop.Seat{Provider: "claude_code", Profile: "km-claude", Tier: "large"}) || seats.Implementer != (loop.Seat{Provider: "codex", Profile: "km-codex"}) {
		t.Fatalf("seats = %+v", seats)
	}
	if selected.Policy.EscalateAfter() != 0 || s.Policy.EscalateAfter() != 2 {
		t.Fatalf("selected escalation = %d, original = %d", selected.Policy.EscalateAfter(), s.Policy.EscalateAfter())
	}
	_, plain, err := runSeats(s, "", nil)
	if err != nil || plain.Reviewer != (loop.Seat{Provider: "antigravity_cli", Profile: "km-agy"}) {
		t.Fatalf("default seats = %+v, %v", plain, err)
	}
	_, overlaid, err := runSeats(s, "", roles)
	if err != nil || overlaid.Reviewer != seats.Reviewer {
		t.Fatalf("flag without ensemble = %+v, %v", overlaid, err)
	}
	if _, _, err := runSeats(s, "nope", roles); err == nil || !strings.Contains(err.Error(), "careful, thorough") {
		t.Fatalf("unknown ensemble: %v", err)
	}
}

func TestAnUnknownEnsembleFlagIsRejectedBeforeWorktreeCreation(t *testing.T) {
	ensembleSettings(t)
	err := runCommand([]string{"-task", "x", "-repo", "/nonexistent-km-repository", "-ensemble", "nope"})
	if err == nil || !strings.Contains(err.Error(), `unknown ensemble "nope"`) || !strings.Contains(err.Error(), "careful, thorough") {
		t.Fatalf("unknown ensemble flag: %v", err)
	}
}

func TestConfigListsEachEnsembleAndItsSeats(t *testing.T) {
	s := ensembleSettings(t)
	var out bytes.Buffer
	printEnsembles(&out, s)
	want := "ensemble careful  implementer=codex@small\nensemble thorough  reviewer=claude@large gate=claude@large escalate=false\n"
	if out.String() != want {
		t.Fatalf("ensembles:\n%s\nwant:\n%s", &out, want)
	}
	out.Reset()
	printEnsembles(&out, loop.DefaultSettings())
	if out.Len() != 0 {
		t.Fatalf("no ensembles: %q", &out)
	}
}
