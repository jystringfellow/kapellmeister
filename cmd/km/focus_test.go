package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/jystringfellow/kapellmeister/internal/ensemble"
	"github.com/jystringfellow/kapellmeister/internal/loop"
)

func TestStatusAndRunSeatingRecordFocusOnlyForChairsThatHaveIt(t *testing.T) {
	p := ensembleRun{Members: []ensemble.Member{{Section: "review", Name: "security", Part: "reviewer", Chair: loop.Chair{Focus: "security"}}, {Section: "review", Name: "style", Part: "reviewer"}}}
	seating := recordSeating(p, loop.DefaultScore())
	data, err := json.Marshal(seating)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"focus":"security"`) || strings.Count(string(data), `"focus"`) != 1 {
		t.Fatalf("seating JSON = %s", data)
	}
}

func TestReseatingWithAReplacementAgentKeepsTheChairsFocus(t *testing.T) {
	original := ensemble.Member{Section: "review", Name: "security", Part: "reviewer", Chair: loop.Chair{Provider: "old", Profile: "old", Address: "review.security@band", Focus: "security"}}
	got, err := prepareMember(context.Background(), nil, loop.Policy{}, loop.BuiltInAgents(), "codex", original)
	if err != nil {
		t.Fatal(err)
	}
	if got.Chair.Focus != original.Chair.Focus || got.Chair.Address != original.Chair.Address || got.Chair.Provider == original.Chair.Provider {
		t.Fatalf("reseated chair = %+v", got.Chair)
	}
}
