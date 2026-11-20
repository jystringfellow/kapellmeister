package main

import (
	"github.com/jystringfellow/kapellmeister/internal/ensemble"
	"github.com/jystringfellow/kapellmeister/internal/loop"
	"path/filepath"
	"testing"
)

func TestStatusJSONShowsAFreshChairsTerminalWhileItPlays(t *testing.T) {
	root, state := statusDataFixture(t)
	writeFile(t, filepath.Join(root, "data", "telemetry.jsonl"), "")
	playing := state["playing"].([]any)
	playing[0].(map[string]any)["terminal"] = "first"
	saveStatusData(t, root, state)
	chairs := dataChairs(t, statusDataRow(t, root))
	for i, want := range map[int]string{1: "first", 2: "second", 3: ""} {
		if chairs[i]["terminal"] != want || chairs[i]["state"] != "playing" {
			t.Errorf("chair = %v, want playing with terminal %q", chairs[i], want)
		}
	}
	for _, step := range statusDataRow(t, root)["playing"].([]any) {
		s := step.(map[string]any)
		if s["chair_name"] == "one" && s["terminal"] != "first" {
			t.Errorf("playing step = %v", s)
		}
	}
}

func TestStatusJSONClearsAFreshChairsTerminalOnceItsStepEnds(t *testing.T) {
	root, state := statusDataFixture(t)
	state["playing"] = []any{}
	saveStatusData(t, root, state)
	writeFile(t, filepath.Join(root, "data", "telemetry.jsonl"), `{"step":"audit","round":2,"chair":{"address":"winds.one@voices"},"terminal_id":"released","kept":false}`+"\n"+`{"step":"verdict-audit","round":2,"verdict":"APPROVE"}`+"\n")
	chair := dataChairs(t, statusDataRow(t, root))[1]
	if chair["terminal"] != "" || chair["state"] != "done" {
		t.Fatal(chair)
	}
}

func TestStatusJSONKeepsAResumedChairsTerminalAfterItsStepEnds(t *testing.T) {
	root, state := statusDataFixture(t)
	state["playing"] = []any{}
	saveStatusData(t, root, state)
	chair := dataChairs(t, statusDataRow(t, root))[0]
	if chair["terminal"] != "pen-terminal" || chair["memory"] != "resume" {
		t.Fatal(chair)
	}
}

func TestStatusJSONShowsAPlayingChairWithoutARecordedTerminal(t *testing.T) {
	root, state := statusDataFixture(t)
	writeFile(t, filepath.Join(root, "data", "telemetry.jsonl"), "")
	for _, step := range state["playing"].([]any) {
		delete(step.(map[string]any), "terminal")
	}
	saveStatusData(t, root, state)
	chairs := dataChairs(t, statusDataRow(t, root))
	for _, chair := range chairs[1:4] {
		if chair["terminal"] != "" || chair["state"] != "playing" {
			t.Fatal(chair)
		}
	}
}

func TestStatusJSONClearsAResumedChairsOldTerminalWhileItsFreshFallbackPlays(t *testing.T) {
	root, state := statusDataFixture(t)
	state["playing"] = []any{map[string]any{"step": "draft", "round": 2, "chair": "strings.pen@voices"}}
	saveStatusData(t, root, state)
	chair := dataChairs(t, statusDataRow(t, root))[0]
	if chair["terminal"] != "" || chair["memory"] != "resume" {
		t.Fatal(chair)
	}
}

func TestStatusSeatingUsesEachPartsMemoryAndAlwaysKeepsReviewPartsFresh(t *testing.T) {
	score := loop.Score{Parts: []loop.Part{{Name: "scout", Memory: loop.MemoryResume}, {Name: "judge", Memory: loop.MemoryFresh}, {Name: "auditor", Memory: loop.MemoryResume}}, Steps: []loop.ScoreStep{
		{Name: "survey", Part: "scout", Kind: loop.KindProduce},
		{Name: "rule", Part: "judge", Kind: loop.KindDecide},
		{Name: "inspect", Part: "auditor", Kind: loop.KindReview},
	}}
	p := ensembleRun{Members: []ensemble.Member{
		{Name: "scout", Part: "scout"}, {Name: "judge", Part: "judge"}, {Name: "auditor", Part: "auditor"},
	}}
	seating := recordSeating(p, score)
	for i, want := range []string{"resume", "fresh", "fresh"} {
		if seating.Sections[0].Chairs[i].Memory != want {
			t.Fatal(seating)
		}
	}
}
