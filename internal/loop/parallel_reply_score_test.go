package loop

import (
	"reflect"
	"strings"
	"testing"
)

func TestParallelReplyScoreAcceptsAllAndRejectsInvalidUses(t *testing.T) {
	cases := []struct{ name, extra, kind, writes, part, want string }{
		{"all", `chairs = "all"`, "produce", `writes = "reply"`, "", ""},
		{"unknown", `chairs = "other"`, "produce", `writes = "reply"`, "", `chairs "other" (want first or all)`},
		{"worktree", `chairs = "all"`, "produce", "", "", `chairs = all needs writes = reply: two chairs would edit one worktree`},
		{"review", `chairs = "all"`, "review", "", "", `chairs = all is not for review steps: review already plays every chair of its part`},
		{"decide", `chairs = "all"`, "decide", "", "", `chairs = all is not for decide or test steps: each is one voice`},
		{"test", `chairs = "all"`, "test", "", "", `chairs = all is not for decide or test steps: each is one voice`},
		{"background", "chairs = \"all\"\nbackground = true", "produce", `writes = "reply"`, "", `chairs = all cannot be a background step`},
		{"resume", `chairs = "all"`, "produce", `writes = "reply"`, "[parts.scout]\nmemory = \"resume\"\n", `chairs = all cannot play part scout with memory = resume: every chair plays in a fresh terminal`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			text := tc.part + "\n[[steps]]\nname = \"research\"\nkind = \"" + tc.kind + "\"\npart = \"scout\"\n" + tc.writes + "\n" + tc.extra + "\nnext = { done = \"approved\" }\n"
			score, err := decodeScore(text, "example.toml", "example")
			if tc.want != "" {
				if err == nil || !strings.Contains(err.Error(), "example.toml: step research: "+tc.want) {
					t.Fatalf("error = %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(score.File(), `chairs = "all"`) {
				t.Fatal(score.File())
			}
		})
	}
}

func TestBuiltInPlannerNamesFilesItReads(t *testing.T) {
	e, _ := otherScoreEngine(t)
	prompt := e.brief(ScoreStep{Brief: "plan"}, handoff{files: []string{"research-1.md", "research-2.md"}}, "plan.md", Chair{})
	if !strings.Contains(prompt, "Read research-1.md, research-2.md.") {
		t.Fatal(prompt)
	}
}

func TestFirstChairAndAnAbsentChairsKeyDecodeTheSameWay(t *testing.T) {
	text := `[[steps]]
name = "research"
kind = "produce"
part = "scout"
writes = "reply"
next = { done = "approved" }
`
	first, err := decodeScore(text+"chairs = \"first\"\n", "score.toml", "example")
	if err != nil {
		t.Fatal(err)
	}
	implicit, err := decodeScore(text, "score.toml", "example")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, implicit) {
		t.Fatalf("first = %+v, implicit = %+v", first, implicit)
	}
	if strings.Contains(DefaultScore().File(), "chairs = ") {
		t.Fatal("default score gained chairs key")
	}
	roundtrip, err := decodeScore(implicit.File(), "score.toml", "example")
	if err != nil || !reflect.DeepEqual(implicit.Steps, roundtrip.Steps) {
		t.Fatalf("roundtrip = %+v, error = %v", roundtrip, err)
	}
}
