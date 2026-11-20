package loop

import (
	"cmp"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func copiedDefault(t *testing.T, text string) Score {
	t.Helper()
	home := t.TempDir()
	scoreFileWrite(t, home, "scores/copy.toml", text)
	loaded, err := LoadScore("copy", ScoreSource{Home: home})
	if err != nil {
		t.Fatal(err)
	}
	return loaded.Score
}

func TestScoreShowDefaultLoadsAsAFileScoreAndPlaysTheSameStepsWithTheSamePrompts(t *testing.T) {
	e, f, _ := scoreEngine(t)
	if res := run(t, e); res.Outcome != Approved {
		t.Fatal(res)
	}
	other, g, _ := scoreEngine(t)
	other.Score = copiedDefault(t, DefaultScore().File())
	if res := run(t, other); res.Outcome != Approved {
		t.Fatal(res)
	}
	a, b := scoreSteps(f), scoreSteps(g)
	if len(a) != len(b) {
		t.Fatalf("steps=%d, %d", len(a), len(b))
	}
	for i, s := range a {
		want, got := b[i], s
		if got.Name != want.Name || got.Round != want.Round || got.Chair != want.Chair || got.ReplyPath != want.ReplyPath || got.KeepTerminal != want.KeepTerminal || (got.ReuseTerminal == "") != (want.ReuseTerminal == "") {
			t.Fatalf("steps differ: %+v / %+v", got, want)
		}
		if scorePlaces(e, got.Prompt) != scorePlaces(other, want.Prompt) {
			t.Fatalf("prompt differs for %s:\n%s\n%s", s.Name, got.Prompt, want.Prompt)
		}
	}
}

func TestACopyOfTheDefaultScoreDiffersOnlyInSizingLessonsTheBackgroundTierAliasAndPartOrder(t *testing.T) {
	built := DefaultScore()
	copy := copiedDefault(t, built.File())
	if copy.LessonsBy != "" {
		t.Fatal(copy.LessonsBy)
	}
	for i, want := range built.Steps {
		want.Sizes, want.Sized, want.Tier = false, false, ""
		got := copy.Steps[i]
		for _, step := range []*ScoreStep{&got, &want} {
			if len(step.Exits) > 0 {
				step.Exits = append([]Exit(nil), step.Exits...)
				slices.SortFunc(step.Exits, func(a, b Exit) int { return cmp.Compare(a.Name, b.Name) })
			}
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("step=%+v want=%+v", got, want)
		}
	}
	for _, want := range built.Parts {
		got, ok := copy.Part(want.Name)
		if !ok || !reflect.DeepEqual(got, want) {
			t.Fatalf("part=%+v want=%+v", got, want)
		}
	}
}

func TestACopyOfTheDefaultScoreWithAFreshImplementerNoLongerKeepsItsTerminal(t *testing.T) {
	e, f, _ := scoreEngine(t)
	e.Score = copiedDefault(t, strings.Replace(DefaultScore().File(), "[parts.implementer]\nmemory = \"resume\"", "[parts.implementer]\nmemory = \"fresh\"", 1))
	if res := run(t, e); res.Outcome != Approved {
		t.Fatal(res)
	}
	for _, s := range f.steps {
		if s.Name == "implement" && (s.KeepTerminal || s.ReuseTerminal != "") {
			t.Fatalf("step=%+v", s)
		}
	}
}

func TestAResumedAndAFreshRunOfAPartReceiveTheSamePrompts(t *testing.T) {
	e, f := memoryRounds(t)
	other, g := memoryRounds(t)
	other.Score.Parts[0].Memory = MemoryFresh
	run(t, e)
	run(t, other)
	for i, s := range f.steps {
		if scorePlaces(e, s.Prompt) != scorePlaces(other, g.steps[i].Prompt) {
			t.Fatalf("prompt differs for %s", s.Name)
		}
	}
}
