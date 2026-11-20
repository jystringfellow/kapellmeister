package loop

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestEveryStepPlayedByAFreshChairRecordsItsMemoryAndReason(t *testing.T) {
	for _, name := range []string{"plan", "gate", "classify-gate", "test-plan", "test", "review"} {
		t.Run(name, func(t *testing.T) {
			e, _, records := newEngine(t, passing)
			if _, err := e.step(context.Background(), Step{Name: name, Chair: e.Config.Chairs.Planner, ReplyPath: "reply.md"}); err != nil {
				t.Fatal(err)
			}
			var rec Record
			if err := json.Unmarshal(records.Bytes(), &rec); err != nil {
				t.Fatal(err)
			}
			reason := "declared fresh"
			if name == "review" {
				reason = "review parts are always fresh"
			}
			if rec.Memory != "fresh" || rec.MemoryReason != reason {
				t.Fatalf("record = %+v", rec)
			}
		})
	}
}

func TestANonScoreStepOfAResumingPartRecordsFreshWithoutClaimingItWasDeclaredFresh(t *testing.T) {
	e, _, records := newEngine(t, passing)
	e.Score = DefaultScore()
	e.Score.Parts[4].Memory = MemoryResume
	if _, err := e.step(context.Background(), Step{Name: "classify-gate", ReplyPath: "reply.md"}); err != nil {
		t.Fatal(err)
	}
	var rec Record
	if err := json.Unmarshal(records.Bytes(), &rec); err != nil {
		t.Fatal(err)
	}
	if rec.Memory != "fresh" || rec.MemoryReason != "" {
		t.Fatalf("record = %+v", rec)
	}
}

func TestStepsTheConductorPlaysItselfRecordNoMemory(t *testing.T) {
	e, _, records := newEngine(t, passing)
	e.Config.StrictCommand = "true"
	if res := run(t, e); res.Outcome != Approved {
		t.Fatal(res)
	}
	seen := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(records.String()), "\n") {
		var rec Record
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatal(err)
		}
		if rec.Step == "verify" || rec.Step == "strict" {
			seen[rec.Step] = true
			if rec.Memory != "" || rec.MemoryReason != "" {
				t.Fatal(rec)
			}
		}
	}
	if !seen["verify"] || !seen["strict"] {
		t.Fatalf("steps = %v", seen)
	}
}

func TestAScoreWithoutABackwardExitHasNoMaximumOfRounds(t *testing.T) {
	linear := Score{Steps: []ScoreStep{{Name: "draft", Exits: []Exit{{"done", "inspect"}}}, {Name: "inspect", Exits: []Exit{{"approve", "approved"}}}}}
	if linear.Loops() {
		t.Fatal("linear score loops")
	}
	linear.Steps[1].Exits = append(linear.Steps[1].Exits, Exit{"changes", "draft"})
	if !linear.Loops() || !DefaultScore().Loops() || !(Score{}).Loops() {
		t.Fatal("looping score has no maximum")
	}
	linear.Steps[1].Exits[1].To = "inspect"
	if !linear.Loops() {
		t.Fatal("self exit must loop")
	}
}
