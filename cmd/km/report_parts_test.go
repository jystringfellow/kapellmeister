package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
)

func partStep(step, model, name string, round int) map[string]any {
	return map[string]any{"step": step, "round": round, "chair_name": name, "chair": map[string]any{"provider": "p", "model": model}, "duration_ms": 1000, "usage": map[string]any{"known": true, "input_tokens": 100, "output_tokens": 20, "cache_read_tokens": 0, "cache_write_tokens": 0}}
}
func partVerdict(subject, name, verdict string, round int) map[string]any {
	return map[string]any{"step": "verdict-" + subject, "chair_name": name, "round": round, "verdict": verdict}
}
func partsFixture(t *testing.T, root, id, outcome string, rounds int, records ...map[string]any) string {
	t.Helper()
	dir := filepath.Join(root, id)
	body := "{\"at\":\"2026-11-10T14:00:00Z\",\"step\":\"start\"}\n"
	for _, r := range records {
		b, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		body += string(b) + "\n"
	}
	writeFile(t, filepath.Join(dir, "telemetry.jsonl"), body)
	if outcome != "" {
		b, _ := json.Marshal(map[string]any{"run_id": id, "outcome": outcome, "rounds": rounds, "repository": "app"})
		writeFile(t, filepath.Join(dir, "result.json"), string(b))
	}
	return dir
}
func partsDocument(t *testing.T, args ...string) map[string]any {
	t.Helper()
	d := reportJSON(t, append([]string{"-parts"}, args...)...)
	p, ok := d["parts"].(map[string]any)
	if !ok {
		t.Fatalf("missing parts: %v", d)
	}
	return p
}
func partRows(t *testing.T, args ...string) []any {
	t.Helper()
	return partsDocument(t, args...)["rows"].([]any)
}
func partRow(t *testing.T, rows []any, part, model string) map[string]any {
	t.Helper()
	for _, r := range rows {
		row := r.(map[string]any)
		if row["part"] == part && row["model"] == model {
			return row
		}
	}
	t.Fatalf("missing %s/%s in %v", part, model, rows)
	return nil
}
func requireNumbers(t *testing.T, row map[string]any, want map[string]int) {
	t.Helper()
	for key, n := range want {
		if row[key] != float64(n) {
			t.Errorf("%s = %v, want %d (row %v)", key, row[key], n, row)
		}
	}
}
func TestPlainReportIgnoresTheStepRecordsAndPrintsWhatItAlwaysPrinted(t *testing.T) {
	root := recordRoot(t)
	dir := partsFixture(t, root, "one", "approved", 1)
	before, _ := captureReport(t, root)
	f, err := os.OpenFile(filepath.Join(dir, "telemetry.jsonl"), os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintln(f, `{"step":"implement","chair":{"model":"new"}}`)
	f.Close()
	after, _ := captureReport(t, root)
	if before != after {
		t.Fatalf("changed plain output: %s / %s", before, after)
	}
	if _, ok := reportJSON(t, root)["parts"]; ok {
		t.Fatal("plain JSON gained parts")
	}
}
func TestReportPartsShowsSeparateRowsForTwoModelsPlayingTheSamePart(t *testing.T) {
	root := recordRoot(t)
	for i, m := range []string{"alpha", "beta"} {
		partsFixture(t, root, m, "approved", i+1, partStep("plan", m, "", 0), partStep("implement", m, "", 1), partStep("review", m, "", 1), partVerdict("review", "", "APPROVE", 1))
	}
	rows := partRows(t, root)
	if len(rows) != 6 {
		t.Fatal(rows)
	}
	for _, m := range []string{"alpha", "beta"} {
		for _, part := range []string{"planner", "implementer", "reviewer"} {
			requireNumbers(t, partRow(t, rows, part, m), map[string]int{"runs": 1, "steps": 1, "duration_ms": 1000, "duration_ms_per_step": 1000})
		}
	}
	out, _ := captureReport(t, "-parts", root)
	for _, s := range []string{"by part and model", "TIME/STEP", "TOKENS/STEP", "alpha", "beta", "work: implementer", "work: reviewer"} {
		if !strings.Contains(out, s) {
			t.Errorf("missing %s: %s", s, out)
		}
	}
}
func TestReportPartsSumsTimeAndTokensPerRowAndPerStepWithTheLowerBoundForUnmeasuredSteps(t *testing.T) {
	root := recordRoot(t)
	a := partStep("review", "partial", "", 1)
	b := partStep("review", "partial", "", 2)
	delete(b, "duration_ms")
	delete(b, "usage")
	c := partStep("review", "unknown", "", 1)
	c["duration_ms"] = -1
	c["usage"] = map[string]any{"known": false}
	z := partStep("review", "zero", "", 1)
	z["duration_ms"] = 0
	z["usage"].(map[string]any)["input_tokens"] = 0
	z["usage"].(map[string]any)["output_tokens"] = 0
	partsFixture(t, root, "one", "approved", 2, a, b, c, z)
	rows := partRows(t, root)
	r := partRow(t, rows, "reviewer", "partial")
	requireNumbers(t, r, map[string]int{"duration_ms": 1000, "unmeasured_duration_steps": 1, "duration_ms_per_step": 500, "steps": 2})
	requireNumbers(t, r["usage"].(map[string]any), map[string]int{"input_tokens": 100, "output_tokens": 20, "unknown_steps": 1})
	requireNumbers(t, r["usage_per_step"].(map[string]any), map[string]int{"input_tokens": 50, "output_tokens": 10})
	if _, ok := partRow(t, rows, "reviewer", "unknown")["duration_ms_per_step"]; ok {
		t.Fatal("unknown average")
	}
	out, _ := captureReport(t, "-parts", root)
	if !strings.Contains(out, "≥100/20") || !strings.Contains(out, "≥50/10") {
		t.Fatal(out)
	}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) > 3 && f[0] == "reviewer" && f[2] == "unknown" && !strings.Contains(line, "unknown  unknown") {
			t.Fatal(line)
		}
	}
}
func TestReportPartsAttributesVerdictsByRoundAndChairAndCountsDownstreamFacts(t *testing.T) {
	root := recordRoot(t)
	records := []map[string]any{}
	for round := 1; round <= 4; round++ {
		for _, n := range []string{"reviewer-1", "reviewer-2"} {
			records = append(records, partStep("review", "review", n, round))
			verdict := "APPROVE"
			if round == 2 && n == "reviewer-2" {
				verdict = "CHANGES"
			}
			if round == 3 && n == "reviewer-2" {
				verdict = "FAILED"
			}
			if round == 4 {
				verdict = "ODD"
			}
			records = append(records, partVerdict("review", n, verdict, round))
		}
		g := "APPROVE"
		if round == 1 {
			g = "CHANGES"
		}
		records = append(records, partStep("gate", "gate", "", round), partVerdict("gate", "", g, round), partStep("test", "test", "", round), partVerdict("test", "", "CHANGES", round))
	}
	// The last duplicate verdict replaces the earlier one.
	records = append(records, partVerdict("review", "reviewer-2", "APPROVE", 4), partVerdict("review", "absent", "APPROVE", 1))
	partsFixture(t, root, "one", "approved", 4, records...)
	p := partsDocument(t, root)
	requireNumbers(t, p, map[string]int{"unattributed_verdicts": 1})
	rows := p["rows"].([]any)
	r := partRow(t, rows, "reviewer", "review")
	requireNumbers(t, r, map[string]int{"runs": 1, "steps": 8})
	requireNumbers(t, r["work"].(map[string]any), map[string]int{"approve": 5, "changes": 1, "failed": 1, "unclear": 1, "approve_gate_ran": 5, "approve_then_gate_not_approved": 2, "approve_test_ran": 5, "approve_then_test_not_approved": 5, "changes_gate_ran": 1, "changes_then_gate_approved": 1})
	requireNumbers(t, partRow(t, rows, "gate", "gate")["work"].(map[string]any), map[string]int{"approve": 3, "changes": 1, "approve_test_ran": 3, "approve_then_test_not_approved": 3})
	requireNumbers(t, partRow(t, rows, "tester", "test")["work"].(map[string]any), map[string]int{"changes": 4, "all_approved_rounds_tested": 0, "not_approved_after_all_approved": 0})
	partsFixture(t, root, "two", "approved", 1, partStep("review", "review", "", 1), partVerdict("review", "", "APPROVE", 1), partStep("gate", "gate", "", 1), partVerdict("gate", "", "APPROVE", 1), partStep("test", "test", "", 1), partVerdict("test", "", "CHANGES", 1))
	requireNumbers(t, partRow(t, partRows(t, root), "tester", "test")["work"].(map[string]any), map[string]int{"all_approved_rounds_tested": 1, "not_approved_after_all_approved": 1})
}
func TestReportPartsCountsRunOutcomesOnlyForPartsPlayedByOneModel(t *testing.T) {
	root := recordRoot(t)
	for i, c := range []struct {
		o string
		r int
	}{{"approved", 1}, {"approved", 3}, {"approved", 0}, {"failed", 2}, {"", 0}} {
		partsFixture(t, root, fmt.Sprint(i), c.o, c.r, partStep("plan", "m", "", 0), partStep("implement", "m", "", 1))
	}
	partsFixture(t, root, "split", "approved", 2, partStep("implement", "m", "", 1), partStep("implement", "other", "", 2))
	for _, p := range []string{"planner", "implementer"} {
		w := partRow(t, partRows(t, root), p, "m")["work"].(map[string]any)
		requireNumbers(t, w, map[string]int{"approved_first_round": 1, "approved_later_rounds": 1, "approved_rounds_unknown": 1, "not_approved": 1, "unfinished": 1, "rounds_in_approved_runs": 4})
		if p == "implementer" {
			requireNumbers(t, w, map[string]int{"split_runs": 1})
		}
	}
}
func TestReportPartsCountsKnownStepsAndNudgesAndSelectsTheRecordedModel(t *testing.T) {
	root := recordRoot(t)
	records := []map[string]any{partStep("escalate", "ignored", "", 1), partStep("unknown", "ignored", "", 1), {"step": "implement"}, {"step": "lessons"}}
	for _, s := range []string{"plan", "implement-nudge", "review-nudge", "gate", "test-plan", "test", "classify-review", "classify-gate-nudge", "classify-test", "lessons"} {
		records = append(records, partStep(s, "m", "", 1))
	}
	a := partStep("review", "configured", "", 1)
	a["model"] = "actual"
	a["model_honored"] = false
	b := partStep("review", "configured", "", 1)
	b["model"] = "actual"
	c := partStep("review", "", "", 1)
	c["model"] = "fallback"
	d := partStep("review", "", "", 1)
	d["chair"] = map[string]any{}
	records = append(records, a, b, c, d)
	partsFixture(t, root, "one", "approved", 1, records...)
	p := partsDocument(t, root)
	requireNumbers(t, p, map[string]int{"unattributed_steps": 2})
	rows := p["rows"].([]any)
	for _, m := range []string{"actual", "configured", "fallback", "unknown"} {
		partRow(t, rows, "reviewer", m)
	}
	requireNumbers(t, partRow(t, rows, "classifier", "m"), map[string]int{"steps": 3})
	requireNumbers(t, partRow(t, rows, "gate", "m"), map[string]int{"steps": 2})
	if len(rows) != 10 {
		t.Fatal(rows)
	}
}
func TestReportPartsOmitsEffortAndTierUnlessTheyVaryAndNeverSplitsOnThem(t *testing.T) {
	root := recordRoot(t)
	a := partStep("implement", "m", "", 1)
	a["effort"] = "high"
	a["tier"] = "large"
	b := partStep("implement", "m", "", 2)
	b["tier"] = "large"
	partsFixture(t, root, "one", "approved", 2, a, b)
	rows := partRows(t, root)
	if len(rows) != 1 {
		t.Fatal(rows)
	}
	r := rows[0].(map[string]any)
	requireNumbers(t, r["efforts"].(map[string]any), map[string]int{"high": 1, "unset": 1})
	if _, ok := r["tiers"]; ok {
		t.Fatal(r)
	}
	out, _ := captureReport(t, "-parts", root)
	if !strings.Contains(out, "effort varies:") || strings.Contains(out, "tier varies:") {
		t.Fatal(out)
	}
}
func TestReportPartsSkipsOddLinesWithOneDiagnosticAndIgnoresATruncatedLastLine(t *testing.T) {
	root := recordRoot(t)
	dir := partsFixture(t, root, "one", "approved", 1, partStep("review", "m", "", 1))
	f, _ := os.OpenFile(filepath.Join(dir, "telemetry.jsonl"), os.O_APPEND|os.O_WRONLY, 0600)
	odd := []string{`null`, `[]`, `42`, `{"step":"review","chair":2}`, `{"step":"review","chair":{},"round":"x"}`, `{"step":"review","chair":{},"duration_ms":1e99}`, `{"step":"review","chair":{},"model_honored":"yes"}`, `{"step":"review","chair":{},"round":-1}`}
	for _, line := range odd {
		fmt.Fprintln(f, line)
	}
	fmt.Fprint(f, `{"step":`)
	f.Close()
	out, diagnostics := captureReport(t, "-parts", "-json", root)
	if strings.Count(diagnostics, "telemetry lines could not be read and were skipped") != 1 || !strings.Contains(diagnostics, "8 telemetry lines") {
		t.Fatalf("%s / %s", out, diagnostics)
	}
	rows := partRows(t, root)
	requireNumbers(t, rows[0].(map[string]any), map[string]int{"steps": 1})
}
func TestReportPartsReadsStepsDespiteMissingResultsCurrentStateOrAnUnreadableFirstLine(t *testing.T) {
	root := recordRoot(t)
	a := partsFixture(t, root, "a", "", 0, partStep("implement", "m", "", 1))
	b := partsFixture(t, root, "b", "approved", 1, partStep("implement", "m", "", 1))
	writeFile(t, filepath.Join(a, "current.json"), `{"started":"2026-11-10T14:00:00Z"}`)
	data, _ := os.ReadFile(filepath.Join(b, "telemetry.jsonl"))
	writeFile(t, filepath.Join(b, "telemetry.jsonl"), "bad\n"+string(data))
	writeFile(t, filepath.Join(root, "empty", "telemetry.jsonl"), "")
	writeFile(t, filepath.Join(root, "missing", "result.json"), `{"outcome":"approved"}`)
	p := partsDocument(t, root)
	requireNumbers(t, p, map[string]int{"runs_without_steps": 2})
	requireNumbers(t, partRow(t, p["rows"].([]any), "implementer", "m"), map[string]int{"runs": 2, "steps": 2})
}
func TestReportPartsFollowsFiltersDirectoriesCopyResolutionAndSearchLimits(t *testing.T) {
	a, b := recordRoot(t), recordRoot(t)
	for _, root := range []string{a, b} {
		partsFixture(t, root, "same", "approved", 1, partStep("implement", "m", "", 1))
	}
	requireNumbers(t, partRow(t, partRows(t, a, b), "implementer", "m"), map[string]int{"runs": 1, "steps": 1})
	if len(partRows(t, "-since", "2026-11-11", a)) != 0 || len(partRows(t, a, "-repository", "other")) != 0 || len(partRows(t, "-max-depth", "1", a)) != 0 {
		t.Fatal("filter ignored")
	}
	if len(partRows(t, "-repository", "app", a)) != 1 {
		t.Fatal("matching filter")
	}
	partsFixture(t, b, "same", "failed", 1, partStep("implement", "m", "", 1))
	requireNumbers(t, partRow(t, partRows(t, a, b), "implementer", "m"), map[string]int{"runs": 2, "steps": 2})
	deep := recordRoot(t)
	partsFixture(t, filepath.Join(deep, "nested"), "run", "approved", 1, partStep("implement", "m", "", 1))
	if len(partRows(t, "-max-entries", "1", deep)) != 0 {
		t.Fatal("entry limit ignored")
	}
}
func TestReportPartsNeverReadsATelemetrySymlinkOrNonRegularFile(t *testing.T) {
	for _, kind := range []string{"symlink", "fifo", "directory"} {
		t.Run(kind, func(t *testing.T) {
			root := recordRoot(t)
			dir := partsFixture(t, root, "one", "approved", 1)
			path := filepath.Join(dir, "telemetry.jsonl")
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			var err error
			switch kind {
			case "symlink":
				target := partsFixture(t, recordRoot(t), "outside", "approved", 1, partStep("review", "outside", "", 1))
				err = os.Symlink(filepath.Join(target, "telemetry.jsonl"), path)
			case "fifo":
				err = syscall.Mkfifo(path, 0600)
			case "directory":
				err = os.Mkdir(path, 0700)
			}
			if err != nil {
				t.Fatal(err)
			}
			p := partsDocument(t, root)
			if len(p["rows"].([]any)) != 0 {
				t.Fatal(p)
			}
			requireNumbers(t, p, map[string]int{"runs_without_steps": 1})
		})
	}
}
func TestReportPartsSkipsAnOversizedLineAndStopsAtTheFileLimit(t *testing.T) {
	root := recordRoot(t)
	dir := partsFixture(t, root, "one", "approved", 1)
	f, _ := os.OpenFile(filepath.Join(dir, "telemetry.jsonl"), os.O_APPEND|os.O_WRONLY, 0600)
	fmt.Fprintln(f, strings.Repeat("x", (1<<20)+1))
	data, _ := json.Marshal(partStep("review", "m", "", 1))
	fmt.Fprintln(f, string(data))
	f.Close()
	requireNumbers(t, partRow(t, partRows(t, root), "reviewer", "m"), map[string]int{"steps": 1})
	f, _ = os.OpenFile(filepath.Join(dir, "telemetry.jsonl"), os.O_APPEND|os.O_WRONLY, 0600)
	for i := 0; i < 64; i++ {
		fmt.Fprintln(f, strings.Repeat(" ", 1<<20))
	}
	fmt.Fprintln(f, string(data))
	f.Close()
	_, diagnostics := captureReport(t, "-parts", root)
	if !strings.Contains(diagnostics, "64 MiB") {
		t.Fatal(diagnostics)
	}
	requireNumbers(t, partRow(t, partRows(t, root), "reviewer", "m"), map[string]int{"steps": 1})
}
func TestReportPartsEscapesControlCharactersAndCarriesFullNamesInJSON(t *testing.T) {
	root := recordRoot(t)
	s := partStep("review", "m\n\x1b[31m", "", 1)
	s["chair"].(map[string]any)["provider"] = "p\tbad"
	partsFixture(t, root, "one", "approved", 1, s)
	row := partRows(t, root)[0].(map[string]any)
	if row["model"] != "m\n\x1b[31m" || row["provider"] != "p\tbad" {
		t.Fatal(row)
	}
	out, _ := captureReport(t, "-parts", root)
	if strings.Contains(out, "\x1b") || !strings.Contains(out, `p\tbad`) || !strings.Contains(out, `m\n\x1b`) {
		t.Fatal(out)
	}
}
func TestReportPartsReadsARunRecordedByTheLoopItself(t *testing.T) {
	s := playStanding(t)
	dir := recordRun(t, s.Worktree, "thorough", chairsFromEnsembleFile(t, s.Worktree))
	rows := partRows(t, dir)
	if len(rows) != 5 {
		t.Fatal(rows)
	}
	r := partRow(t, rows, "reviewer", "m")
	requireNumbers(t, r, map[string]int{"runs": 1, "steps": 2})
	requireNumbers(t, r["work"].(map[string]any), map[string]int{"approve": 2})
}
func TestReportPartsLeavesThePlainJSONKeysAndTheirMeaningUnchanged(t *testing.T) {
	root := recordRoot(t)
	partsFixture(t, root, "one", "approved", 1, partStep("review", "m", "", 1))
	plain := reportJSON(t, root)
	parts := reportJSON(t, "-parts", root)
	delete(parts, "parts")
	if !reflect.DeepEqual(plain, parts) {
		t.Fatalf("%v / %v", plain, parts)
	}
}

