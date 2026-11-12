package loop

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

func TestResolveChairKnowsUserDefinedAgentsAndTheirTiers(t *testing.T) {
	agents, err := BuiltInAgents().With(map[string]string{"mine": "claude_code:my-reviewer"})
	if err != nil {
		t.Fatal(err)
	}
	for _, tier := range []string{"", "small", "medium", "large"} {
		spec := "mine"
		if tier != "" {
			spec += "@" + tier
		}
		chair, err := agents.ResolveChair(spec)
		if err != nil || chair != (Chair{Provider: "claude_code", Profile: "my-reviewer", Tier: tier}) {
			t.Fatalf("chair = %+v, %v", chair, err)
		}
	}
	for _, spec := range []string{"unknown", "mine,codex"} {
		if _, err := agents.ResolveChair(spec); err == nil || !strings.Contains(err.Error(), "mine") {
			t.Fatalf("unknown: %v", err)
		}
	}
	selected, err := agents.Select("mine,codex")
	if err != nil || selected[0].Chair.Profile != "my-reviewer" || selected[1].Name != "codex" {
		t.Fatalf("selected = %v, %v", selected, err)
	}
}

func TestADefinitionOverridesABuiltInAgent(t *testing.T) {
	for _, name := range BuiltInAgents().Names() {
		home := writeUserConfig(t, fmt.Sprintf("[agents]\navailable = [%q]\n[agents.define]\n%s = %q\n", name, name, "other:custom"))
		s, err := LoadSettings(home, "")
		if err != nil {
			t.Fatal(err)
		}
		chair, err := s.Defined.ResolveChair(name)
		if err != nil || chair.Provider != "other" || chair.Profile != "custom" {
			t.Fatalf("chair = %+v, %v", chair, err)
		}
	}
	if c, _ := BuiltInAgents().ResolveChair("claude"); c.Profile != "km-claude" {
		t.Fatal("modified built-ins")
	}
}

func TestAnAgentDefinitionWithAModelATierOrABadNameIsRejected(t *testing.T) {
	for _, def := range []struct{ name, value string }{
		{"mine", "codex:base:model"}, {"mine", "codex:base:"}, {"mine", "codex:base@large"}, {"mine", ":base"}, {"mine", "codex:"}, {"mine", "codex"}, {"mine", "codex:base:extra:part"},
		{"Bad", "codex:base"}, {"bad,name", "codex:base"}, {"bad@name", "codex:base"}, {"bad:name", "codex:base"}, {"", "codex:base"},
	} {
		home := writeUserConfig(t, fmt.Sprintf("[agents.define]\n%q = %q\n", def.name, def.value))
		_, err := LoadSettings(home, "")
		if err == nil || !strings.Contains(err.Error(), UserConfigPath(home)) || !strings.Contains(err.Error(), "agents.define."+def.name) {
			t.Errorf("%+v: %v", def, err)
		}
	}
}

func TestAnAgentMayBeDefinedInTheUserConfigOnly(t *testing.T) {
	home, repo := t.TempDir(), t.TempDir()
	writeConfig(t, repo, "[agents.define]\nmine = \"other:base\"\n")
	_, err := LoadSettings(home, repo)
	if err == nil || !strings.Contains(err.Error(), "[agents] belongs in "+UserConfigPath(home)) {
		t.Fatalf("error = %v", err)
	}
}

func TestAvailableMayListADefinedAgent(t *testing.T) {
	s, err := LoadSettings(writeUserConfig(t, "[agents]\navailable = [\"available\"]\n[agents.define]\navailable = \"other:base\"\n"), "")
	if err != nil || !slices.Equal(s.Agents, []string{"available"}) {
		t.Fatalf("settings = %+v, %v", s, err)
	}
}

func TestClaudeCodexAndCopilotKeepTodaysChairsInAnyOrder(t *testing.T) {
	for _, available := range [][]string{{"claude", "codex", "copilot"}, {"copilot", "claude", "codex"}, {"codex", "copilot", "claude"}, {"agy", "copilot", "codex", "claude"}} {
		chairs := DefaultChairs(available)
		for role, want := range map[string]string{"planner": "claude", "implementer": "codex", "reviewer": "copilot", "gate": "claude", "classifier": "claude"} {
			if chairs[role] != want {
				t.Errorf("%v %s = %q", available, role, chairs[role])
			}
		}
	}
}

func TestWithTwoAgentsTheReviewerIsNotTheImplementersAgent(t *testing.T) {
	for _, first := range BuiltInAgents().Names() {
		for _, second := range BuiltInAgents().Names() {
			if first == second {
				continue
			}
			chairs := DefaultChairs([]string{first, first, second, second})
			for _, role := range []string{"planner", "reviewer", "gate", "classifier"} {
				if chairs[role] != first {
					t.Fatalf("%s %s: %v", first, second, chairs)
				}
			}
			if chairs["implementer"] != second {
				t.Fatalf("chairs = %v", chairs)
			}
		}
	}
}

func TestThreeOtherAgentsFillPlannerImplementerAndReviewerInOrder(t *testing.T) {
	chairs := DefaultChairs([]string{"mine", "agy", "codex", "claude"})
	if chairs["planner"] != "mine" || chairs["implementer"] != "agy" || chairs["reviewer"] != "codex" {
		t.Fatalf("chairs = %v", chairs)
	}
}

func TestConfiguredChairsWinOverDerivedDefaults(t *testing.T) {
	s, err := LoadSettings(writeUserConfig(t, "[agents]\navailable = [\"codex\"]\n[chairs]\nreviewer = \"claude@large\"\n"), "")
	if err != nil || s.Chairs["reviewer"] != "claude@large" || s.Chairs["implementer"] != "codex" {
		t.Fatalf("settings = %+v, %v", s, err)
	}
}

func TestNoTesterIsDefaulted(t *testing.T) {
	if DefaultChairs(BuiltInAgents().Names())["tester"] != "" {
		t.Fatal("defaulted tester")
	}
	if len(DefaultChairs(nil)) != 0 {
		t.Fatal("defaulted without available agents")
	}
}

func TestEffortVariantsCoverACustomBase(t *testing.T) {
	p := DefaultPolicy()
	variants := p.EffortVariants(map[string][]Chair{"plan": {{Provider: "claude_code", Profile: "mine"}}})
	if len(variants) == 0 {
		t.Fatal("no variants")
	}
	for _, v := range variants {
		if v.Base != "mine" {
			t.Fatalf("variant = %+v", v)
		}
	}
}
