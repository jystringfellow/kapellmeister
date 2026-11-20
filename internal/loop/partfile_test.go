package loop

import (
	"strings"
	"testing"
)

func TestAFilePartIsBriefedPreambleTaskLessonsPartTextThenTheFixedBlock(t *testing.T) {
	for _, kind := range []StepKind{KindProduce, KindReview, KindDecide, KindTest} {
		t.Run(string(kind), func(t *testing.T) {
			e, _, _ := newEngine(t, passing)
			e.Config.Task = "Task marker"
			e.Config.ScratchDir = "scratch-path"
			e.Score = Score{Steps: []ScoreStep{{Name: "draft", Part: "drafter", Kind: kind}}, Parts: []Part{{Name: "drafter", Prompt: map[string]string{"draft": "Part marker {plan} {reply} {inputs} {verify} {check} {scratch}"}}}}
			e.lessons = map[lessonKey]chairLessons{{"drafter", ""}: {text: "Lesson marker"}}
			h := handoff{files: []string{"input{reply}"}, inputs: []stepReply{{files: []string{"input{reply}"}}}, verifyPath: "verify-path"}
			prompt := e.brief(ScoreStep{Name: "draft", Part: "drafter", Kind: kind}, h, "reply-path", Chair{})
			last := -1
			for _, marker := range []string{"read and follow every agent instruction", "Task marker", "Lesson marker", "Part marker input{reply} reply-path", "Read input{reply}", "Write your reply to reply-path."} {
				index := strings.Index(strings.ToLower(prompt), strings.ToLower(marker))
				if index <= last {
					t.Fatalf("missing or out of order %q: %s", marker, prompt)
				}
				last = index
			}
			contract := "Put APPROVE or CHANGES alone on the last line of the reply file.\n"
			if (kind != KindProduce) != strings.HasSuffix(prompt, contract) {
				t.Fatalf("contract: %s", prompt)
			}
		})
	}
}

func TestABuiltInPartInAFileScoreGetsTodaysPromptForItsKind(t *testing.T) {
	e, _, _ := newEngine(t, passing)
	h := handoff{inputs: []stepReply{{files: []string{"plan.md"}}, {files: []string{"summary.md"}}, {files: []string{"review.md"}}}, files: []string{"plan.md", "summary.md", "review.md"}}
	for _, original := range DefaultScore().Steps {
		t.Run(original.Brief, func(t *testing.T) {
			text := "[[steps]]\nname = \"draft\"\nkind = \"" + string(original.Kind) + "\"\npart = \"" + original.Part + "\"\n"
			if original.Writes == "reply" {
				text += "writes = \"reply\"\n"
			}
			if len(original.Reads) > 0 {
				text += "reads = [\"draft\", \"draft\", \"draft\", \"draft\"]\n"
			}
			switch original.Kind {
			case KindProduce:
				text += "next = {done = \"approved\"}\n"
			case KindReview, KindDecide:
				text += "next = {approve = \"approved\", changes = \"changes_requested\"}\n"
			case KindTest:
				text += "next = {approve = \"approved\", changes = \"changes_requested\", absent = \"approved\"}\n"
			}
			score, err := decodeScore(text, "score.toml", "small")
			if err != nil {
				t.Fatal(err)
			}
			loaded := score.Steps[0]
			a := e.brief(original, h, "own-reply.md", Chair{})
			b := e.brief(loaded, h, "own-reply.md", Chair{})
			if a != b {
				t.Fatalf("brief %s differs:\n%s\n%s", original.Brief, a, b)
			}
		})
	}
}

func TestAPartThatPlaysTwoStepsReceivesCommonTextAndOnlyItsOwnSection(t *testing.T) {
	prompts, err := decodePart("Common\n## step: draft\nDraft {reply}\n## step: inspect\nInspect {inputs}", "part.md", []ScoreStep{{Name: "draft"}, {Name: "inspect"}})
	if err != nil {
		t.Fatal(err)
	}
	if prompts["draft"] != "Common\n\nDraft {reply}" || prompts["inspect"] != "Common\n\nInspect {inputs}" {
		t.Fatalf("prompts = %v", prompts)
	}
}
