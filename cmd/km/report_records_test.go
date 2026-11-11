package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

func reportUsageFixture(t *testing.T, root, id, repository, ensemble, usage string) string {
	t.Helper()
	dir := reportFixture(t, root, id, "2026-11-09T14:00:00Z", repository, ensemble, "approved")
	data, err := os.ReadFile(filepath.Join(dir, "result.json"))
	if err != nil {
		t.Fatal(err)
	}
	if usage != "" {
		writeFile(t, filepath.Join(dir, "result.json"), strings.TrimSuffix(string(data), "}")+`,"usage":`+usage+`}`)
	}
	return dir
}

const knownReportUsage = `{"known":true,"input_tokens":1200000,"output_tokens":340000,"cache_read_tokens":50,"cache_write_tokens":60}`

func TestReportShowsEachRunsRecordedTokenUsageInTheTableAndJSON(t *testing.T) {
	root := recordRoot(t)
	reportUsageFixture(t, root, "a", "app", "quick", knownReportUsage)
	out, diagnostics := captureReport(t, root)
	if diagnostics != "" || !strings.Contains(out, "TOKENS") || !strings.Contains(out, "1.2M/340k") {
		t.Fatalf("%s\n%s", out, diagnostics)
	}
	row := reportRows(t, root)[0].(map[string]any)
	var want any
	json.Unmarshal([]byte(knownReportUsage), &want)
	if !reflect.DeepEqual(row["usage"], want) {
		t.Fatalf("usage = %v, want %v", row["usage"], want)
	}
}

func TestReportShowsUnknownUsageForARunWithoutUsageWithUnknownUsageOrWithPartlyMeasuredUsageNeverZero(t *testing.T) {
	for name, usage := range map[string]string{
		"absent": "", "unknown": `{"known":false}`, "partial": strings.TrimSuffix(knownReportUsage, "}") + `,"unknown_steps":1}`,
		"negative": strings.Replace(knownReportUsage, "1200000", "-1", 1), "string": strings.Replace(knownReportUsage, "1200000", `"12"`, 1),
		"too large": strings.Replace(knownReportUsage, "1200000", "68719476737", 1), "missing": `{"known":true}`, "null counter": strings.Replace(knownReportUsage, "1200000", "null", 1),
		"null partial": strings.TrimSuffix(knownReportUsage, "}") + `,"unknown_steps":null}`, "unfinished": "",
	} {
		t.Run(name, func(t *testing.T) {
			root := recordRoot(t)
			dir := reportUsageFixture(t, root, "a", "app", "quick", usage)
			if name == "unfinished" {
				if err := os.Remove(filepath.Join(dir, "result.json")); err != nil {
					t.Fatal(err)
				}
			}
			out, diagnostics := captureReport(t, root)
			row := reportRows(t, root)[0].(map[string]any)
			if diagnostics != "" || !reflect.DeepEqual(row["usage"], map[string]any{"known": false}) || !strings.HasSuffix(strings.TrimSpace(strings.Split(out, "\n")[1]), "unknown") {
				t.Fatalf("row=%v\n%s\n%s", row, out, diagnostics)
			}
		})
	}
}

func TestReportTotalsByEnsembleAndRepositorySumOnlyKnownUsageAndCountRunsLeftOut(t *testing.T) {
	root := recordRoot(t)
	reportUsageFixture(t, root, "a", "app", "quick", knownReportUsage)
	reportUsageFixture(t, root, "b", "app", "quick", knownReportUsage)
	reportUsageFixture(t, root, "c", "app", "quick", "")
	reportUsageFixture(t, root, "d", "other", "slow", "")
	totals := reportJSON(t, root)["totals"].(map[string]any)
	want := map[string]any{"known_runs": float64(2), "unknown_runs": float64(1), "input_tokens": float64(2400000), "output_tokens": float64(680000), "cache_read_tokens": float64(100), "cache_write_tokens": float64(120)}
	for group, key := range map[string]string{"ensemble_usage": "quick", "repository_usage": "app"} {
		groups, ok := totals[group].(map[string]any)
		if !ok || !reflect.DeepEqual(groups[key], want) {
			t.Fatalf("totals=%v", totals)
		}
	}
	overall := totals["usage"].(map[string]any)
	if overall["unknown_runs"] != float64(2) {
		t.Fatal(overall)
	}
	out, _ := captureReport(t, root)
	normalized := strings.Join(strings.Fields(out), " ")
	for _, text := range []string{"usage: in 2.4M out 680k (2 without usage)", "quick 3 in 2.4M out 680k (1 without usage)", "slow 1 unknown", "other 1 unknown"} {
		if !strings.Contains(normalized, text) {
			t.Fatalf("missing %q: %s", text, out)
		}
	}
}