func TestReportPartsSeparatesLessonsMetadataFromUnattributedStepsAndCountsIncompleteVerdicts(t *testing.T) {
	root := recordRoot(t)
	partsFixture(t, root, "one", "approved", 1,
		map[string]any{"step": "lessons", "lessons": map[string]any{"part": "planner"}},
		map[string]any{"step": "lessons", "error": "tree changed", "tree_changed": true},
		map[string]any{"step": "lessons"},
		map[string]any{"step": "verdict-review", "verdict": "APPROVE"},
		map[string]any{"step": "verdict-gate", "round": 1},
	)
	p := partsDocument(t, root)
	requireNumbers(t, p, map[string]int{"unattributed_steps": 1, "unattributed_verdicts": 2, "runs_without_steps": 1})
}
func TestReportPartsNeverAttributesAnUnreadableResultAsAnUnfinishedOrFailedRun(t *testing.T) {
	root := recordRoot(t)
	dir := partsFixture(t, root, "one", "approved", 1, partStep("implement", "m", "", 1))
	writeFile(t, filepath.Join(dir, "result.json"), "{")
	w := partRow(t, partRows(t, root), "implementer", "m")["work"].(map[string]any)
	requireNumbers(t, w, map[string]int{"unreadable_results": 1, "unfinished": 0, "not_approved": 0})
}
func TestReportPartsUsesTheLastStepAndVerdictForEachRoundAndChair(t *testing.T) {
	root := recordRoot(t)
	partsFixture(t, root, "one", "approved", 1, partStep("review", "old", "reviewer-1", 1), partVerdict("review", "reviewer-1", "CHANGES", 1), partStep("review", "new", "reviewer-1", 1), partVerdict("review", "reviewer-1", "APPROVE", 1), partStep("review", "other", "reviewer-2", 1), partVerdict("review", "reviewer-2", "CHANGES", 1))
	rows := partRows(t, root)
	requireNumbers(t, partRow(t, rows, "reviewer", "old")["work"].(map[string]any), map[string]int{"approve": 0, "changes": 0})
	requireNumbers(t, partRow(t, rows, "reviewer", "new")["work"].(map[string]any), map[string]int{"approve": 1, "changes": 0})
	requireNumbers(t, partRow(t, rows, "reviewer", "other")["work"].(map[string]any), map[string]int{"approve": 0, "changes": 1})
}
func TestReportPartsCountsMalformedUsageAsUnknownButKeepsTheStepAndMeasuredZero(t *testing.T) {
	root := recordRoot(t)
	records := []map[string]any{}
	for _, usage := range []any{nil, "bad", map[string]any{"known": true}, map[string]any{"known": true, "input_tokens": -1, "output_tokens": 0, "cache_read_tokens": 0, "cache_write_tokens": 0}, map[string]any{"known": true, "input_tokens": 1 << 37, "output_tokens": 0, "cache_read_tokens": 0, "cache_write_tokens": 0}} {
		s := partStep("review", "m", "", 1)
		s["usage"] = usage
		s["error"] = "step error"
		records = append(records, s)
	}
	z := partStep("review", "zero", "", 1)
	z["duration_ms"] = 0
	z["usage"] = map[string]any{"known": true, "input_tokens": 0, "output_tokens": 0, "cache_read_tokens": 0, "cache_write_tokens": 0}
	records = append(records, z)
	partsFixture(t, root, "one", "approved", 1, records...)
	rows := partRows(t, root)
	r := partRow(t, rows, "reviewer", "m")
	requireNumbers(t, r, map[string]int{"steps": 5, "step_errors": 5})
	requireNumbers(t, r["usage"].(map[string]any), map[string]int{"unknown_steps": 5})
	if r["usage"].(map[string]any)["known"] != false {
		t.Fatal(r)
	}
	out, _ := captureReport(t, "-parts", root)
	if !strings.Contains(out, "0s") || !strings.Contains(out, "0/0") {
		t.Fatal(out)
	}
}
func TestTheReadmesReportPartsExampleIsWhatTheFixturePrints(t *testing.T) {
	root := recordRoot(t)
	for i, m := range []string{"alpha", "beta"} {
		partsFixture(t, root, m, "approved", i+1, partStep("implement", m, "", 1), partStep("review", m, "", 1), partVerdict("review", "", "APPROVE", 1))
	}
	out, _ := captureReport(t, "-parts", root)
	_, section, ok := strings.Cut(out, "by part and model\n")
	if !ok {
		t.Fatal(out)
	}
	readme, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatal(err)
	}
	_, example, ok := strings.Cut(string(readme), "<!-- report-parts-example -->\n```text\n")
	if !ok {
		t.Fatal("README lacks report parts example")
	}
	example, _, ok = strings.Cut(example, "```\n<!-- /report-parts-example -->")
	if !ok {
		t.Fatal("unterminated example")
	}
	if example != "by part and model\n"+section {
		t.Fatalf("example differs, fixture prints:\nby part and model\n%s", section)
	}
}

