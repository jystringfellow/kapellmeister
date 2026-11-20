package loop

import (
	"reflect"
	"strings"
	"testing"
)

func customTierEngine(t *testing.T, pin string) (*Engine, *fakeStepper) {
	t.Helper()
	loaded, err := LoadScore("small", scoreFileSource(t))
	if err != nil {
		t.Fatal(err)
	}
	e, f, _ := newEngine(t, passing)
	e.Score = loaded.Score
	p := DefaultPolicy()
	p.Tiers["codex"]["medium"] = TierSpec{Model: "chosen-medium", Effort: "high"}
	p.Sizing.Mode = SizingOn
	after := 1
	p.Sizing.EscalateAfter = &after
	e.Config.Policy = &p
	chair := codexChair
	chair.Tier = pin
	e.Config.Chairs.Parts = map[string][]Chair{"drafter": {chair}}
	e.Config.Chairs.Reviewers = []Chair{claudeChair}
	return e, f
}
func TestAnUnpinnedChairOfACustomPartPlaysOnTheMediumTier(t *testing.T) {
	e, f := customTierEngine(t, "")
	run(t, e)
	s := stepNamed(t, f, "draft", 1)
	if s.Chair.Model != "chosen-medium" || s.Chair.Profile != "km-codex-high" || s.Assignment.Tier != "medium" {
		t.Fatalf("draft = %+v", s)
	}
}
func TestAPinnedChairOfACustomPartPlaysOnThePinnedTier(t *testing.T) {
	for _, tier := range []string{"small", "medium", "large", "xlarge"} {
		t.Run(tier, func(t *testing.T) {
			e, f := customTierEngine(t, tier)
			run(t, e)
			s := stepNamed(t, f, "draft", 1)
			if s.Assignment.Tier != tier || s.Chair.Model != e.Config.Policy.Tiers["codex"][tier].Model {
				t.Fatalf("draft = %+v", s)
			}
		})
	}
}
func TestStepsTableForACustomStepIsHonoured(t *testing.T) {
	home := writeUserConfig(t, "[steps.draft]\ntier = 'xlarge'\nmin = 'large'\nmax = 'xlarge'\n")
	settings, err := LoadSettings(home, "")
	if err != nil {
		t.Fatal(err)
	}
	e, f := customTierEngine(t, "")
	e.Config.Policy = &settings.Policy
	run(t, e)
	s := stepNamed(t, f, "draft", 1)
	if s.Assignment.Tier != "xlarge" {
		t.Fatal(s.Assignment)
	}
}
func TestAStepsTableNamingNoStepOfTheScoreIsRefused(t *testing.T) {
	home := writeUserConfig(t, "[steps.missing]\ntier = 'large'\n")
	settings, err := LoadSettings(home, "")
	if err != nil {
		t.Fatal(err)
	}
	e, _ := customTierEngine(t, "")
	e.Config.Policy = &settings.Policy
	_, err = e.Run(t.Context())
	if err == nil || !strings.Contains(err.Error(), "steps.missing: unknown step") {
		t.Fatal(err)
	}
}
func TestACustomProduceStepWithEscalateMovesUpAfterRoundsWithoutApproval(t *testing.T) {
	e, f := customTierEngine(t, "")
	e.Config.MaxRounds = 5
	f.verdicts["inspect"] = []string{"CHANGES", "CHANGES", "CHANGES", "CHANGES"}
	run(t, e)
	for i, tier := range []string{"medium", "large", "xlarge", "xlarge", "xlarge"} {
		if s := stepNamed(t, f, "draft", i+1); s.Assignment.Tier != tier {
			t.Fatalf("round %d = %+v", i+1, s.Assignment)
		}
	}
}
func TestCustomStepsGetBoundsFromTheirKind(t *testing.T) {
	for _, c := range []struct {
		kind     StepKind
		escalate bool
		want     StepPolicy
	}{
		{KindProduce, false, StepPolicy{"medium", "small", "large"}}, {KindProduce, true, StepPolicy{"medium", "small", "xlarge"}}, {KindReview, false, StepPolicy{"medium", "small", "large"}}, {KindDecide, false, StepPolicy{"medium", "medium", "large"}}, {KindTest, false, StepPolicy{"medium", "medium", "large"}},
	} {
		e, _ := customTierEngine(t, "")
		e.Score.Steps[0].Kind = c.kind
		e.Score.Steps[0].Escalate = c.escalate
		e.Config.MaxRounds = 1
		_, _ = e.Run(t.Context())
		if got := e.Config.Policy.Steps["draft"]; !reflect.DeepEqual(got, c.want) {
			t.Fatalf("%s escalate %v = %+v", c.kind, c.escalate, got)
		}
	}
}

