package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jystringfellow/kapellmeister/internal/loop"
)

func restoreSummary() ensembleSummary {
	return ensembleSummary{Result: loop.Result{RunID: "restore-run", Outcome: loop.Approved, Rounds: 1, Restores: []loop.Restore{{Step: "gate", Round: 1, Chairs: []string{"p:x"}, Paths: []string{"main.go", "new.go"}, Outcome: "retried"}}}}
}

func TestRunPrintsTheRestoreLinesInItsSummary(t *testing.T) {
	var out bytes.Buffer
	if err := finishRun(&out, restoreSummary()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "restored  gate[1] p:x: 2 paths changed (main.go, new.go), work restored, step retried") {
		t.Fatal(out.String())
	}
}

func TestWaitShowsTheRestoreAndRetryOfARun(t *testing.T) {
	root := recordRoot(t)
	data, err := json.Marshal(restoreSummary())
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "restore-run", "result.json"), string(data))
	out, diag, code := waitSubprocess(t, "restore-run", root)
	if code != 0 || diag != "" || !strings.Contains(out, "work restored, step retried") {
		t.Fatalf("%d %s %s", code, out, diag)
	}
}

func TestReportCountsRunsWithRestoresAndLeavesOtherOutputUnchanged(t *testing.T) {
	root := recordRoot(t)
	reportFixture(t, root, "plain", "", "app", "quick", "approved")
	before, _ := captureReport(t, root)
	data, err := json.Marshal(restoreSummary())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "restore-run", "result.json")
	writeFile(t, path, string(data))
	doc := reportJSON(t, root)
	totals := doc["totals"].(map[string]any)
	if totals["restores"] != float64(1) || totals["retries"] != float64(1) {
		t.Fatalf("%v", totals)
	}
	found := false
	for _, row := range doc["runs"].([]any) {
		r := row.(map[string]any)
		if r["run_id"] == "restore-run" {
			found = r["restores"] == float64(1) && r["retries"] == float64(1)
		}
	}
	if !found {
		t.Fatalf("%v", doc)
	}
	out, _ := captureReport(t, root)
	if !strings.Contains(out, "restored: 1\n") || !strings.Contains(out, "retried: 1\n") {
		t.Fatal(out)
	}
	if err := os.RemoveAll(filepath.Dir(path)); err != nil {
		t.Fatal(err)
	}
	after, _ := captureReport(t, root)
	if before != after {
		t.Fatalf("plain output changed:\n%s\n%s", before, after)
	}
}

func TestARestoreRecordDoesNotCountAsAnUnattributedStepInTheReport(t *testing.T) {
	root := recordRoot(t)
	partsFixture(t, root, "one", "approved", 1, map[string]any{"step": "restore", "restored": true, "paths": []string{"main.go"}})
	p := partsDocument(t, root)
	if len(p["rows"].([]any)) != 0 || p["unattributed_steps"] != float64(0) {
		t.Fatalf("%v", p)
	}
}
