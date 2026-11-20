package ensemble

import (
	"github.com/jystringfellow/kapellmeister/internal/loop"
	"strings"
	"testing"
)

func TestTheReviewScoreSeatsReviewersGateAndClassifierAndRefusesOtherParts(t *testing.T) {
	loaded, err := loop.LoadScore("review", loop.ScoreSource{Home: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	members := []Member{{Name: "first", Part: "reviewer"}, {Name: "second", Part: "reviewer"}, {Name: "third", Part: "gate"}, {Name: "fourth", Part: "classifier"}}
	chairs, err := ChairsFor(loaded.Score, members)
	if err != nil || len(chairs.Reviewers) != 2 {
		t.Fatalf("chairs = %+v, %v", chairs, err)
	}
	for _, part := range []string{"planner", "implementer", "tester"} {
		_, err := ChairsFor(loaded.Score, append(append([]Member{}, members...), Member{Part: part}))
		if err == nil || !strings.Contains(err.Error(), `plays unknown part "`+part+`"`) {
			t.Fatalf("error = %v", err)
		}
	}
	for _, part := range []string{"reviewer", "gate", "classifier"} {
		var reduced []Member
		for _, m := range members {
			if m.Part != part {
				reduced = append(reduced, m)
			}
		}
		_, err := ChairsFor(loaded.Score, reduced)
		if err == nil || !strings.Contains(err.Error(), "no "+part) {
			t.Fatalf("error = %v", err)
		}
	}
}
