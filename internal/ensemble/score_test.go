package ensemble

import (
	"reflect"
	"strings"
	"testing"

	"github.com/jystringfellow/kapellmeister/internal/loop"
)

func TestAnEnsembleFileNamesItsScoreWithATopLevelKey(t *testing.T) {
	home, wt := repoEnsemble(t, "small", `score = "small"
[sections.strings.chairs.first]
part = "drafter"
agents = ["claude"]
`)
	spec := find(t, "small", home, wt)
	if spec.Score != "small" {
		t.Fatalf("score = %q", spec.Score)
	}
	writeEnsemble(t, home+"/ensembles", "small", "score = \"../other\"\n")
	_, _, err := Find("small", home, wt, loop.BuiltInAgents())
	if err == nil || !strings.Contains(err.Error(), `score: invalid name "../other"`) {
		t.Fatalf("error = %v", err)
	}
}
func TestChairsForChecksTheScoreAndSeatsCustomParts(t *testing.T) {
	score := loop.Score{Classifier: "classifier", Parts: []loop.Part{{Name: "drafter"}, {Name: "inspector"}, {Name: "classifier"}}, Steps: []loop.ScoreStep{{Name: "draft", Part: "drafter", Kind: loop.KindProduce}, {Name: "inspect", Part: "inspector", Kind: loop.KindReview}}}
	members := []Member{{Name: "first", Part: "drafter", Chair: loop.Chair{Profile: "draft"}}, {Name: "second", Part: "inspector", Chair: loop.Chair{Profile: "inspect"}}, {Name: "third", Part: "inspector", Chair: loop.Chair{Profile: "inspect-again"}}, {Name: "fourth", Part: "classifier", Chair: loop.Chair{Profile: "classify"}}}
	chairs, err := ChairsFor(score, members)
	if err != nil {
		t.Fatal(err)
	}
	if len(chairs.Of("inspector")) != 2 || chairs.Of("drafter")[0].Profile != "draft" {
		t.Fatalf("chairs = %+v", chairs)
	}
	for _, c := range []struct {
		members []Member
		reason  string
	}{
		{members[1:], "ensemble seats no drafter; the score needs one"},
		{members[:3], "ensemble seats no classifier; step inspect needs one"},
		{append(append([]Member{}, members...), Member{Part: "stranger"}), `ensemble chair . plays unknown part "stranger"`},
		{append(append([]Member{}, members...), members[0]), "ensemble seats 2 drafters (.first, .first); the score has one"},
	} {
		_, err := ChairsFor(score, c.members)
		if err == nil || err.Error() != c.reason {
			t.Fatalf("error = %v, want %s", err, c.reason)
		}
	}
}
func TestChairsOfIsChairsForTheDefaultScore(t *testing.T) {
	home, wt := repoEnsemble(t, "thorough", thorough)
	members := bind(t, find(t, "thorough", home, wt), everyAgent)
	a, ea := ChairsOf(members)
	b, eb := ChairsFor(loop.DefaultScore(), members)
	if !reflect.DeepEqual(a, b) || ea != eb {
		t.Fatalf("chairs differ: %+v, %+v, %v, %v", a, b, ea, eb)
	}
}

func TestAnExplicitlyEmptyScoreNameIsRefused(t *testing.T) {
	home, wt := repoEnsemble(t, "empty", `score = ""`)
	_, _, err := Find("empty", home, wt, loop.BuiltInAgents())
	if err == nil || !strings.Contains(err.Error(), `score: invalid name ""`) {
		t.Fatalf("error = %v", err)
	}
}
func TestAProduceOnlyScoreNeedsNoClassifier(t *testing.T) {
	score := loop.Score{Classifier: "classifier", Parts: []loop.Part{{Name: "drafter"}, {Name: "classifier", Optional: true}}, Steps: []loop.ScoreStep{{Name: "draft", Part: "drafter", Kind: loop.KindProduce}}}
	chairs, err := ChairsFor(score, []Member{{Part: "drafter", Chair: loop.Chair{Provider: "p"}}})
	if err != nil || len(chairs.Of("drafter")) != 1 {
		t.Fatalf("chairs = %+v, error = %v", chairs, err)
	}
}

func TestSeveralChairsCanPlayAParallelReplyPart(t *testing.T) {
	for _, part := range []string{"scout", "planner"} {
		score := loop.Score{Parts: []loop.Part{{Name: part}}, Steps: []loop.ScoreStep{{Name: "research", Part: part, Kind: loop.KindProduce, Chairs: loop.ChairsAll}}}
		members := []Member{{Part: part, Chair: loop.Chair{Profile: "one"}}, {Part: part, Chair: loop.Chair{Profile: "two"}}, {Part: part, Chair: loop.Chair{Profile: "three"}}}
		chairs, err := ChairsFor(score, members)
		if err != nil || len(chairs.Of(part)) != 3 {
			t.Fatalf("chairs = %+v, error = %v", chairs, err)
		}
		score.Steps[0].Chairs = ""
		if _, err := ChairsFor(score, members); err == nil || !strings.Contains(err.Error(), "the score has one") {
			t.Fatalf("error = %v", err)
		}
	}
}
