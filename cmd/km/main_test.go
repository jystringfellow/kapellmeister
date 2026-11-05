package main

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jystringfellow/kapellmeister/internal/loop"
)

func TestFlagsOverrideConfigOnlyWhenGiven(t *testing.T) {
	home := t.TempDir()
	t.Setenv(loop.HomeEnv, home)
	t.Setenv(loop.CAOURLEnv, "")
	if err := os.WriteFile(loop.UserConfigPath(home), []byte("[run]\ntimeout = \"1h\"\nrounds = 5\n\n[chairs]\nreviewer = \"agy\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	c := commonFlags(fs)
	roles := chairFlags(fs, loop.ChairRoles...)
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
	chairs, err := resolveChairs(chairSpecs(s, roles))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(chairs.Reviewers, []loop.Chair{{Provider: "antigravity_cli", Profile: "km-agy"}}) {
		t.Fatalf("reviewer = %+v, want the configured agent", chairs.Reviewers)
	}
	if chairs.Implementer != (loop.Chair{Provider: "claude_code", Profile: "km-claude", Model: "claude-fable-5-1"}) {
		t.Fatalf("implementer = %+v, want the flag", chairs.Implementer)
	}
	if chairs.Planner != (loop.Chair{Provider: "claude_code", Profile: "km-claude"}) {
		t.Fatalf("planner = %+v, want the default agent", chairs.Planner)
	}
}

func TestUnknownChairsAndBadSizingFlagsAreRejected(t *testing.T) {
	t.Setenv(loop.HomeEnv, t.TempDir())
	if _, err := resolveChair("gemini"); err == nil {
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
			chair, err := resolveChair(tc.spec)
			if err != nil || chair != (loop.Chair{Provider: tc.provider, Profile: tc.profile, Tier: tc.tier}) {
				t.Fatalf("%s: %+v %v", tc.spec, chair, err)
			}
		})
	}
	for _, tc := range []struct{ spec, want string }{
		{"claude@huge", "small, medium, large"},
		{"codex@", "small, medium, large"},
		{"claude_code:km-claude:claude-fable-5-1@large", "explicit model"},
	} {
		t.Run(tc.spec, func(t *testing.T) {
			if _, err := resolveChair(tc.spec); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("%s: %v", tc.spec, err)
			}
		})
	}
}

