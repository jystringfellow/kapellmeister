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
	if len(s.Files) != 0 || s.CAOURL != want.CAOURL || s.Rounds != 3 || s.Timeout != 40*time.Minute || s.Chairs["implementer"] != "codex" {
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

[chairs]
implementer = "claude"
gate = "codex:km-codex:gpt-6-astra"

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
	if s.Chairs["implementer"] != "claude" || s.Chairs["gate"] != "codex:km-codex:gpt-6-astra" || s.Chairs["planner"] != "claude" {
		t.Fatalf("chairs = %v", s.Chairs)
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
	for _, section := range []string{"[chairs]\nimplementer = \"claude\"\n", "[cao]\nurl = \"x\"\n"} {
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
		"[chairs]\nreviewr = \"claude\"\n":     "chairs.reviewr",
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

func TestANamedEnsembleLoadsFromTheUserConfig(t *testing.T) {
	home := writeUserConfig(t, `[chairs]
implementer = "codex"
[ensembles.thorough]
reviewer = "claude@large"
gate = "claude@large"
escalate = false
`)
	if _, err := LoadSettings(home, ""); err != nil {
		t.Fatal(err)
	}
}

func TestEnsemblesInTheRepositoryConfigAreRejected(t *testing.T) {
	for _, body := range []string{"[ensembles]\n", "[ensembles.thorough]\nreviewer = \"claude\"\n"} {
		repo, home := t.TempDir(), t.TempDir()
		writeConfig(t, repo, body)
		_, err := LoadSettings(home, repo)
		if err == nil || !strings.Contains(err.Error(), ConfigFile+": [ensembles] belongs in "+UserConfigPath(home)) {
			t.Fatalf("repo ensembles: %v", err)
		}
	}
}

func TestAnUnknownKeyInAnEnsembleIsRejectedNamingTheFileAndKey(t *testing.T) {
	home := writeUserConfig(t, "[ensembles.thorough]\nreviewers = \"claude\"\n")
	_, err := LoadSettings(home, "")
	if err == nil || !strings.Contains(err.Error(), UserConfigPath(home)+": unknown keys") || !strings.Contains(err.Error(), "ensembles.thorough.reviewers") {
		t.Fatalf("unknown ensemble key: %v", err)
	}
}

func TestAnEnsembleOverlaysChairsAndTakesTheRestFromChairsThenDefaults(t *testing.T) {
	home := writeUserConfig(t, `[chairs]
implementer = "codex@small"
planner = "agy"
[ensembles.thorough]
reviewer = "claude@large"
gate = "claude@large"
`)
	s, err := LoadSettings(home, "")
	if err != nil {
		t.Fatal(err)
	}
	selected, err := s.WithEnsemble("thorough")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"planner": "agy", "implementer": "codex@small", "reviewer": "claude@large", "gate": "claude@large", "classifier": "claude"}
	for role, spec := range want {
		if selected.Chairs[role] != spec {
			t.Fatalf("%s = %q, want %q", role, selected.Chairs[role], spec)
		}
	}
	if s.Chairs["reviewer"] != "copilot" || s.Chairs["gate"] != "claude" || s.Chairs["implementer"] != "codex@small" {
		t.Fatalf("selection mutated the default chairs: %v", s.Chairs)
	}
}

func TestAnUnknownEnsembleIsRejectedListingTheDefinedOnes(t *testing.T) {
	home := writeUserConfig(t, "[ensembles.thorough]\n[ensembles.careful]\n")
	s, err := LoadSettings(home, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.WithEnsemble("fast"); err == nil || !strings.Contains(err.Error(), `unknown ensemble "fast"`) || !strings.Contains(err.Error(), "defined: careful, thorough") {
		t.Fatalf("unknown ensemble: %v", err)
	}
	if _, err := DefaultSettings().WithEnsemble("fast"); err == nil || !strings.Contains(err.Error(), "no ensembles defined") {
		t.Fatalf("no ensembles: %v", err)
	}
}

func TestEnsembleEscalateFalseTurnsEscalationOffAndCannotTurnItOn(t *testing.T) {
	for _, tc := range []struct {
		name, key, repo string
		want            int
	}{
		{"disabled", "escalate = false", "", 0},
		{"enabled", "escalate = true", "", 2},
		{"absent", "", "", 2},
		{"repository disables", "escalate = true", "[sizing]\nescalate_after = 0\n", 0},
		{"repository configures", "escalate = false", "[sizing]\nescalate_after = 1\n", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := writeUserConfig(t, "[ensembles.steady]\n"+tc.key+"\n")
			repo := t.TempDir()
			writeConfig(t, repo, tc.repo)
			s, err := LoadSettings(home, repo)
			if err != nil {
				t.Fatal(err)
			}
			before := s.Policy.EscalateAfter()
			selected, err := s.WithEnsemble("steady")
			if err != nil {
				t.Fatal(err)
			}
			if got := selected.Policy.EscalateAfter(); got != tc.want {
				t.Fatalf("escalate after = %d, want %d", got, tc.want)
			}
			if s.Policy.EscalateAfter() != before {
				t.Fatal("selection mutated the original escalation policy")
			}
		})
	}
}

func TestReviewerAcceptsAListOfChairsInChairsAndInEnsembles(t *testing.T) {
	s, err := LoadSettings(writeUserConfig(t, `[chairs]
reviewer = ["copilot", "claude@large"]
[ensembles.pair]
reviewer = ["agy", "codex"]
`), "")
	if err != nil {
		t.Fatal(err)
	}
	if s.Chairs["reviewer"] != "copilot,claude@large" {
		t.Fatal(s.Chairs)
	}
	selected, err := s.WithEnsemble("pair")
	if err != nil || selected.Chairs["reviewer"] != "agy,codex" {
		t.Fatalf("%+v %v", selected, err)
	}
}

func TestAListOfChairsForAnyOtherRoleIsRejectedNamingTheFileAndRole(t *testing.T) {
	for _, section := range []string{"chairs", "ensembles.x"} {
		for _, role := range ChairRoles {
			if role == "reviewer" {
				continue
			}
			home := writeUserConfig(t, "["+section+"]\n"+role+" = [\"claude\"]\n")
			_, err := LoadSettings(home, "")
			if err == nil || !strings.Contains(err.Error(), UserConfigPath(home)) || !strings.Contains(err.Error(), section+"."+role) {
				t.Errorf("%s.%s: %v", section, role, err)
			}
		}
	}
	for _, value := range []string{`[]`, `["a,b"]`, `[""]`, `[1]`, `42`} {
		if _, err := LoadSettings(writeUserConfig(t, "[chairs]\nreviewer = "+value+"\n"), ""); err == nil {
			t.Errorf("accepted %s", value)
		}
	}
}

func TestGateWhenDefaultsToAnyAndAnEnsembleCanSetIt(t *testing.T) {
	for _, when := range []string{"", "any", "all", "always"} {
		body := "[ensembles.x]\n"
		if when != "" {
			body += "gate_when = \"" + when + "\"\n"
		}
		s, err := LoadSettings(writeUserConfig(t, body), "")
		if err != nil {
			t.Fatal(err)
		}
		if s.GateWhen != "any" {
			t.Errorf("default: %s", s.GateWhen)
		}
		s, err = s.WithEnsemble("x")
		if err != nil {
			t.Fatal(err)
		}
		want := when
		if want == "" {
			want = "any"
		}
		if s.GateWhen != want {
			t.Errorf("selected: %s want %s", s.GateWhen, want)
		}
	}
}

func TestAnUnknownGateWhenIsRejectedNamingTheFileAndKey(t *testing.T) {
	home := writeUserConfig(t, "[ensembles.x]\ngate_when = \"sometimes\"\n")
	_, err := LoadSettings(home, "")
	if err == nil || !strings.Contains(err.Error(), UserConfigPath(home)) || !strings.Contains(err.Error(), "ensembles.x.gate_when") {
		t.Fatalf("%v", err)
	}
}

func TestAnExplicitlyEmptyGateWhenIsRejectedNamingTheFileAndKey(t *testing.T) {
	home := writeUserConfig(t, "[ensembles.x]\ngate_when = \"\"\n")
	_, err := LoadSettings(home, "")
	if err == nil || !strings.Contains(err.Error(), UserConfigPath(home)) || !strings.Contains(err.Error(), "ensembles.x.gate_when") || !strings.Contains(err.Error(), "any, all or always") {
		t.Fatalf("explicitly empty gate_when: %v", err)
	}
}

func TestEveryKnownAgentIsAvailableUnlessTheUserConfigSaysOtherwise(t *testing.T) {
	s, err := LoadSettings(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(s.Agents, ",") != "claude,codex,copilot,agy" {
		t.Fatalf("agents = %v, want every known agent", s.Agents)
	}
	s, err = LoadSettings(writeUserConfig(t, "[agents]\navailable = [\"copilot\", \"claude\"]\n"), "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(s.Agents, ",") != "copilot,claude" {
		t.Fatalf("agents = %v, want copilot,claude", s.Agents)
	}
}

func TestAnUnknownAvailableAgentIsRejectedNamingTheFileAndAgent(t *testing.T) {
	home := writeUserConfig(t, "[agents]\navailable = [\"claude\", \"gemini\"]\n")
	_, err := LoadSettings(home, "")
	if err == nil || !strings.Contains(err.Error(), UserConfigPath(home)+": agents.available: unknown agent \"gemini\"") {
		t.Fatalf("err = %v", err)
	}
}

func TestAvailableAgentsBelongInTheUserConfig(t *testing.T) {
	repo := t.TempDir()
	writeConfig(t, repo, "[agents]\navailable = [\"claude\"]\n")
	home := t.TempDir()
	if _, err := LoadSettings(home, repo); err == nil || !strings.Contains(err.Error(), "[agents] belongs in "+UserConfigPath(home)) {
		t.Fatalf("err = %v", err)
	}
}

func TestBlockerPollingDefaultsToTwentySecondsAndCanBeOverriddenOrDisabled(t *testing.T) {
	if got := DefaultSettings().BlockerPoll; got != 20*time.Second {
		t.Fatalf("default = %s", got)
	}
	for _, value := range []string{"3s", "0", "-1s", "soon"} {
		t.Run(value, func(t *testing.T) {
			home := writeUserConfig(t, "[run]\nblocker_poll = '1s'\n")
			repo := t.TempDir()
			writeConfig(t, repo, "[run]\nblocker_poll = '"+value+"'\n")
			s, err := LoadSettings(home, repo)
			if value == "-1s" || value == "soon" {
				if err == nil || !strings.Contains(err.Error(), "run.blocker_poll") {
					t.Fatalf("error = %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			want, _ := time.ParseDuration(value)
			if s.BlockerPoll != want {
				t.Fatalf("poll = %s", s.BlockerPoll)
			}
		})
	}
}
