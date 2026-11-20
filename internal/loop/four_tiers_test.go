package loop

import (
	"context"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func assertShippedTiers(t *testing.T, provider string, want map[string]TierSpec) {
	t.Helper()
	if got := DefaultPolicy().Tiers[provider]; !reflect.DeepEqual(got, want) {
		t.Fatalf("tiers = %#v, want %#v", got, want)
	}
}
func TestTheShippedClaudeTiersHaveAlignedEfforts(t *testing.T) {
	assertShippedTiers(t, "claude_code", map[string]TierSpec{
		"small": {"claude-haiku-5-5", "medium"}, "medium": {"claude-sonnet-5-5", "high"}, "large": {"claude-opus-5-5", "high"}, "xlarge": {"claude-fable-5-2", "high"},
	})
}
func TestTheShippedCodexTiersHaveAlignedEfforts(t *testing.T) {
	assertShippedTiers(t, "codex", map[string]TierSpec{
		"small": {"gpt-6-luna", "medium"}, "medium": {"gpt-6.1-terra", "high"}, "large": {"gpt-6.1-sol", "high"}, "xlarge": {"gpt-6-astra", "high"},
	})
}
func TestTheShippedCopilotTiersHaveNoEffortSetting(t *testing.T) {
	assertShippedTiers(t, "copilot_cli", map[string]TierSpec{
		"small": {Model: "claude-haiku-5.5"}, "medium": {Model: "claude-sonnet-5.5"}, "large": {Model: "claude-opus-5.5"}, "xlarge": {Model: "claude-fable-5.2"},
	})
}
func TestTheShippedAntigravityTiersCarryEffortInTheirModelIDs(t *testing.T) {
	assertShippedTiers(t, "antigravity_cli", map[string]TierSpec{
		"small": {Model: "gemini-3.8-flash-high"}, "medium": {Model: "gemini-4-hydrogen-high"}, "large": {Model: "gemini-4-neon-high"}, "xlarge": {Model: "gemini-4-argon-high"},
	})
}
func TestAThreeTierConfigLoadsUnchangedAndTakesTheFourthDefault(t *testing.T) {
	home := writeUserConfig(t, `[tiers.codex]
small = { model = "first", effort = "inherit" }
medium = { model = "second", effort = "medium" }
large = { model = "third", effort = "high" }
[steps.implement]
max = "large"
`)
	s, err := LoadSettings(home, "")
	if err != nil {
		t.Fatal(err)
	}
	for tier, model := range map[string]string{"small": "first", "medium": "second", "large": "third", "xlarge": "gpt-6-astra"} {
		if s.Policy.Tiers["codex"][tier].Model != model {
			t.Fatalf("tiers = %+v", s.Policy.Tiers["codex"])
		}
	}
	if s.Policy.Steps["implement"].Max != "large" {
		t.Fatal(s.Policy.Steps)
	}
}
func TestAPartialTiersOverrideKeepsWhatItSetsAndDefaultsTheRest(t *testing.T) {
	home := writeUserConfig(t, "[tiers.codex]\nlarge = { model = 'chosen', effort = 'inherit' }\n")
	s, err := LoadSettings(home, "")
	if err != nil {
		t.Fatal(err)
	}
	if s.Policy.Tiers["codex"]["large"] != (TierSpec{"chosen", "inherit"}) || s.Policy.Tiers["codex"]["xlarge"] != (TierSpec{"gpt-6-astra", "high"}) {
		t.Fatal(s.Policy.Tiers)
	}
}
func TestOnlyTheImplementDefaultMaximumBecomesXlarge(t *testing.T) {
	want := map[string]StepPolicy{
		"implement": {"medium", "small", "xlarge"}, "plan": {"medium", "medium", "large"}, "review": {"medium", "small", "large"}, "gate": {"medium", "medium", "large"}, "test": {"medium", "medium", "large"}, "classify": {"small", "small", "small"},
	}
	if got := DefaultPolicy().Steps; !reflect.DeepEqual(got, want) {
		t.Fatalf("steps = %+v", got)
	}
}
func TestEscalationReachesXlargeOnTheImplementStepAndStopsThere(t *testing.T) {
	e, f, records := tieredEngine(t, SizingOff)
	p := DefaultPolicy()
	after := 1
	p.Sizing.EscalateAfter = &after
	p.Sizing.Mode = SizingOff
	e.Config.Policy = &p
	e.Config.MaxRounds = 5
	f.verdicts["review"] = []string{"CHANGES", "CHANGES", "CHANGES", "CHANGES"}
	run(t, e)
	for i, tier := range []string{"medium", "large", "xlarge", "xlarge", "xlarge"} {
		s := stepNamed(t, f, "implement", i+1)
		if s.Assignment.Tier != tier || s.Chair.Model != p.Tiers["codex"][tier].Model {
			t.Fatalf("round %d = %+v", i+1, s)
		}
	}
	if got := recordsOf(t, records, "escalate"); len(got) != 2 {
		t.Fatalf("escalations = %+v", got)
	}
}
func TestAStepWhoseMaximumIsLargeNeverGetsXlarge(t *testing.T) {
	p := DefaultPolicy()
	for _, key := range []string{"implement", "review", "gate"} {
		step := p.Steps[key]
		step.Max = "large"
		p.Steps[key] = step
		_, a := p.Chair(key, codexChair, "", 20)
		if a.Tier != "large" {
			t.Fatal(a)
		}
	}
}
func TestSizingRefusesXlargeAndKeepsThreeAnswers(t *testing.T) {
	if _, _, err := PlannerSize("Size: xlarge"); err == nil || !strings.Contains(err.Error(), "no Size line") {
		t.Fatal(err)
	}
	if _, _, err := CommandSize(context.Background(), `echo '{"size":"xlarge"}'`, t.TempDir(), SizeRequest{}); err == nil || !strings.Contains(err.Error(), "not one of small, medium, large") {
		t.Fatal(err)
	}
	e, f, records := tieredEngine(t, SizingOn)
	planSays(f, "Size: xlarge")
	run(t, e)
	if s := stepNamed(t, f, "implement", 1); s.Assignment.Source != SourceDefault {
		t.Fatal(s.Assignment)
	}
	if d := recordsOf(t, records, "size")[0].Decision; d.Applied || d.Error == "" {
		t.Fatal(d)
	}
}

func TestAnUnshippedProviderNeedsXlargeOnlyWhenARunAsksForIt(t *testing.T) {
	for _, rounds := range []int{2, 3} {
		t.Run(strconv.Itoa(rounds), func(t *testing.T) {
			e, f, _ := tieredEngine(t, SizingOff)
			p := DefaultPolicy()
			after := 1
			p.Sizing.EscalateAfter = &after
			p.Sizing.Mode = SizingOff
			p.Tiers["other"] = map[string]TierSpec{"small": {Model: "first"}, "medium": {Model: "second"}, "large": {Model: "third"}}
			e.Config.Policy = &p
			e.Config.MaxRounds = rounds
			e.Config.Chairs.Implementer = Chair{Provider: "other", Profile: "base"}
			f.verdicts["review"] = []string{"CHANGES", "CHANGES", "CHANGES"}
			result := run(t, e)
			if rounds == 2 && result.Outcome != RoundsExhausted {
				t.Fatal(result)
			}
			if rounds == 3 && (result.Outcome != Failed || !strings.Contains(result.Reason, "[tiers.other] has no xlarge tier to pin against")) {
				t.Fatal(result)
			}
		})
	}
}
func TestEffortProfilesIncludeTheXlargeTierWhenAStepCanReachIt(t *testing.T) {
	p := DefaultPolicy()
	p.Tiers["codex"]["xlarge"] = TierSpec{Model: "top", Effort: "ultra"}
	variants := p.EffortVariants(map[string][]Chair{"implement": {codexChair}})
	if !reflect.DeepEqual(variants, []EffortVariant{{"codex", "km-codex", "medium"}, {"codex", "km-codex", "high"}, {"codex", "km-codex", "ultra"}}) {
		t.Fatal(variants)
	}
}