func TestReportPartsNeverWrapsLargeRecordedDurationsOrRoundTotals(t *testing.T) {
	root := recordRoot(t)
	for _, id := range []string{"one", "two"} {
		s := partStep("implement", "m", "", 1)
		s["duration_ms"] = int64(1<<63 - 1)
		partsFixture(t, root, id, "approved", int(1<<62), s)
	}
	row := partRow(t, partRows(t, root), "implementer", "m")
	if row["duration_ms"].(float64) <= 0 || row["work"].(map[string]any)["rounds_in_approved_runs"].(float64) <= 0 {
		t.Fatal(row)
	}
	out, _ := captureReport(t, "-parts", root)
	if strings.Contains(out, "-2562047") {
		t.Fatal(out)
	}
}

func TestReportPartsMergesEffortTierAndUnmeasuredUsageAcrossRuns(t *testing.T) {
	root := recordRoot(t)
	a := partStep("review", "m", "", 1)
	a["effort"] = "high"
	a["tier"] = "large"
	b := partStep("review", "m", "", 1)
	b["effort"] = "low"
	b["tier"] = "small"
	delete(b, "usage")
	delete(b, "duration_ms")
	partsFixture(t, root, "one", "approved", 1, a)
	partsFixture(t, root, "two", "approved", 1, b)
	row := partRow(t, partRows(t, root), "reviewer", "m")
	requireNumbers(t, row, map[string]int{"runs": 2, "steps": 2, "unmeasured_duration_steps": 1})
	requireNumbers(t, row["efforts"].(map[string]any), map[string]int{"high": 1, "low": 1})
	requireNumbers(t, row["tiers"].(map[string]any), map[string]int{"large": 1, "small": 1})
	requireNumbers(t, row["usage"].(map[string]any), map[string]int{"unknown_steps": 1, "input_tokens": 100})
}
func TestReportPartsUsesUnattributedVerdictsForRoundFactsWithoutGivingThemToAModel(t *testing.T) {
	root := recordRoot(t)
	partsFixture(t, root, "one", "approved", 1, partStep("review", "m", "", 1), partVerdict("review", "", "APPROVE", 1), partVerdict("gate", "", "CHANGES", 1), partVerdict("test", "", "UNCLEAR", 1))
	p := partsDocument(t, root)
	requireNumbers(t, p, map[string]int{"unattributed_verdicts": 2})
	rows := p["rows"].([]any)
	if len(rows) != 1 {
		t.Fatal(rows)
	}
	requireNumbers(t, rows[0].(map[string]any)["work"].(map[string]any), map[string]int{"approve": 1, "approve_gate_ran": 1, "approve_then_gate_not_approved": 1, "approve_test_ran": 1, "approve_then_test_not_approved": 1})
}
func TestReportPartsCountsApprovedRunsWithMissingRoundsAsUnknownAndKeepsOldVerdictlessSteps(t *testing.T) {
	root := recordRoot(t)
	dir := partsFixture(t, root, "one", "approved", 1, partStep("plan", "m", "", 0), partStep("review", "m", "", 1))
	writeFile(t, filepath.Join(dir, "result.json"), `{"outcome":"approved"}`)
	rows := partRows(t, root)
	requireNumbers(t, partRow(t, rows, "planner", "m")["work"].(map[string]any), map[string]int{"approved_rounds_unknown": 1, "approved_first_round": 0})
	requireNumbers(t, partRow(t, rows, "reviewer", "m")["work"].(map[string]any), map[string]int{"approve": 0, "changes": 0, "failed": 0, "unclear": 0})
}

