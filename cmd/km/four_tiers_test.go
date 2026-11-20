package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jystringfellow/kapellmeister/internal/loop"
)

func TestXlargeIsAcceptedByRoleFlagsAndConfiguredChairs(t *testing.T) {
	s := builtInSettings()
	s.Chairs["implementer"] = "codex@xlarge"
	roles := noRoleFlags(t)
	*roles["reviewer"] = "claude@xlarge"
	s, chairs, err := runChairs(s, "", roles)
	if err != nil {
		t.Fatal(err)
	}
	if chairs.Implementer.Tier != "xlarge" || chairs.Reviewers[0].Tier != "xlarge" {
		t.Fatal(chairs)
	}
	if err := checkPins(s.Policy, chairSpecs(s, roles), chairs); err != nil {
		t.Fatal(err)
	}
}
func TestATierPinOnAChairOfACustomPartIsHonoured(t *testing.T) {
	for _, pin := range []string{`agents = ["codex@xlarge"]`, "agents = [\"codex\"]\ntier = \"xlarge\""} {
		t.Run(pin, func(t *testing.T) {
			s, _, _, wt := smallRepositoryScore(t)
			s.Score = loop.LoadedScore{}
			writeFile(t, wt+"/.kapellmeister/ensembles/small.toml", strings.Replace(smallScoreEnsemble, `agents = ["codex"]`, pin, 1))
			s, _, members, err := lineUp(s, wt, "small", noRoleFlags(t))
			if err != nil {
				t.Fatal(err)
			}
			sizeMembers(s.Policy, members)
			if members[0].Chair.Model != s.Policy.Tiers["codex"]["xlarge"].Model || members[0].Chair.Model == "" {
				t.Fatal(members)
			}
		})
	}
}

func TestAnUnknownStepInConfigIsRefusedWhenTheScoreHasNoSuchStep(t *testing.T) {
	home := t.TempDir()
	t.Setenv(loop.HomeEnv, home)
	writeFile(t, loop.UserConfigPath(home), "[steps.missing]\ntier = 'large'\n")
	s, err := loop.LoadSettings(home, "")
	if err != nil {
		t.Fatal(err)
	}
	_, err = loadSelectedScore(s, "", "")
	if err == nil || !strings.Contains(err.Error(), "steps.missing: unknown step") {
		t.Fatal(err)
	}
}
func TestConfigPrintsFourTiersPerProvider(t *testing.T) {
	s := builtInSettings()
	t.Setenv(loop.HomeEnv, t.TempDir())
	writeFile(t, loop.UserConfigPath(loop.Home()), builtInAgentConfig)
	var output bytes.Buffer
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()
	text, err := captureStdout(t, func() error { return configCommand([]string{"-worktree", t.TempDir(), "-cao", server.URL}) })
	if err != nil {
		t.Fatal(err)
	}
	output.WriteString(text)
	for provider := range s.Policy.Tiers {
		for _, tier := range []string{"small", "medium", "large", "xlarge"} {
			found := false
			for _, line := range strings.Split(output.String(), "\n") {
				fields := strings.Fields(line)
				if len(fields) > 2 && fields[0] == provider && fields[1] == tier {
					found = true
				}
			}
			if !found {
				t.Fatalf("missing %s %s in %s", provider, tier, &output)
			}
		}
	}
}

func TestAPinForAnUnshippedProvidersMissingXlargeNamesTheMissingTier(t *testing.T) {
	p := loop.DefaultPolicy()
	p.Tiers["other"] = map[string]loop.TierSpec{"small": {Model: "first"}, "medium": {Model: "second"}, "large": {Model: "third"}}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	chair := loop.Chair{Provider: "other", Profile: "base", Tier: "xlarge"}
	err := checkPin(p, "implementer", "implementer", "other:base@xlarge", chair)
	want := `implementer: chair "other:base@xlarge": [tiers.other] has no xlarge tier to pin against`
	if err == nil || err.Error() != want {
		t.Fatal(err)
	}
}
func TestACustomStepsConfiguredTierIsUsedWhenSeatingItsChair(t *testing.T) {
	s, _, members, wt := smallRepositoryScore(t)
	writeFile(t, loop.UserConfigPath(loop.Home()), builtInAgentConfig+"\n[steps.draft]\ntier = 'large'\nmax = 'large'\n[tiers.codex]\nlarge = {model = 'chosen',effort = 'inherit'}\n")
	var err error
	s, err = loop.LoadSettings(loop.Home(), wt)
	if err != nil {
		t.Fatal(err)
	}
	s, _, members, err = lineUp(s, wt, "small", noRoleFlags(t))
	if err != nil {
		t.Fatal(err)
	}
	sizeMembers(s.Policy, members)
	if members[0].Chair.Model != "chosen" || members[0].Chair.Profile != "km-codex" {
		t.Fatal(members)
	}
}

func TestAnUnpinnedChairIsRefusedWhenItsStepRequestsAMissingTier(t *testing.T) {
	p := loop.DefaultPolicy()
	p.Tiers["other"] = map[string]loop.TierSpec{"small": {Model: "first"}, "medium": {Model: "second"}, "large": {Model: "third"}}
	p.Steps["draft"] = loop.StepPolicy{Tier: "xlarge", Min: "small", Max: "xlarge"}
	score := loop.Score{Steps: []loop.ScoreStep{{Name: "draft", Kind: loop.KindProduce, Part: "drafter"}}}
	p, err := p.ForScore(score)
	if err != nil {
		t.Fatal(err)
	}
	err = checkPin(p, "drafter", "strings.first", "other:base", loop.Chair{Provider: "other", Profile: "base"})
	if err == nil || !strings.Contains(err.Error(), "[tiers.other] has no xlarge tier to pin against") {
		t.Fatal(err)
	}
}
func TestPreflightRefusesARequestedTierThatTheProvidersTableLacks(t *testing.T) {
	t.Setenv(loop.HomeEnv, t.TempDir())
	writeFile(t, loop.UserConfigPath(loop.Home()), `[agents]
available = ["mine"]
[agents.define]
mine = "other:base"
[tiers.other]
small = {model = "first"}
medium = {model = "second"}
large = {model = "third"}
[run]
preflight_timeout = "0s"
`)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("unexpected request", r.URL.Path)
		http.NotFound(w, r)
	}))
	defer server.Close()
	_, err := captureStdout(t, func() error {
		return preflightCommand([]string{"-worktree", t.TempDir(), "-tier", "xlarge", "-cao", server.URL})
	})
	if err == nil || !strings.Contains(err.Error(), "[tiers.other] has no xlarge tier to pin against") {
		t.Fatal(err)
	}
}
