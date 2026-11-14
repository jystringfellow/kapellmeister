package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestAnOldRunRecordWithUnknownModelStillReportsTheSameTotalsAndParts(t *testing.T) {
	root := recordRoot(t)
	dir := reportFixture(t, root, "old", "2026-11-09T14:00:00Z", "app", "thorough", "approved")
	path := filepath.Join(dir, "result.json")
	tokens := `"input_tokens":2,"output_tokens":3,"cache_write_tokens":4,"cache_read_tokens":5`
	usage := `{"known":true,` + tokens + `,"models":{"unknown":{` + tokens + `}}}`
	result := `{"run_id":"old","outcome":"approved","usage":` + usage + `}`
	writeFile(t, path, result)
	telemetry := filepath.Join(dir, "telemetry.jsonl")
	start, err := os.ReadFile(telemetry)
	if err != nil {
		t.Fatal(err)
	}
	step := `{"step":"review","chair":{"provider":"antigravity_cli","model":"gemini-x"},"usage":` + usage + `}` + "\n"
	writeFile(t, telemetry, string(start)+step)
	before, diagnostics := captureReport(t, root)
	if diagnostics != "" || !strings.Contains(before, "usage: in 2 out 3") {
		t.Fatalf("report = %s, diagnostics = %s", before, diagnostics)
	}
	beforeJSON := reportJSON(t, "-parts", root)
	parts := beforeJSON["parts"].(map[string]any)["rows"].([]any)
	if len(parts) != 1 {
		t.Fatal(parts)
	}
	row := parts[0].(map[string]any)
	if row["model"] != "gemini-x" {
		t.Fatal(row)
	}
	requireNumbers(t, row["usage"].(map[string]any), map[string]int{"input_tokens": 2, "output_tokens": 3, "cache_write_tokens": 4, "cache_read_tokens": 5})
	for name, want := range map[string]string{"result.json": result, "telemetry.jsonl": string(start) + step} {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || string(data) != want {
			t.Fatalf("%s was changed: %s, %v", name, data, err)
		}
	}
	usageWithoutModels := `{"known":true,` + tokens + `}`
	writeFile(t, path, strings.Replace(result, usage, usageWithoutModels, 1))
	writeFile(t, telemetry, string(start)+strings.Replace(step, usage, usageWithoutModels, 1))
	after, diagnostics := captureReport(t, root)
	if diagnostics != "" || before != after || !reflect.DeepEqual(beforeJSON, reportJSON(t, "-parts", root)) {
		t.Fatalf("old model map changed report: before %s, after %s, diagnostics %s", before, after, diagnostics)
	}
}