func TestReportTableStaysWithinOneHundredColumnsWithUsageShown(t *testing.T) {
	root := recordRoot(t)
	reportUsageFixture(t, root, "a", strings.Repeat("repository", 10), "thorough", "{\"known\":true,\"input_tokens\":68719476736,\"output_tokens\":68719476736,\"cache_read_tokens\":0,\"cache_write_tokens\":0}")
	out, _ := captureReport(t, root)
	for _, line := range strings.Split(strings.Split(out, "\n\n")[0], "\n") {
		if utf8.RuneCountInString(line) > 100 {
			t.Fatalf("wide row (%d): %s", utf8.RuneCountInString(line), line)
		}
	}
	row := reportRows(t, root)[0].(map[string]any)
	if row["repository"] != strings.Repeat("repository", 10) {
		t.Fatal(row)
	}
}

func TestReportListsTwoRecordsOfOneRunIdThatDisagreeAndNamesBothPathsOnStderr(t *testing.T) {
	for _, field := range []string{"outcome", "started", "repository"} {
		t.Run(field, func(t *testing.T) {
			a, b := recordRoot(t), recordRoot(t)
			pathA := reportFixture(t, a, "same", "2026-11-09T14:00:00Z", "app", "quick", "approved")
			at, repo, outcome := "2026-11-09T14:00:00Z", "app", "approved"
			switch field {
			case "outcome":
				outcome = "failed"
			case "started":
				at = "2026-11-10T14:00:00Z"
			case "repository":
				repo = "other"
			}
			pathB := reportFixture(t, b, "same", at, repo, "quick", outcome)
			var previous string
			for _, dirs := range [][]string{{a, b, a}, {b, a}} {
				out, diag := captureReport(t, append([]string{"-json"}, dirs...)...)
				doc := reportJSON(t, dirs...)
				rows := doc["runs"].([]any)
				if len(rows) != 2 || doc["totals"].(map[string]any)["runs"] != float64(2) || !strings.Contains(diag, pathA) || !strings.Contains(diag, pathB) || !strings.Contains(diag, field) {
					t.Fatalf("%s\n%s", out, diag)
				}
				for _, row := range rows {
					if row.(map[string]any)["conflict"] != true {
						t.Fatal(row)
					}
				}
				if previous != "" && previous != out {
					t.Fatalf("directory order changed report:\n%s\n%s", previous, out)
				}
				previous = out
				table, _ := captureReport(t, dirs...)
				if !strings.Contains(table, "conflicts: 2") {
					t.Fatal(table)
				}
			}
		})
	}
}

func TestReportDoesNotCallAnUnfinishedOrUnreadableCopyAConflictWithAFinishedOne(t *testing.T) {
	a, b, c := recordRoot(t), recordRoot(t), recordRoot(t)
	reportFixture(t, a, "same", "2026-11-09T14:00:00Z", "app", "quick", "approved")
	writeFile(t, filepath.Join(b, "same", "telemetry.jsonl"), "{\"at\":\"2026-11-09T14:00:00Z\"}\n")
	writeFile(t, filepath.Join(c, "same", "result.json"), "{")
	rows := reportRows(t, b, c, a)
	if len(rows) != 1 || rows[0].(map[string]any)["conflict"] != nil {
		t.Fatal(rows)
	}
}

func TestReportStopsBelowTheDepthLimitSaysWhatItSkippedAndCarriesOn(t *testing.T) {
	root := recordRoot(t)
	reportFixture(t, root, "found", "", "app", "quick", "approved")
	skipped := filepath.Join(root, "deep", "below")
	reportFixture(t, skipped, "hidden", "", "app", "quick", "approved")
	out, diag := captureReport(t, root, "-max-depth", "2")
	doc := reportJSON(t, root, "-max-depth", "2")
	if len(doc["runs"].([]any)) != 1 || !strings.Contains(diag, skipped) || !strings.Contains(diag, "-max-depth 2") || !strings.Contains(out, "not searched: 1 directories") || doc["not_searched_total"] != float64(1) {
		t.Fatalf("%s\n%s\n%v", out, diag, doc)
	}
	item := doc["not_searched"].([]any)[0].(map[string]any)
	if item["path"] != skipped || item["reason"] != "depth" {
		t.Fatal(item)
	}
}

