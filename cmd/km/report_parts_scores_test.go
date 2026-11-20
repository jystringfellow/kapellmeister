package main

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func recordedScore(t *testing.T, dir string, rounds int, steps ...[3]string) {
	t.Helper()
	outline := []map[string]string{}
	for _, step := range steps {
		outline = append(outline, map[string]string{"name": step[0], "kind": step[1], "part": step[2]})
	}
	data, err := json.Marshal(map[string]any{"score": map[string]any{"rounds": rounds, "steps": outline}})
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "current.json"), string(data))
}

func TestReportPartsListsOneRowPerPartAndModelForARunOfAScoreFromAFile(t *testing.T) {
	for _, source := range []string{"record and outline", "record", "outline"} {
		t.Run(source, func(t *testing.T) {
			root := recordRoot(t)
			draft, check, nudge := partStep("draft", "alpha", "", 1), partStep("check", "beta", "", 1), partStep("check-nudge", "beta", "", 1)
			if source != "outline" {
				draft["part"] = "drafter"
				check["part"] = "checker"
				if source == "record" {
					nudge["part"] = "checker"
				}
			}
			dir := partsFixture(t, root, "one", "approved", 1, draft, check, nudge)
			if source != "record" {
				recordedScore(t, dir, 0, [3]string{"draft", "produce", "drafter"}, [3]string{"check", "review", "checker"})
			}
			p := partsDocument(t, root)
			requireNumbers(t, p, map[string]int{"runs_without_steps": 0})
			rows := p["rows"].([]any)
			if len(rows) != 2 {
				t.Fatalf("rows = %v", rows)
			}
			if rows[0].(map[string]any)["part"] != "checker" || rows[1].(map[string]any)["part"] != "drafter" {
				t.Fatal(rows)
			}
			for _, c := range []struct {
				part, model string
				steps       int
			}{{"drafter", "alpha", 1}, {"checker", "beta", 2}} {
				row := partRow(t, rows, c.part, c.model)
				requireNumbers(t, row, map[string]int{"runs": 1, "steps": c.steps, "duration_ms": 1000 * c.steps, "duration_ms_per_step": 1000})
				requireNumbers(t, row["usage"].(map[string]any), map[string]int{"input_tokens": 100 * c.steps, "output_tokens": 20 * c.steps})
				if _, ok := row["work"]; ok {
					t.Fatalf("inapplicable work: %v", row)
				}
			}
			out, diagnostics := captureReport(t, "-parts", root)
			if diagnostics != "" || !strings.Contains(out, "checker") || !strings.Contains(out, "drafter") || strings.Contains(out, "work:") || strings.Contains(out, "runs without steps: 1") {
				t.Fatalf("%s / %s", out, diagnostics)
			}
		})
	}
}

func TestReportPartsPrefersTheRecordedPartAndIgnoresEscalationAndBlockedRecordsThatCarryAPart(t *testing.T) {
	for _, outline := range []bool{false, true} {
		t.Run(map[bool]string{false: "record", true: "outline"}[outline], func(t *testing.T) {
			root := recordRoot(t)
			draft := partStep("draft", "m", "", 1)
			draft["part"] = "drafter"
			records := []map[string]any{draft}
			for _, name := range []string{"escalate", "blocked", "unblocked", "verify", "strict", "start", "verdict-draft"} {
				record := partStep(name, "ignored", "", 1)
				record["part"] = "drafter"
				if name == "blocked" || name == "unblocked" {
					record["blocked_step"] = "draft"
				}
				records = append(records, record)
			}
			dir := partsFixture(t, root, "one", "approved", 1, records...)
			if outline {
				recordedScore(t, dir, 0, [3]string{"draft", "produce", "other"}, [3]string{"verify", "verify", "drafter"}, [3]string{"strict", "strict", "drafter"})
			}
			rows := partRows(t, root)
			if len(rows) != 1 {
				t.Fatal(rows)
			}
			requireNumbers(t, partRow(t, rows, "drafter", "m"), map[string]int{"steps": 1})
		})
	}
}

func TestReportPartsBuildsTheWorkTablesFromStepKindsAndLeavesOutTablesThatMeanNothingForTheScore(t *testing.T) {
	for _, missing := range []string{"", "review", "decide", "test"} {
		t.Run(missing, func(t *testing.T) {
			root := recordRoot(t)
			records := []map[string]any{}
			steps := [][3]string{{"draft", "produce", "drafter"}}
			for _, s := range [][3]string{{"check", "review", "checker"}, {"choose", "decide", "decider"}, {"probe", "test", "prober"}} {
				if s[1] == missing {
					continue
				}
				steps = append(steps, s)
				records = append(records, partStep(s[0], "m", "", 1), partVerdict(s[0], "", "APPROVE", 1))
			}
			records = append(records, partStep("draft", "m", "", 1))
			dir := partsFixture(t, root, "one", "approved", 1, records...)
			recordedScore(t, dir, 2, steps...)
			rows := partRows(t, root)
			for _, s := range steps {
				row := partRow(t, rows, s[2], "m")
				work, ok := row["work"].(map[string]any)
				want := s[1] == "produce" || missing == "" || (s[1] == "decide" && missing == "review")
				if ok != want {
					t.Fatalf("%s work=%v want applicable=%v", s[2], work, want)
				}
				if !ok {
					continue
				}
				switch s[1] {
				case "produce":
					requireNumbers(t, work, map[string]int{"approved_first_round": 1})
				case "review":
					requireNumbers(t, work, map[string]int{"approve": 1, "approve_gate_ran": 1, "approve_test_ran": 1})
				case "decide":
					requireNumbers(t, work, map[string]int{"approve": 1, "approve_test_ran": 1})
				case "test":
					requireNumbers(t, work, map[string]int{"approve": 1, "all_approved_rounds_tested": 1})
				}
			}
			out, _ := captureReport(t, "-parts", root)
			if missing == "" {
				for _, part := range []string{"drafter", "checker", "decider", "prober"} {
					if !strings.Contains(out, "work: "+part) {
						t.Fatal(out)
					}
				}
			}
		})
	}
}

