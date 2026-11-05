package main

import (
	"bytes"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
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

func TestAPinnedTierOnAProviderWithoutATierTableIsRejected(t *testing.T) {
	for _, source := range []string{"seats", "ensemble", "flag"} {
		for _, role := range loop.SeatRoles {
			for _, tier := range loop.TierNames {
				t.Run(source+"/"+role+"/"+tier, func(t *testing.T) {
					home := t.TempDir()
					spec := "myprov:myprof@" + tier
					config, ensemble := "", ""
					fs := flag.NewFlagSet("run", flag.ContinueOnError)
					roles := seatFlags(fs, loop.SeatRoles...)
					switch source {
					case "seats":
						config = "[seats]\n" + role + " = \"" + spec + "\"\n"
					case "ensemble":
						config = "[ensembles.x]\n" + role + " = \"" + spec + "\"\n"
						ensemble = "x"
					case "flag":
						if err := fs.Parse([]string{"-" + role, spec}); err != nil {
							t.Fatal(err)
						}
					}
					if err := os.WriteFile(loop.UserConfigPath(home), []byte(config), 0o644); err != nil {
						t.Fatal(err)
					}
					s, err := loop.LoadSettings(home, "")
					if err != nil {
						t.Fatal(err)
					}
					s, seats, err := runSeats(s, ensemble, roles)
					if err != nil {
						t.Fatal(err)
					}
					want := role + ": seat \"" + spec + "\": no [tiers.myprov] to pin a tier against"
					if err := checkPins(s.Policy, seatSpecs(s, roles), seats); err == nil || err.Error() != want {
						t.Fatalf("pin validation = %v, want %s", err, want)
					}
				})
			}
		}
	}
	t.Run("original agent spec", func(t *testing.T) {
		s := loop.DefaultSettings()
		delete(s.Policy.Tiers, "codex")
		s.Seats["reviewer"] = "codex@small"
		_, seats, err := runSeats(s, "", nil)
		if err != nil {
			t.Fatal(err)
		}
		want := `reviewer: seat "codex@small": no [tiers.codex] to pin a tier against`
		if err := checkPins(s.Policy, s.Seats, seats); err == nil || err.Error() != want {
			t.Fatalf("pin validation = %v, want %s", err, want)
		}
	})
}

func TestAPinnedTierOnAProviderWithATierTableIsAccepted(t *testing.T) {
	for _, source := range []string{"built-in", "user", "repository"} {
		t.Run(source, func(t *testing.T) {
			home, worktree := t.TempDir(), t.TempDir()
			spec := "claude@large"
			if source != "built-in" {
				spec = "myprov:myprof@large"
				path := loop.UserConfigPath(home)
				if source == "repository" {
					path = filepath.Join(worktree, loop.ConfigFile)
					if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
						t.Fatal(err)
					}
				}
				if err := os.WriteFile(path, []byte("[tiers.myprov.large]\nmodel = \"m\"\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			s, err := loop.LoadSettings(home, worktree)
			if err != nil {
				t.Fatal(err)
			}
			s.Seats["reviewer"] = spec
			_, seats, err := runSeats(s, "", nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := checkPins(s.Policy, s.Seats, seats); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestAnUnpinnedSeatOnAProviderWithoutATierTableIsAccepted(t *testing.T) {
	for _, spec := range []string{"myprov:myprof", "myprov:myprof:some-model"} {
		t.Run(spec, func(t *testing.T) {
			s := loop.DefaultSettings()
			s.Seats["reviewer"] = spec
			_, seats, err := runSeats(s, "", nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := checkPins(s.Policy, s.Seats, seats); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRunRejectsAPinOnAProviderWithoutATierTableBeforeAnyCAOCall(t *testing.T) {
	t.Setenv(loop.HomeEnv, t.TempDir())
	err := runCommand([]string{"-task", "x", "-worktree", t.TempDir(), "-cao", unreachableCAO, "-reviewer", "myprov:myprof@large"})
	want := `reviewer: seat "myprov:myprof@large": no [tiers.myprov] to pin a tier against`
	if err == nil || err.Error() != want {
		t.Errorf("run error = %v, want %s", err, want)
	}
}

func TestRigRejectsAPinOnAProviderWithoutATierTableBeforeAnyCAOCall(t *testing.T) {
	t.Setenv(loop.HomeEnv, t.TempDir())
	t.Setenv(loop.StateEnv, t.TempDir())
	t.Setenv("TMPDIR", t.TempDir())
	err := rigCommand([]string{"-task", "x", "-repo", clonedRepo(t), "-base", "origin/main", "-cao", unreachableCAO, "-classifier", "myprov:myprof@large"})
	want := `classifier: seat "myprov:myprof@large": no [tiers.myprov] to pin a tier against`
	if err == nil || err.Error() != want {
		t.Errorf("rig error = %v, want %s", err, want)
	}
}

func TestAPinOnATierItsProvidersTableLacksIsRejected(t *testing.T) {
	s := loop.DefaultSettings()
	s.Policy.Tiers["myprov"] = map[string]loop.TierSpec{"medium": {Model: "m"}}
	for _, tc := range []struct {
		role, spec, want string
	}{
		{"reviewer", "myprov:myprof@small", `reviewer: seat "myprov:myprof@small": [tiers.myprov] has no small tier to pin against`},
		{"reviewer", "myprov:myprof@large", `reviewer: seat "myprov:myprof@large": [tiers.myprov] has no large tier to pin against`},
		{"reviewer", "myprov:myprof@medium", ""},
		{"gate", "myprov:myprof@small", ""},
	} {
		t.Run(tc.role+"/"+tc.spec, func(t *testing.T) {
			specs := map[string]string{}
			for role, spec := range s.Seats {
				specs[role] = spec
			}
			specs[tc.role] = tc.spec
			seats, err := resolveSeats(specs)
			if err != nil {
				t.Fatal(err)
			}
			err = checkPins(s.Policy, specs, seats)
			if tc.want == "" && err != nil {
				t.Fatalf("rejected a pin that resolves to a configured tier: %v", err)
			}
			if tc.want != "" && (err == nil || err.Error() != tc.want) {
				t.Fatalf("pin validation = %v, want %s", err, tc.want)
			}
		})
	}
}

const unreachableCAO = "http://127.0.0.1:1"

func clonedRepo(t *testing.T) string {
	t.Helper()
	origin, clone := t.TempDir(), filepath.Join(t.TempDir(), "clone")
	for _, args := range [][]string{
		{"-C", origin, "init", "-q", "-b", "main"},
		{"-C", origin, "-c", "user.name=t", "-c", "user.email=t@example.invalid", "commit", "-q", "--allow-empty", "-m", "base"},
		{"clone", "-q", origin, clone},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	return clone
}