func TestReportPartsNeverTreatsAReviewStepWithNoVerdictAsAnApprovalForUnanimity(t *testing.T) {
	root := recordRoot(t)
	partsFixture(t, root, "one", "approved", 1,
		partStep("review", "m", "reviewer-1", 1), partVerdict("review", "reviewer-1", "APPROVE", 1),
		partStep("review", "m", "reviewer-2", 1),
		partStep("gate", "g", "", 1), partVerdict("gate", "", "APPROVE", 1),
		partStep("test", "t", "", 1), partVerdict("test", "", "CHANGES", 1))
	requireNumbers(t, partRow(t, partRows(t, root), "tester", "t")["work"].(map[string]any), map[string]int{"all_approved_rounds_tested": 0, "not_approved_after_all_approved": 0, "changes": 1})
}

func TestReportPartsLeavesAVerdictUnattributedWhenTheLastMatchingStepHasNoChair(t *testing.T) {
	root := recordRoot(t)
	partsFixture(t, root, "one", "approved", 1, partStep("review", "m", "", 1), map[string]any{"step": "review", "round": 1}, partVerdict("review", "", "APPROVE", 1))
	p := partsDocument(t, root)
	requireNumbers(t, p, map[string]int{"unattributed_steps": 1, "unattributed_verdicts": 1})
	requireNumbers(t, partRow(t, p["rows"].([]any), "reviewer", "m")["work"].(map[string]any), map[string]int{"approve": 0})
}