func TestReportPartsKeepsMergedWorkOnlyWhenEveryContributingRunHasTheSameApplicableTable(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		t.Run(map[bool]string{false: "first applicable", true: "last applicable"}[reverse], func(t *testing.T) {
			root := recordRoot(t)
			for i, id := range []string{"one", "two"} {
				rounds := 0
				if (i == 0) != reverse {
					rounds = 2
				}
				dir := partsFixture(t, root, id, "approved", 1, partStep("draft", "m", "", 1))
				recordedScore(t, dir, rounds, [3]string{"draft", "produce", "drafter"})
			}
			row := partRow(t, partRows(t, root), "drafter", "m")
			requireNumbers(t, row, map[string]int{"runs": 2, "steps": 2})
			if _, ok := row["work"]; ok {
				t.Fatal(row)
			}
		})
	}
}

func TestReportPartsAttributesRenamedVerdictsByRoundAndChairAndCountsDownstreamFacts(t *testing.T) {
	root := recordRoot(t)
	records := []map[string]any{}
	for round := 1; round <= 4; round++ {
		for _, name := range []string{"first", "second"} {
			records = append(records, partStep("check", "review", name, round))
			verdict := "APPROVE"
			if round == 2 && name == "second" {
				verdict = "CHANGES"
			}
			if round == 3 && name == "second" {
				verdict = "FAILED"
			}
			if round == 4 {
				verdict = "ODD"
			}
			records = append(records, partVerdict("check", name, verdict, round))
		}
		verdict := "APPROVE"
		if round == 1 {
			verdict = "CHANGES"
		}
		records = append(records, partStep("choose", "decide", "", round), partVerdict("choose", "", verdict, round), partStep("probe", "test", "", round), partVerdict("probe", "", "CHANGES", round))
	}
	records = append(records, partVerdict("check", "second", "APPROVE", 4), partVerdict("check", "absent", "APPROVE", 1))
	dir := partsFixture(t, root, "one", "approved", 4, records...)
	steps := [][3]string{{"check", "review", "checker"}, {"choose", "decide", "decider"}, {"probe", "test", "prober"}}
	recordedScore(t, dir, 4, steps...)
	p := partsDocument(t, root)
	requireNumbers(t, p, map[string]int{"unattributed_verdicts": 1})
	rows := p["rows"].([]any)
	requireNumbers(t, partRow(t, rows, "checker", "review")["work"].(map[string]any), map[string]int{"approve": 5, "changes": 1, "failed": 1, "unclear": 1, "approve_gate_ran": 5, "approve_then_gate_not_approved": 2, "approve_test_ran": 5, "approve_then_test_not_approved": 5, "changes_gate_ran": 1, "changes_then_gate_approved": 1})
	requireNumbers(t, partRow(t, rows, "decider", "decide")["work"].(map[string]any), map[string]int{"approve": 3, "changes": 1, "approve_test_ran": 3, "approve_then_test_not_approved": 3})
	requireNumbers(t, partRow(t, rows, "prober", "test")["work"].(map[string]any), map[string]int{"changes": 4, "all_approved_rounds_tested": 0, "not_approved_after_all_approved": 0})
	dir = partsFixture(t, root, "two", "approved", 1, partStep("check", "review", "", 1), partVerdict("check", "", "APPROVE", 1), partVerdict("choose", "", "APPROVE", 1), partStep("probe", "test", "", 1), partVerdict("probe", "", "CHANGES", 1))
	recordedScore(t, dir, 4, steps...)
	requireNumbers(t, partRow(t, partRows(t, root), "prober", "test")["work"].(map[string]any), map[string]int{"all_approved_rounds_tested": 1, "not_approved_after_all_approved": 1})
}

func TestReportPartsEscapesRecordedPartNamesInTextAndKeepsThemInJSON(t *testing.T) {
	root := recordRoot(t)
	part := "drafter\n\x1b[31m"
	step := partStep("draft", "m", "", 1)
	step["part"] = part
	partsFixture(t, root, "one", "approved", 1, step)
	row := partRow(t, partRows(t, root), part, "m")
	if row["part"] != part {
		t.Fatal(row)
	}
	out, _ := captureReport(t, "-parts", root)
	if strings.Contains(out, "\x1b") || !strings.Contains(out, `drafter\n\x1b[31m`) {
		t.Fatal(out)
	}
}
