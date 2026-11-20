package loop

import (
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestTheDefaultScoreNamesEveryStepTheEnginePlays(t *testing.T) {
	score := DefaultScore()
	var names []string
	for _, step := range score.Steps {
		names = append(names, step.Name)
		part, ok := score.Part(step.Part)
		if !ok || part.Prompt[step.Name] == "" {
			t.Fatalf("step %q has no part prompt", step.Name)
		}
	}
	if want := []string{"plan", "test-plan", "implement", "review", "gate", "test"}; !slices.Equal(names, want) {
		t.Fatalf("steps = %v, want %v", names, want)
	}
	if score.Name != "default" || score.Rounds != 3 || DefaultSettings().Rounds != score.Rounds {
		t.Fatalf("default score = %+v", score)
	}
}

func TestTheDefaultScoreMapsEachStepToTodaysPart(t *testing.T) {
	want := map[string]string{
		"plan": "planner", "test-plan": "tester", "implement": "implementer",
		"review": "reviewer", "gate": "gate", "test": "tester", "lessons": "gate",
	}
	if !reflect.DeepEqual(stepRoles, want) {
		t.Fatalf("step parts = %v, want %v", stepRoles, want)
	}
	score := DefaultScore()
	for step, part := range want {
		if got := score.StepPart(step); got != part {
			t.Errorf("StepPart(%q) = %q, want %q", step, got, part)
		}
	}
	for step, part := range map[string]string{"classify-gate": "classifier", "unknown": "unknown", "plan": "planner"} {
		if got := PartOf(step); got != part {
			t.Errorf("PartOf(%q) = %q, want %q", step, got, part)
		}
	}
}

func TestTheDefaultScoreRoutesEveryExitToAStepOrAnEnding(t *testing.T) {
	score := DefaultScore()
	targets := map[string]bool{"approved": true, "changes_requested": true}
	for _, step := range score.Steps {
		targets[step.Name] = true
	}
	background := 0
	for _, step := range score.Steps {
		if step.Background {
			background++
			if step.Name != "test-plan" || step.Kind != KindProduce || len(step.Exits) != 0 {
				t.Fatalf("background step = %+v", step)
			}
			continue
		}
		var exits []string
		for _, exit := range step.Exits {
			exits = append(exits, exit.Name)
			if !targets[exit.To] {
				t.Errorf("%s exit %q leads to unknown target %q", step.Name, exit.Name, exit.To)
			}
		}
		want, ok := map[StepKind][]string{
			KindProduce: {"done"}, KindReview: {"approve", "changes"},
			KindDecide: {"approve", "changes"}, KindTest: {"approve", "changes", "absent"},
		}[step.Kind]
		if !ok || !slices.Equal(exits, want) {
			t.Errorf("%s exits = %v, want %v", step.Name, exits, want)
		}
	}
	if background != 1 {
		t.Fatalf("background steps = %d, want 1", background)
	}
}

func TestTheDefaultScoreHasTheseParts(t *testing.T) {
	score := DefaultScore()
	required := []string{"planner", "implementer", "reviewer", "gate", "classifier"}
	optional := []string{"tester"}
	all := []string{"planner", "implementer", "reviewer", "gate", "classifier", "tester"}
	if !slices.Equal(score.Roles(), all) || !slices.Equal(AllRoles(), all) {
		t.Fatalf("parts = %v; all roles = %v", score.Roles(), AllRoles())
	}
	if !slices.Equal(score.Required(), required) || !slices.Equal(ChairRoles, required) {
		t.Fatalf("required = %v; chair roles = %v", score.Required(), ChairRoles)
	}
	if !slices.Equal(score.Optional(), optional) || !slices.Equal(OptionalRoles, optional) {
		t.Fatalf("optional = %v; optional roles = %v", score.Optional(), OptionalRoles)
	}
	for _, part := range score.Parts {
		want := MemoryFresh
		if part.Name == "implementer" {
			want = MemoryResume
		}
		if part.Memory != want {
			t.Errorf("%s memory = %q, want %q", part.Name, part.Memory, want)
		}
	}
	var testerSteps []string
	for _, step := range score.StepsOf("tester") {
		testerSteps = append(testerSteps, step.Name)
	}
	if !slices.Equal(testerSteps, []string{"test-plan", "test"}) {
		t.Fatalf("tester steps = %v", testerSteps)
	}
	if steps := score.StepsOf("classifier"); len(steps) != 0 {
		t.Fatalf("classifier steps = %v", steps)
	}
}

func TestOnlyTheImplementerResumesAndReviewIsAlwaysFresh(t *testing.T) {
	score := DefaultScore()
	for _, step := range score.Steps {
		if got := score.Resumes(step.Name); got != (step.Name == "implement") {
			t.Errorf("Resumes(%q) = %t", step.Name, got)
		}
	}
	for i := range score.Parts {
		if score.Parts[i].Name == "reviewer" {
			score.Parts[i].Memory = MemoryResume
		}
	}
	if score.Resumes("review") || score.Resumes("unknown") {
		t.Fatal("review and unknown steps must start fresh")
	}
}

func TestTheOutlineListsVerifyAndStrictOnlyWhenTheRunHasThemToRun(t *testing.T) {
	kinds := func(verify, strict bool) string {
		var out []string
		for _, step := range DefaultScore().Outline(verify, strict).Steps {
			if step.Part == "" {
				out = append(out, step.Kind)
			}
		}
		return strings.Join(out, " ")
	}
	for _, c := range []struct {
		verify, strict bool
		want           string
	}{{false, false, ""}, {true, false, "verify verify"}, {true, true, "verify verify strict"}, {false, true, "strict"}} {
		if got := kinds(c.verify, c.strict); got != c.want {
			t.Errorf("verify %t strict %t: %q, want %q", c.verify, c.strict, got, c.want)
		}
	}
}