func TestReportPartsNeverKeepsAnEarlierApprovalWhenTheLastVerdictRecordIsIncomplete(t *testing.T) {
	root := recordRoot(t)
	partsFixture(t, root, "one", "approved", 1, partStep("review", "m", "", 1), partVerdict("review", "", "APPROVE", 1), map[string]any{"step": "verdict-review", "round": 1})
	p := partsDocument(t, root)
	requireNumbers(t, p, map[string]int{"unattributed_verdicts": 1})
	requireNumbers(t, partRow(t, p["rows"].([]any), "reviewer", "m")["work"].(map[string]any), map[string]int{"approve": 0})
}
func TestReportPartsCountsEachPartlyMeasuredStepOnceEvenWithAnOddUnknownStepsCounter(t *testing.T) {
	root := recordRoot(t)
	a := partStep("review", "m", "", 1)
	a["usage"].(map[string]any)["unknown_steps"] = int(1<<63 - 1)
	partsFixture(t, root, "one", "approved", 1, a, a)
	row := partRow(t, partRows(t, root), "reviewer", "m")
	requireNumbers(t, row["usage"].(map[string]any), map[string]int{"unknown_steps": 2, "input_tokens": 200})
	out, _ := captureReport(t, "-parts", root)
	if !strings.Contains(out, "≥200/40") {
		t.Fatal(out)
	}
}

func TestTheReadmesReportCommandSynopsisListsThePartsFlag(t *testing.T) {
	readme, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatal(err)
	}
	_, commands, ok := strings.Cut(string(readme), "## Commands\n")
	if !ok {
		t.Fatal("README lacks Commands section")
	}
	_, report, ok := strings.Cut(commands, "\nkm report ")
	if !ok {
		t.Fatal("README Commands section lacks report synopsis")
	}
	synopsis, _, _ := strings.Cut(report, "\n")
	if !strings.Contains(synopsis, "[-parts]") {
		t.Fatalf("README report command synopsis omits [-parts]: km report %s", synopsis)
	}
}