func ensembleSettings(t *testing.T) loop.Settings {
	t.Helper()
	home := t.TempDir()
	t.Setenv(loop.HomeEnv, home)
	if err := os.WriteFile(loop.UserConfigPath(home), []byte(`[chairs]
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
	roles := chairFlags(fs, loop.ChairRoles...)
	if err := fs.Parse([]string{"-reviewer", "codex"}); err != nil {
		t.Fatal(err)
	}
	selected, chairs, err := runChairs(s, "thorough", roles)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(chairs.Reviewers, []loop.Chair{{Provider: "codex", Profile: "km-codex"}}) || chairs.Gate != (loop.Chair{Provider: "claude_code", Profile: "km-claude", Tier: "large"}) || chairs.Implementer != (loop.Chair{Provider: "codex", Profile: "km-codex"}) {
		t.Fatalf("chairs = %+v", chairs)
	}
	if selected.Policy.EscalateAfter() != 0 || s.Policy.EscalateAfter() != 2 {
		t.Fatalf("selected escalation = %d, original = %d", selected.Policy.EscalateAfter(), s.Policy.EscalateAfter())
	}
	_, plain, err := runChairs(s, "", nil)
	if err != nil || !slices.Equal(plain.Reviewers, []loop.Chair{{Provider: "antigravity_cli", Profile: "km-agy"}}) {
		t.Fatalf("default chairs = %+v, %v", plain, err)
	}
	_, overlaid, err := runChairs(s, "", roles)
	if err != nil || !slices.Equal(overlaid.Reviewers, chairs.Reviewers) {
		t.Fatalf("flag without ensemble = %+v, %v", overlaid, err)
	}
	if _, _, err := runChairs(s, "nope", roles); err == nil || !strings.Contains(err.Error(), "careful, thorough") {
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

func TestConfigListsEachEnsembleAndItsChairs(t *testing.T) {
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
	for _, source := range []string{"chairs", "ensemble", "flag"} {
		for _, role := range loop.ChairRoles {
			for _, tier := range loop.TierNames {
				t.Run(source+"/"+role+"/"+tier, func(t *testing.T) {
					home := t.TempDir()
					spec := "myprov:myprof@" + tier
					config, ensemble := "", ""
					fs := flag.NewFlagSet("run", flag.ContinueOnError)
					roles := chairFlags(fs, loop.ChairRoles...)
					switch source {
					case "chairs":
						config = "[chairs]\n" + role + " = \"" + spec + "\"\n"
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
					s, chairs, err := runChairs(s, ensemble, roles)
					if err != nil {
						t.Fatal(err)
					}
					want := role + ": chair \"" + spec + "\": no [tiers.myprov] to pin a tier against"
					if err := checkPins(s.Policy, chairSpecs(s, roles), chairs); err == nil || err.Error() != want {
						t.Fatalf("pin validation = %v, want %s", err, want)
					}
				})
			}
		}
	}
	t.Run("original agent spec", func(t *testing.T) {
		s := loop.DefaultSettings()
		delete(s.Policy.Tiers, "codex")
		s.Chairs["reviewer"] = "codex@small"
		_, chairs, err := runChairs(s, "", nil)
		if err != nil {
			t.Fatal(err)
		}
		want := `reviewer: chair "codex@small": no [tiers.codex] to pin a tier against`
		if err := checkPins(s.Policy, s.Chairs, chairs); err == nil || err.Error() != want {
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
			s.Chairs["reviewer"] = spec
			_, chairs, err := runChairs(s, "", nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := checkPins(s.Policy, s.Chairs, chairs); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestAnUnpinnedChairOnAProviderWithoutATierTableIsAccepted(t *testing.T) {
	for _, spec := range []string{"myprov:myprof", "myprov:myprof:some-model"} {
		t.Run(spec, func(t *testing.T) {
			s := loop.DefaultSettings()
			s.Chairs["reviewer"] = spec
			_, chairs, err := runChairs(s, "", nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := checkPins(s.Policy, s.Chairs, chairs); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRunRejectsAPinOnAProviderWithoutATierTableBeforeAnyCAOCall(t *testing.T) {
	t.Setenv(loop.HomeEnv, t.TempDir())
	err := runCommand([]string{"-task", "x", "-worktree", t.TempDir(), "-cao", unreachableCAO, "-reviewer", "myprov:myprof@large"})
	want := `reviewer: chair "myprov:myprof@large": no [tiers.myprov] to pin a tier against`
	if err == nil || err.Error() != want {
		t.Errorf("run error = %v, want %s", err, want)
	}
}

func TestAPinOnATierItsProvidersTableLacksIsRejected(t *testing.T) {
	s := loop.DefaultSettings()
	s.Policy.Tiers["myprov"] = map[string]loop.TierSpec{"medium": {Model: "m"}}
	for _, tc := range []struct {
		role, spec, want string
	}{
		{"reviewer", "myprov:myprof@small", `reviewer: chair "myprov:myprof@small": [tiers.myprov] has no small tier to pin against`},
		{"reviewer", "myprov:myprof@large", `reviewer: chair "myprov:myprof@large": [tiers.myprov] has no large tier to pin against`},
		{"reviewer", "myprov:myprof@medium", ""},
		{"gate", "myprov:myprof@small", ""},
	} {
		t.Run(tc.role+"/"+tc.spec, func(t *testing.T) {
			specs := map[string]string{}
			for role, spec := range s.Chairs {
				specs[role] = spec
			}
			specs[tc.role] = tc.spec
			chairs, err := resolveChairs(specs)
			if err != nil {
				t.Fatal(err)
			}
			err = checkPins(s.Policy, specs, chairs)
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

func TestTheReviewerFlagReplacesTheWholeConfiguredList(t *testing.T) {
	s := ensembleSettings(t)
	s.Chairs["reviewer"] = "agy,copilot"
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	roles := chairFlags(fs, loop.ChairRoles...)
	if err := fs.Parse([]string{"-reviewer", "copilot,claude@large"}); err != nil {
		t.Fatal(err)
	}
	_, chairs, err := runChairs(s, "thorough", roles)
	want := []loop.Chair{{Provider: "copilot_cli", Profile: "km-copilot"}, {Provider: "claude_code", Profile: "km-claude", Tier: "large"}}
	if err != nil || !slices.Equal(chairs.Reviewers, want) {
		t.Fatalf("%+v %v", chairs, err)
	}
}

func TestOnlyReviewerFlagsAcceptSeveralChairsAndErrorsNameTheChair(t *testing.T) {
	for _, role := range loop.ChairRoles {
		s := loop.DefaultSettings()
		s.Chairs[role] = "claude,codex"
		_, err := resolveChairs(s.Chairs)
		if role == "reviewer" {
			if err != nil {
				t.Error(err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), role+":") || !strings.Contains(err.Error(), "only reviewer") {
			t.Errorf("%s: %v", role, err)
		}
	}
	s := loop.DefaultSettings()
	s.Chairs["reviewer"] = "copilot,unknown"
	if _, err := resolveChairs(s.Chairs); err == nil || !strings.Contains(err.Error(), "reviewer-2:") {
		t.Fatalf("%v", err)
	}
}

func TestPinsAreCheckedForEveryReviewerWithItsNumberedName(t *testing.T) {
	s := loop.DefaultSettings()
	for _, spec := range []string{"myprov:custom@large", "myprov:custom@small"} {
		s.Chairs["reviewer"] = "copilot," + spec
		chairs, err := resolveChairs(s.Chairs)
		if err != nil {
			t.Fatal(err)
		}
		want := "reviewer-2: chair \"" + spec + "\": no [tiers.myprov] to pin a tier against"
		if err := checkPins(s.Policy, s.Chairs, chairs); err == nil || err.Error() != want {
			t.Fatalf("%v want %s", err, want)
		}
	}
}

func TestPreflightAndEffortProfilesCoverEveryReviewer(t *testing.T) {
	s := loop.DefaultSettings()
	s.Chairs["reviewer"] = "copilot,claude@large,codex@small"
	chairs, err := resolveChairs(s.Chairs)
	if err != nil {
		t.Fatal(err)
	}
	if len(stepChairs(chairs)["review"]) != 3 {
		t.Fatal(stepChairs(chairs))
	}
	preflight := defaultChairs(&s.Policy, chairs)
	for _, reviewer := range chairs.Reviewers {
		want, _ := s.Policy.Chair("review", reviewer, "", 0)
		if !slices.Contains(preflight, want) {
			t.Fatalf("missing %+v in %v", want, preflight)
		}
	}
	variants := s.Policy.EffortVariants(stepChairs(chairs))
	var names []string
	for _, v := range variants {
		names = append(names, v.Name())
	}
	for _, name := range []string{"km-claude-xhigh", "km-codex-medium"} {
		if !slices.Contains(names, name) {
			t.Fatalf("missing %s: %v", name, names)
		}
	}
}

func TestConfigShowsReviewerListsAndGateWhen(t *testing.T) {
	s := loop.DefaultSettings()
	s.Chairs["reviewer"] = "copilot,claude@large"
	var out bytes.Buffer
	printChairs(&out, s)
	if !strings.Contains(out.String(), "reviewer=copilot,claude@large") || !strings.Contains(out.String(), "gate_when=any") {
		t.Fatal(out.String())
	}
	home := t.TempDir()
	if err := os.WriteFile(loop.UserConfigPath(home), []byte("[ensembles.x]\nreviewer = [\"copilot\", \"claude@large\"]\ngate_when = \"always\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := loop.LoadSettings(home, "")
	if err != nil {
		t.Fatal(err)
	}
	out.Reset()
	printEnsembles(&out, s)
	if !strings.Contains(out.String(), "reviewer=copilot,claude@large gate_when=always") {
		t.Fatal(out.String())
	}
}
