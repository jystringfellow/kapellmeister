package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/jystringfellow/kapellmeister/internal/loop"
)

func defaultScoreRun(t *testing.T) string {
	t.Helper()
	s := playStanding(t)
	chairs := chairsFromEnsembleFile(t, s.Worktree)
	tester := loop.Chair{Provider: "p", Profile: "tester", Model: "m"}
	chairs.Tester = &tester
	dir := filepath.Join(loop.StateDir(), "runs", "recorded")
	sum, err := runEnsemble(context.Background(), ensembleRun{
		RunID: "recorded", StateDir: dir, Ensemble: "thorough", Stepper: replyingStepper{}, Progress: io.Discard,
		Config: loop.Config{RunID: "recorded", Task: "probe", Worktree: s.Worktree, Chairs: chairs, MaxRounds: 1, ReplyGrace: time.Millisecond},
	})
	if err != nil || sum.Outcome != loop.Approved {
		t.Fatalf("run = %+v, %v", sum, err)
	}
	return dir
}

func reportGolden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if os.Getenv("KM_UPDATE_GOLDEN") != "" {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Fatalf("%s differs from what km report prints now; run with KM_UPDATE_GOLDEN=1 only when the change is meant\n%s", path, got)
	}
}

func TestReportPrintsThisTableForARunOfTheDefaultScore(t *testing.T) {
	out, diagnostics := captureReport(t, "-parts", defaultScoreRun(t))
	if diagnostics != "" {
		t.Fatal(diagnostics)
	}
	out = regexp.MustCompile(`\d{4}-\d\d-\d\d \d\d:\d\d:\d\d`).ReplaceAllString(out, "YYYY-MM-DD HH:MM:SS")
	out = regexp.MustCompile(`(approved  1       )\d+s`).ReplaceAllString(out, "${1}Ns")
	reportGolden(t, "default_score_report.golden", out)
}

func TestReportPrintsThisJSONForARunOfTheDefaultScore(t *testing.T) {
	document := reportJSON(t, "-parts", defaultScoreRun(t))
	for _, r := range document["runs"].([]any) {
		run := r.(map[string]any)
		run["path"], run["started"], run["duration_ms"] = "<run>", "<started>", "<duration>"
	}
	for _, r := range document["parts"].(map[string]any)["rows"].([]any) {
		row := r.(map[string]any)
		row["duration_ms"], row["duration_ms_per_step"] = "<duration>", "<duration>"
	}
	got, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	reportGolden(t, "default_score_report_json.golden", string(got)+"\n")
}
