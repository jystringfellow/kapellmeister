package loop

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"
)

func TestEachReviewerReceivesOnlyItsOwnFocusUnderDefaultAndFileScores(t *testing.T) {
	for _, custom := range []bool{false, true} {
		e, _, _ := newEngine(t, passing)
		step, _ := DefaultScore().Step("review")
		if custom {
			e.Score = Score{Steps: []ScoreStep{step}, Parts: []Part{{Name: "reviewer", Prompt: map[string]string{"review": "Part marker"}}}}
			step.Brief = ""
		}
		h := handoff{files: []string{"input.md"}}
		plain := e.brief(step, h, "reply.md", Chair{})
		for _, pair := range [][2]string{{"security {reply} %s %d", "style angle"}, {"style angle", "security {reply} %s %d"}} {
			got := e.brief(step, h, "reply.md", Chair{Focus: pair[0]})
			block := "Your focus in this ensemble: " + pair[0] + "\nOther chairs playing the same part may cover other angles; concentrate on yours.\n"
			if !strings.Contains(got, block) || strings.Contains(got, pair[1]) || strings.Contains(got, "%!") {
				t.Fatalf("prompt = %s", got)
			}
			if custom {
				if !strings.Contains(got, "Part marker\n"+block+"Read input.md") {
					t.Fatalf("block position: %s", got)
				}
			} else if got != plain+block {
				t.Fatalf("default block position: %s", got)
			}
		}
	}
}

func TestClassifierAndPlannerReceiveTheirFocus(t *testing.T) {
	e, _, _ := newEngine(t, passing)
	e.Config.Chairs.Classifier.Focus = "classification angle"
	if got := e.classifyBrief("review", "in", "out"); !strings.Contains(got, "classification angle") {
		t.Fatalf("classifier prompt = %s", got)
	}
	step, _ := DefaultScore().Step("plan")
	if got := e.brief(step, handoff{}, "out", Chair{Focus: "planning angle"}); !strings.Contains(got, "planning angle") {
		t.Fatalf("planner prompt = %s", got)
	}
}

func TestPreflightDeduplicatesChairsWithTheSameBindingAndDifferentFocus(t *testing.T) {
	first := Chair{Provider: "provider", Profile: "profile", Model: "model", Tier: "large", Focus: "security"}
	second := first
	second.Focus = "style"
	second.Address = "review.style@band"
	second.Timeout = 1
	other := second
	other.Model = "other"
	got := UniqueChairs(first, second, other)
	if len(got) != 2 || got[0] != first || got[1] != other {
		t.Fatalf("unique chairs = %+v", got)
	}
}

func TestAChairWithoutFocusReceivesTheUnchangedFileScorePrompt(t *testing.T) {
	source := scoreFileSource(t)
	loaded, err := LoadScore("small", source)
	if err != nil {
		t.Fatal(err)
	}
	e, _, _ := newEngine(t, passing)
	e.Score = loaded.Score
	e.Config.Task = "task text"
	step, ok := e.Score.Step("draft")
	if !ok {
		t.Fatal("missing draft step")
	}
	got := e.brief(step, handoff{files: []string{"input.md"}}, "reply.md", Chair{})
	want := fmt.Sprintf(ground, "drafter") + "<task>\ntask text\n</task>\nDraft reply.md.\nRead input.md.\nWrite your reply to reply.md.\n"
	if got != want {
		t.Fatalf("prompt = %q\nwant   = %q", got, want)
	}
	if digest := fmt.Sprintf("%x", sha256.Sum256([]byte(got))); digest != "12f6f8e82ef5125b27d9036781e614e945f3bb72a5f146805ea8afbfdb8a9295" {
		t.Fatalf("prompt digest = %s", digest)
	}
}