func TestReportStopsAtTheEntryLimitSaysWhatItSkippedAndCarriesOn(t *testing.T) {
	a, b := recordRoot(t), recordRoot(t)
	reportFixture(t, a, "found", "", "app", "quick", "approved")
	for i := 0; i < 2000; i++ {
		writeFile(t, filepath.Join(b, fmt.Sprintf("file-%04d", i)), "")
	}
	out, diag := captureReport(t, filepath.Join(a, "found"), b, "-max-entries", "10")
	doc := reportJSON(t, filepath.Join(a, "found"), b, "-max-entries", "10")
	if len(doc["runs"].([]any)) != 1 || !strings.Contains(diag, "stopped after reading 10 entries (-max-entries 10)") || !strings.Contains(diag, b) || !strings.Contains(out, "not searched:") || doc["not_searched_total"] != float64(1) {
		t.Fatalf("%s\n%s\n%v", out, diag, doc)
	}
	if doc["not_searched"].([]any)[0].(map[string]any)["reason"] != "entries" {
		t.Fatal(doc)
	}
}

func TestReportDefaultLimitsReachThousandsOfKeptSandboxesAndTwentyLevelsOfDepth(t *testing.T) {
	root := recordRoot(t)
	for i := 0; i < 3000; i++ {
		reportFixture(t, filepath.Join(root, fmt.Sprintf("sandbox-%04d", i), "state", "runs"), fmt.Sprintf("run-%04d", i), "", "app", "quick", "approved")
	}
	deep := root
	for i := 0; i < 20; i++ {
		deep = filepath.Join(deep, "deep")
	}
	reportFixture(t, deep, "deep-run", "", "app", "quick", "approved")
	doc := reportJSON(t, root)
	if len(doc["runs"].([]any)) != 3001 {
		t.Fatal(doc["totals"])
	}
	if doc["not_searched_total"] != float64(0) {
		t.Fatal(doc["not_searched_total"])
	}
}

func TestReportRejectsALimitBelowOneOrAboveTheCap(t *testing.T) {
	for _, args := range [][]string{{"-max-depth", "0"}, {"-max-depth", "-1"}, {"-max-entries", "0"}, {"-max-entries", "16777217"}} {
		var out, diag bytes.Buffer
		if err := reportCommand(args, &out, &diag); err == nil || !strings.Contains(err.Error(), "must be") || out.Len() != 0 {
			t.Fatalf("args=%v err=%v out=%s", args, err, &out)
		}
	}
}

func TestADirectoryHoldingOnlyConfigJSONIsNotARun(t *testing.T) {
	root := recordRoot(t)
	writeFile(t, filepath.Join(root, "cfg", "config.json"), `{"RunID":"not-a-run"}`)
	out, diag := captureReport(t, root)
	if out != "no runs found\n" || diag != "" {
		t.Fatalf("%s\n%s", out, diag)
	}
	writeFile(t, filepath.Join(root, "cfg2", "config.json"), `{}`)
	reportFixture(t, filepath.Join(root, "cfg2", "child"), "real", "", "app", "quick", "approved")
	rows := reportRows(t, root)
	if len(rows) != 1 || rows[0].(map[string]any)["run_id"] != "real" {
		t.Fatal(rows)
	}
}

func TestReportNeverLetsAnIncompleteCopyHideDisagreementBetweenOtherCopies(t *testing.T) {
	root := recordRoot(t)
	reportFixture(t, filepath.Join(root, "a"), "same", "", "", "quick", "approved")
	first := reportFixture(t, filepath.Join(root, "b"), "same", "", "app", "quick", "approved")
	second := reportFixture(t, filepath.Join(root, "c"), "same", "", "other", "quick", "approved")
	out, diag := captureReport(t, "-json", root)
	rows := reportRows(t, root)
	if len(rows) != 2 || !strings.Contains(diag, first) || !strings.Contains(diag, second) {
		t.Fatalf("%s\n%s", out, diag)
	}
}
