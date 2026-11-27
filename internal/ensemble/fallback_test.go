package ensemble

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jystringfellow/kapellmeister/internal/loop"
)

func TestBindSeatsTheFirstAvailableAgentAndListsTheOnesAfterIt(t *testing.T) {
	s := Spec{Name: "test", Sections: []Section{{Name: "review", Chairs: []ChairSpec{{Name: "lead", Part: "reviewer", Agents: []string{"agy", "copilot", "claude"}}}}}}
	members, err := s.Bind(loop.BuiltInAgents(), []string{"copilot", "claude"})
	if err != nil {
		t.Fatal(err)
	}
	if members[0].Chair.Provider != "copilot_cli" {
		t.Fatalf("first provider = %s", members[0].Chair.Provider)
	}
	first, later := s.Listed("review", "lead", []string{"copilot", "claude"}, loop.BuiltInAgents())
	if first != "copilot" || !slices.Equal(later, []string{"claude"}) {
		t.Fatalf("listed = %q, %v", first, later)
	}
}

func TestASubstituteKeepsTheChairsTierTimeoutAndFocus(t *testing.T) {
	s := Spec{Name: "test", Timeout: time.Hour, Sections: []Section{{Name: "review", Chairs: []ChairSpec{{Name: "lead", Part: "reviewer", Tier: "large", Focus: "security"}}}}}
	m, err := s.Seat(loop.BuiltInAgents(), "review", "lead", "claude")
	if err != nil {
		t.Fatal(err)
	}
	if m.Chair.Tier != "large" || m.Chair.Timeout != time.Hour || m.Chair.Focus != "security" {
		t.Fatalf("chair = %+v", m.Chair)
	}
}

func TestAReviewerNewlyOnTheImplementersProviderIsAnOverlapAndAnOldOneIsNot(t *testing.T) {
	before := []Member{{Section: "build", Name: "dev", Part: "implementer", Chair: loop.Chair{Provider: "a"}}, {Section: "review", Name: "lead", Part: "reviewer", Chair: loop.Chair{Provider: "b"}}}
	after := slices.Clone(before)
	after[1].Chair.Provider = "a"
	changes := []SeatChange{{Address: "review.lead@test"}}
	FindOverlaps("test", before, after, changes)
	if len(changes[0].Overlaps) != 1 || changes[0].Overlaps[0] != "review.lead@test now plays on the same provider as the implementer (a)" {
		t.Fatalf("overlaps = %v", changes[0].Overlaps)
	}
	changes[0].Overlaps = nil
	FindOverlaps("test", after, after, changes)
	if len(changes[0].Overlaps) != 0 {
		t.Fatal(changes)
	}
}

func TestTwoSeatsInASectionThatNewlyShareAProviderAreOneOverlap(t *testing.T) {
	before := []Member{{Section: "review", Name: "lead", Chair: loop.Chair{Provider: "a"}}, {Section: "review", Name: "second", Chair: loop.Chair{Provider: "b"}}}
	after := slices.Clone(before)
	after[1].Chair.Provider = "a"
	changes := []SeatChange{{Address: "review.second@test"}}
	FindOverlaps("test", before, after, changes)
	if len(changes[0].Overlaps) != 1 || changes[0].Overlaps[0] != "review.second@test now plays on the same provider as review.lead@test (a)" {
		t.Fatal(changes)
	}
}

func TestEverySectionAndTheEnsembleThatLostAProviderIsListed(t *testing.T) {
	before := []Member{{Section: "review", Chair: loop.Chair{Provider: "a"}}, {Section: "review", Chair: loop.Chair{Provider: "b"}}, {Section: "build", Chair: loop.Chair{Provider: "a"}}}
	after := slices.Clone(before)
	after[1].Chair.Provider = "a"
	lost := LostProviders(before, after)
	if len(lost) != 2 || lost[0].Section != "review" || lost[1].Section != "" || lost[0].Before != 2 || lost[0].After != 1 || !slices.Equal(lost[0].Blocked, []string{"b"}) {
		t.Fatal(lost)
	}
}

func TestSeatChangesMarshalEmptyTriedAndOverlapsAsArrays(t *testing.T) {
	data, err := json.Marshal(SeatChange{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"tried":[]`) || !strings.Contains(string(data), `"overlaps":[]`) {
		t.Fatal(string(data))
	}
}

func TestSeatLinesMarkReasonsAsUntrustedAndNameLostProviders(t *testing.T) {
	lines := SeatLines([]SeatChange{{Address: "review.lead@test", BlockedAgent: "copilot", Reason: "budget exhausted", SeatedAgent: "claude", Overlaps: []string{"overlap"}}}, []ProvidersLost{{Section: "review", Before: 2, After: 1, Blocked: []string{"copilot_cli"}}, {Before: 3, After: 1, Blocked: []string{"a", "b"}}})
	want := []string{"review.lead@test: copilot was BLOCKED (untrusted text from the agent's pane: budget exhausted); claude takes the seat", "overlap", "section review played with 1 provider instead of 2: copilot_cli was blocked", "the ensemble played with 1 provider instead of 3: a, b were blocked"}
	if !slices.Equal(lines, want) {
		t.Fatalf("got %v", lines)
	}
}

func TestAnImplementerMovingOntoAReviewersProviderRecordsTheOverlapOnItsOwnSeatChange(t *testing.T) {
	before := []Member{{Section: "build", Name: "dev", Part: "implementer", Chair: loop.Chair{Provider: "a"}}, {Section: "review", Name: "lead", Part: "reviewer", Chair: loop.Chair{Provider: "b"}}}
	after := slices.Clone(before)
	after[0].Chair.Provider = "b"
	changes := []SeatChange{{Address: "build.dev@test"}}
	FindOverlaps("test", before, after, changes)
	if !slices.Equal(changes[0].Overlaps, []string{"review.lead@test now plays on the same provider as the implementer (b)"}) {
		t.Fatal(changes)
	}
}
