package loop

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeUserConfig(t *testing.T, body string) string {
	t.Helper()
	home := t.TempDir()
	if err := os.WriteFile(UserConfigPath(home), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return home
}

func TestWithoutConfigFilesSettingsAreTheBuiltInDefaults(t *testing.T) {
	t.Setenv(CAOURLEnv, "")
	s, err := LoadSettings(t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	want := DefaultSettings()
	if len(s.Files) != 0 || s.CAOURL != want.CAOURL || s.Rounds != 3 || s.Timeout != 40*time.Minute || s.Seats["implementer"] != "codex" || len(s.RigAgents) != 4 {
		t.Fatalf("settings = %+v", s)
	}
}

func TestUserConfigSetsMachineSettingsAndTheRepoSetsItsContract(t *testing.T) {
	t.Setenv(CAOURLEnv, "")
	home := writeUserConfig(t, `
[cao]
url = "http://127.0.0.1:9999"

[run]
timeout = "1h"
rounds = 5

[seats]
implementer = "claude"
gate = "codex:km-codex:gpt-6-astra"

[rig]
agents = ["claude", "codex", "copilot", "claude"]

[steps.gate]
tier = "large"
min = "small"

[sizing]
mode = "on"
`)
	repo := t.TempDir()
	writeConfig(t, repo, `
[test]
verify = "make verify"

[run]
rounds = 2

[steps.gate]
min = "medium"

[sizing]
mode = "shadow"
`)
	s, err := LoadSettings(home, repo)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(s.Files, " ") != UserConfigPath(home)+" "+filepath.Join(repo, ConfigFile) {
		t.Fatalf("files = %v", s.Files)
	}
	if s.CAOURL != "http://127.0.0.1:9999" || s.Timeout != time.Hour || s.ReplyGrace != 15*time.Minute {
		t.Fatalf("machine settings = %+v", s)
	}
	if s.Rounds != 2 {
		t.Fatalf("the repo's rounds should win, got %d", s.Rounds)
	}
	if s.Seats["implementer"] != "claude" || s.Seats["gate"] != "codex:km-codex:gpt-6-astra" || s.Seats["planner"] != "claude" {
		t.Fatalf("seats = %v", s.Seats)
	}
	if strings.Join(s.RigAgents, ",") != "claude,codex,copilot,claude" {
		t.Fatalf("rig agents = %v", s.RigAgents)
	}
	if gate := s.Policy.Steps["gate"]; gate != (StepPolicy{Tier: "large", Min: "medium", Max: "large"}) {
		t.Fatalf("gate = %+v, want the user's tier under the repo's floor", gate)
	}
	if s.Policy.Sizing.Mode != SizingShadow {
		t.Fatalf("the repo's sizing mode should win, got %q", s.Policy.Sizing.Mode)
	}
}

func TestUserTierModelsWinOverTheRepos(t *testing.T) {
	home := writeUserConfig(t, "[tiers.copilot_cli]\nlarge = { model = \"claude-sonnet-5.5\" }\n")
	repo := t.TempDir()
	writeConfig(t, repo, "[tiers.copilot_cli]\nlarge = { model = \"claude-opus-5.5\" }\nmedium = { model = \"gpt-6-sol\" }\n")
	s, err := LoadSettings(home, repo)
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Policy.Tiers["copilot_cli"]["large"].Model; got != "claude-sonnet-5.5" {
		t.Fatalf("large = %q, want the user's model: only they know what their account can run", got)
	}
	if got := s.Policy.Tiers["copilot_cli"]["medium"].Model; got != "gpt-6-sol" {
		t.Fatalf("medium = %q, want the repo's model where the user set none", got)
	}
}

func TestEnvironmentCAOURLWinsOverTheUserConfig(t *testing.T) {
	t.Setenv(CAOURLEnv, "http://cao.internal:9889")
	s, err := LoadSettings(writeUserConfig(t, "[cao]\nurl = \"http://127.0.0.1:9999\"\n"), "")
	if err != nil {
		t.Fatal(err)
	}
	if s.CAOURL != "http://cao.internal:9889" {
		t.Fatalf("cao = %q", s.CAOURL)
	}
}

func TestSectionsInTheWrongLayerAreRejectedWithWhereTheyBelong(t *testing.T) {
	if _, err := LoadSettings(writeUserConfig(t, "[test]\nverify = \"make\"\n"), ""); err == nil || !strings.Contains(err.Error(), "[test] belongs in the repository's .kapellmeister/config.toml") {
		t.Fatalf("user [test]: %v", err)
	}
	for _, section := range []string{"[seats]\nimplementer = \"claude\"\n", "[cao]\nurl = \"x\"\n", "[rig]\nagents = [\"claude\"]\n"} {
		repo := t.TempDir()
		writeConfig(t, repo, section)
		home := t.TempDir()
		if _, err := LoadSettings(home, repo); err == nil || !strings.Contains(err.Error(), "belongs in "+UserConfigPath(home)) {
			t.Fatalf("repo %q: %v", section, err)
		}
	}
}

func TestInvalidMachineSettingsNameTheFileAndKey(t *testing.T) {
	cases := map[string]string{
		"[run]\ntimeout = \"forty minutes\"\n": "run.timeout",
		"[run]\nrounds = 0\n":                  "run.rounds",
		"[seats]\nreviewr = \"claude\"\n":      "seats.reviewr",
		"[steps.gate]\nmin = \"huge\"\n":       "steps.gate",
	}
	for body, key := range cases {
		home := writeUserConfig(t, body)
		_, err := LoadSettings(home, "")
		if err == nil || !strings.Contains(err.Error(), key) || !strings.Contains(err.Error(), UserConfigPath(home)) {
			t.Fatalf("%q: %v", body, err)
		}
	}
}

func TestHomeAndStateFollowTheirEnvironmentOverrides(t *testing.T) {
	t.Setenv(HomeEnv, "/tmp/km-home")
	t.Setenv(StateEnv, "")
	if Home() != "/tmp/km-home" || StateDir() != "/tmp/km-home/state" {
		t.Fatalf("home %q state %q", Home(), StateDir())
	}
	t.Setenv(StateEnv, "/tmp/km-state")
	if StateDir() != "/tmp/km-state" {
		t.Fatalf("state %q", StateDir())
	}
	t.Setenv(HomeEnv, "")
	if home, _ := os.UserHomeDir(); Home() != filepath.Join(home, ".kapellmeister") {
		t.Fatalf("home %q", Home())
	}
}