func TestBindingAPolicyToAScoreTwiceChangesNothing(t *testing.T) {
	loaded, err := LoadScore("small", scoreFileSource(t))
	if err != nil {
		t.Fatal(err)
	}
	original := DefaultPolicy().withSteps(map[string]StepPolicy{"draft": {Tier: "large"}})
	first, err := original.ForScore(loaded.Score)
	if err != nil {
		t.Fatal(err)
	}
	second, err := first.ForScore(loaded.Score)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatal("binding changed the policy")
	}
	if _, ok := original.Steps["draft"]; ok {
		t.Fatal("binding changed the original policy")
	}
}
func TestTheBuiltInReviewScoreKeepsTheReviewAndGatePolicies(t *testing.T) {
	p := DefaultPolicy()
	bound, err := p.ForScore(reviewScoreForTest(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"review", "gate"} {
		if bound.Steps[key] != p.Steps[key] {
			t.Fatal(bound.Steps)
		}
	}
}
func TestACustomStepsPartialBoundsAreValidatedAfterItsKindSuppliesDefaults(t *testing.T) {
	for _, over := range []StepPolicy{{Min: "xlarge"}, {Tier: "unknown"}, {Max: "small"}} {
		p := DefaultPolicy().withSteps(map[string]StepPolicy{"draft": over})
		loaded, err := LoadScore("small", scoreFileSource(t))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := p.ForScore(loaded.Score); err == nil || !strings.Contains(err.Error(), "steps.draft:") {
			t.Fatal(err)
		}
	}
}
func TestTheBuiltInTestPlanUsesTheTestPolicyTable(t *testing.T) {
	p := DefaultPolicy().withSteps(map[string]StepPolicy{"test-plan": {Tier: "large"}})
	if _, err := p.ForScore(DefaultScore()); err == nil || !strings.Contains(err.Error(), "use [steps.test]") {
		t.Fatal(err)
	}
}
func TestAProduceStepWithoutEscalateKeepsItsTierThroughRejections(t *testing.T) {
	e, f := customTierEngine(t, "")
	e.Score.Steps[0].Escalate = false
	e.Config.MaxRounds = 3
	f.verdicts["inspect"] = []string{"CHANGES", "CHANGES"}
	run(t, e)
	for round := 1; round <= 3; round++ {
		if s := stepNamed(t, f, "draft", round); s.Assignment.Tier != "medium" {
			t.Fatal(s.Assignment)
		}
	}
}

func TestACustomChairsPinIsClampedToItsStepsBounds(t *testing.T) {
	e, f := customTierEngine(t, "xlarge")
	e.Score.Steps[0].Escalate = false
	run(t, e)
	if s := stepNamed(t, f, "draft", 1); s.Assignment.Tier != "large" {
		t.Fatal(s.Assignment)
	}
}
func TestRepositoryOverridesForCustomStepsKeepUnspecifiedUserFields(t *testing.T) {
	home := writeUserConfig(t, "[steps.draft]\ntier = 'large'\nmin = 'medium'\n")
	dir := t.TempDir()
	writeConfig(t, dir, "[steps.draft]\nmax = 'large'\n")
	settings, err := LoadSettings(home, dir)
	if err != nil {
		t.Fatal(err)
	}
	e, f := customTierEngine(t, "")
	e.Config.Policy = &settings.Policy
	run(t, e)
	if s := stepNamed(t, f, "draft", 1); s.Assignment.Tier != "large" || e.Config.Policy.Steps["draft"] != (StepPolicy{"large", "medium", "large"}) {
		t.Fatal(s.Assignment, e.Config.Policy.Steps)
	}
}
