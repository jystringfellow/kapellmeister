package main

import (
	"bytes"
	"flag"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
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
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, "unexpected CAO call", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	err := runCommand([]string{"-task", "x", "-worktree", t.TempDir(), "-cao", server.URL, "-reviewer", "myprov:myprof@large"})
	want := `reviewer: seat "myprov:myprof@large": no [tiers.myprov] to pin a tier against`
	if err == nil || err.Error() != want {
		t.Errorf("run error = %v, want %s", err, want)
	}
	if calls.Load() != 0 {
		t.Errorf("made %d CAO calls before rejecting the pin", calls.Load())
	}
}

func TestRunAndRigRejectAPinWithoutATierTableBeforeCreatingAWorktree(t *testing.T) {
	for _, command := range []string{"run", "rig"} {
		for _, source := range []string{"seats", "flag", "ensemble"} {
			if command == "rig" && source == "ensemble" {
				continue
			}
			t.Run(command+"/"+source, func(t *testing.T) {
				home := t.TempDir()
				t.Setenv(loop.HomeEnv, home)
				log := pinTestGit(t, "")
				role, run := "reviewer", runCommand
				if command == "rig" {
					role, run = "classifier", rigCommand
				}
				args := []string{"-task", "x", "-repo", t.TempDir(), "-base", "test-base"}
				spec := "myprov:myprof@large"
				config := ""
				switch source {
				case "seats":
					config = "[seats]\n" + role + " = \"" + spec + "\"\n"
				case "ensemble":
					config = "[ensembles.x]\n" + role + " = \"" + spec + "\"\n"
					args = append(args, "-ensemble", "x")
				case "flag":
					args = append(args, "-"+role, spec)
				}
				if err := os.WriteFile(loop.UserConfigPath(home), []byte(config), 0o644); err != nil {
					t.Fatal(err)
				}
				want := role + ": seat \"" + spec + "\": no [tiers.myprov] to pin a tier against"
				if err := run(args); err == nil || err.Error() != want {
					t.Errorf("%s error = %v, want %s", command, err, want)
				}
				data, err := os.ReadFile(log)
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(string(data), "worktree add") {
					t.Error("attempted to create a worktree before rejecting the pin")
				}
			})
		}
	}
}

func TestRunAndRigAcceptAPinWithAConfiguredTierTableBeforeCreatingAWorktree(t *testing.T) {
	for _, command := range []string{"run", "rig"} {
		for _, source := range []string{"user", "repository"} {
			t.Run(command+"/"+source, func(t *testing.T) {
				home := t.TempDir()
				t.Setenv(loop.HomeEnv, home)
				config := "[tiers.myprov.large]\nmodel = \"m\"\n"
				repoConfig := ""
				if source == "user" {
					if err := os.WriteFile(loop.UserConfigPath(home), []byte(config), 0o644); err != nil {
						t.Fatal(err)
					}
				} else {
					repoConfig = config
				}
				pinTestGit(t, repoConfig)
				role, run := "reviewer", runCommand
				if command == "rig" {
					role, run = "classifier", rigCommand
				}
				err := run([]string{"-task", "x", "-repo", t.TempDir(), "-base", "test-base", "-" + role, "myprov:myprof@large"})
				if err == nil || !strings.Contains(err.Error(), "test stopped before worktree creation") {
					t.Fatalf("%s rejected a configured pin: %v", command, err)
				}
			})
		}
	}
}

func pinTestGit(t *testing.T, repoConfig string) string {
	t.Helper()
	dir := t.TempDir()
	log, config := filepath.Join(dir, "git.log"), filepath.Join(dir, "config.toml")
	if err := os.WriteFile(config, []byte(repoConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$KM_TEST_GIT_LOG"
shift 2
case "$1 $2" in
  "fetch origin"|"worktree prune") ;;
  "ls-tree --name-only")
    if test -s "$KM_TEST_REPO_CONFIG"; then printf '.kapellmeister/config.toml\n'; fi ;;
  "show test-base:.kapellmeister/config.toml") cat "$KM_TEST_REPO_CONFIG" ;;
  "worktree add") echo 'test stopped before worktree creation' >&2; exit 1 ;;
  *) echo "unexpected git command: $*" >&2; exit 1 ;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("KM_TEST_GIT_LOG", log)
	t.Setenv("KM_TEST_REPO_CONFIG", config)
	return log
}
