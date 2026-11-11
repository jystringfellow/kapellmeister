package main

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/jystringfellow/kapellmeister/internal/loop"
)

var statusNow = time.Date(2026, 11, 11, 6, 0, 0, 0, time.UTC)

func statusFixture(t *testing.T, root, id string) runState {
	t.Helper()
	host, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	token, err := processStart(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	s := runState{RunID: id, Started: statusNow.Add(-time.Hour), Updated: statusNow.Add(-time.Minute), Repository: "host/owner/app", Ensemble: "thorough", Command: "run", Round: 2, MaxRounds: 3, PID: os.Getpid(), Host: host, ProcessStart: token, Playing: []loop.ActiveStep{}}
	saveStatusFixture(t, root, id, s)
	return s
}

func saveStatusFixture(t *testing.T, root, id string, s runState) {
	t.Helper()
	data, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, id, "current.json"), string(data))
}

func captureStatus(t *testing.T, args ...string) (string, string) {
	t.Helper()
	var out, diagnostics bytes.Buffer
	if err := statusAt(args, &out, &diagnostics, statusNow); err != nil {
		t.Fatal(err)
	}
	return out.String(), diagnostics.String()
}

func statusRows(t *testing.T, args ...string) []statusRun {
	t.Helper()
	out, _ := captureStatus(t, append([]string{"-json"}, args...)...)
	var doc struct {
		Runs []statusRun `json:"runs"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatal(err)
	}
	return doc.Runs
}

func TestStatusShowsARunInTheMiddleOfAStepWithItsChairAndHowLongTheStepHasGone(t *testing.T) {
	root := t.TempDir()
	s := statusFixture(t, root, "playing")
	s.Playing = []loop.ActiveStep{{Step: "implement", Chair: "leads.implementer@thorough", Started: statusNow.Add(-192 * time.Second)}}
	saveStatusFixture(t, root, "playing", s)
	out, diagnostics := captureStatus(t, root)
	flat := strings.Join(strings.Fields(out), " ")
	if diagnostics != "" || !strings.Contains(flat, "playing 2026-11-11 05:00:00 host/owner/app thorough 2/3 implement leads.implementer@thorough 3m12s alive") {
		t.Fatalf("%s\n%s", out, diagnostics)
	}
}

func TestStatusShowsARunBetweenStepsAsBetweenSteps(t *testing.T) {
	root := t.TempDir()
	statusFixture(t, root, "between")
	out, _ := captureStatus(t, root)
	if !strings.Contains(strings.Join(strings.Fields(out), " "), "2/3 between steps - 1m0s alive") {
		t.Fatal(out)
	}
}

func TestStatusShowsTheLongestPlayingStepAndCountsTheOthersWhenStepsOverlap(t *testing.T) {
	root := t.TempDir()
	s := statusFixture(t, root, "parallel")
	s.Playing = []loop.ActiveStep{{Step: "review", ChairName: "style", Started: statusNow.Add(-time.Second)}, {Step: "review", Chair: "review.security@thorough", Started: statusNow.Add(-time.Minute)}}
	saveStatusFixture(t, root, "parallel", s)
	out, _ := captureStatus(t, root)
	if !strings.Contains(strings.Join(strings.Fields(out), " "), "review +1 review.security@thorough 1m0s alive") {
		t.Fatal(out)
	}
	if rows := statusRows(t, root); len(rows[0].Playing) != 2 {
		t.Fatal(rows)
	}
}

func statusChild(t *testing.T) *exec.Cmd {
	t.Helper()
	child := exec.Command("sleep", "60")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = child.Process.Kill(); _ = child.Wait() })
	return child
}

func TestStatusShowsARunWhoseProcessIsGoneAsAbandoned(t *testing.T) {
	root := t.TempDir()
	s := statusFixture(t, root, "gone")
	child := statusChild(t)
	s.PID = child.Process.Pid
	var err error
	s.ProcessStart, err = processStart(s.PID)
	if err != nil {
		t.Fatal(err)
	}
	saveStatusFixture(t, root, "gone", s)
	if got := statusRows(t, root)[0].Liveness; got != "alive" {
		t.Fatal(got)
	}
	_ = child.Process.Kill()
	_ = child.Wait()
	if got := statusRows(t, root)[0].Liveness; got != "abandoned" {
		t.Fatal(got)
	}
}

func TestStatusDoesNotCountARecycledProcessIdAsAlive(t *testing.T) {
	root := t.TempDir()
	s := statusFixture(t, root, "reused")
	s.ProcessStart += "different"
	saveStatusFixture(t, root, "reused", s)
	if got := statusRows(t, root)[0].Liveness; got != "abandoned" {
		t.Fatal(got)
	}
}

func TestStatusDoesNotListAFinishedRun(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	statusFixture(t, a, "same")
	reportFixture(t, b, "same", "", "app", "thorough", "approved")
	for _, dirs := range [][]string{{a, b}, {b, a}, {b}} {
		if rows := statusRows(t, dirs...); len(rows) != 0 {
			t.Fatal(rows)
		}
	}
}

func TestStatusSaysUnknownForRecordsCopiedFromAnotherHost(t *testing.T) {
	root := t.TempDir()
	s := statusFixture(t, root, "copy")
	s.Host += "-elsewhere"
	saveStatusFixture(t, root, "copy", s)
	if got := statusRows(t, root)[0].Liveness; got != "unknown" {
		t.Fatal(got)
	}
}

func TestStatusListsARunThatPredatesCurrentJSONAsUnknown(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "old", "telemetry.jsonl"), "{\"at\":\"2026-11-09T14:00:00Z\"}\n")
	rows := statusRows(t, root)
	if len(rows) != 1 || rows[0].Liveness != "unknown" || len(rows[0].Playing) != 0 {
		t.Fatal(rows)
	}
}

func TestStatusJSONPrintsTheSameRunsWithTheirPlayingStepsAndLiveness(t *testing.T) {
	root := t.TempDir()
	s := statusFixture(t, root, "json")
	s.Playing = []loop.ActiveStep{{Step: "verify-2", Started: statusNow.Add(-1234 * time.Millisecond)}}
	saveStatusFixture(t, root, "json", s)
	rows := statusRows(t, root)
	if len(rows) != 1 {
		t.Fatal(rows)
	}
	r := rows[0]
	if r.RunID != "json" || r.Repository != s.Repository || r.Ensemble != s.Ensemble || r.Command != "run" || r.Round != 2 || r.MaxRounds != 3 || r.Liveness != "alive" || r.Playing[0].ElapsedMS != 1234 || r.Playing[0].Step != "verify-2" || r.Playing[0].Chair != "" {
		t.Fatalf("%+v", r)
	}
	s.Playing[0].Started = statusNow.Add(time.Hour)
	saveStatusFixture(t, root, "json", s)
	if got := statusRows(t, root)[0].Playing[0].ElapsedMS; got != 0 {
		t.Fatal(got)
	}
}

func TestStatusWithNothingInProgressSaysSoAndExitsZero(t *testing.T) {
	t.Setenv(loop.StateEnv, t.TempDir())
	if out, _ := captureStatus(t); out != "no runs in progress\n" {
		t.Fatal(out)
	}
	if out, _ := captureStatus(t, "-json"); out != "{\"runs\":[]}\n" {
		t.Fatal(out)
	}
	var out, diag bytes.Buffer
	if err := statusAt([]string{filepath.Join(t.TempDir(), "missing")}, &out, &diag, statusNow); err == nil || out.Len() != 0 {
		t.Fatalf("%v %s", err, &out)
	}
}

func TestStatusEscapesControlCharactersInRecordedValues(t *testing.T) {
	root := t.TempDir()
	s := statusFixture(t, root, "unsafe")
	value := "bad\x1b[31m\n\t\u202e\u2028"
	s.Repository = value
	s.Ensemble = value
	s.Playing = []loop.ActiveStep{{Step: value, Chair: value, Started: statusNow}}
	saveStatusFixture(t, root, "unsafe", s)
	for _, args := range [][]string{{root}, {root, "-json"}} {
		out, _ := captureStatus(t, args...)
		for _, r := range out {
			if !unicode.IsPrint(r) && r != '\n' && r != '\t' {
				t.Fatalf("unsafe output %q", out)
			}
		}
	}
	if got := statusRows(t, root)[0]; got.Repository != value || got.Playing[0].Chair != value {
		t.Fatal(got)
	}
}

func TestStatusNeverFollowsASymbolicLink(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	statusFixture(t, outside, "outside")
	if err := os.Symlink(outside, filepath.Join(root, "directory")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "linked", "telemetry.jsonl"), "{\"at\":\"2026-11-09T14:00:00Z\"}\n")
	if err := os.Symlink(filepath.Join(outside, "outside", "current.json"), filepath.Join(root, "linked", "current.json")); err != nil {
		t.Fatal(err)
	}
	rows := statusRows(t, root)
	if len(rows) != 1 || rows[0].RunID != "linked" || rows[0].Liveness != "unknown" || rows[0].Repository != "" {
		t.Fatal(rows)
	}
	var out, diag bytes.Buffer
	if err := statusAt([]string{filepath.Join(root, "directory")}, &out, &diag, statusNow); err == nil || out.Len() != 0 {
		t.Fatalf("followed explicit link: %v %s", err, &out)
	}
}

func TestStatusOnlyReads(t *testing.T) {
	root := t.TempDir()
	statusFixture(t, root, "readonly")
	snapshot := func() map[string]string {
		result := map[string]string{}
		if err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			result[path] = info.ModTime().String()
			if !d.IsDir() {
				data, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				result[path] += string(data)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		return result
	}
	before := snapshot()
	captureStatus(t, root)
	statusRows(t, root)
	if after := snapshot(); !reflect.DeepEqual(before, after) {
		t.Fatalf("inputs changed: %v %v", before, after)
	}
}

func TestStatusReadsEveryDirectoryGivenAndTheDefaultStateRuns(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	t.Setenv(loop.StateEnv, a)
	statusFixture(t, filepath.Join(a, "runs"), "default")
	statusFixture(t, filepath.Join(b, "sandbox", "runs"), "nested")
	if rows := statusRows(t); len(rows) != 1 || rows[0].RunID != "default" {
		t.Fatal(rows)
	}
	if rows := statusRows(t, a, b); len(rows) != 2 || rows[0].RunID != "default" || rows[1].RunID != "nested" {
		t.Fatal(rows)
	}
}

func TestReportCountsARunInItsFirstStepAsUnfinishedNotUnreadable(t *testing.T) {
	root := t.TempDir()
	statusFixture(t, root, "first")
	writeFile(t, filepath.Join(root, "first", "telemetry.jsonl"), "")
	rows := reportRows(t, root)
	if len(rows) != 1 || rows[0].(map[string]any)["outcome"] != "unfinished" {
		t.Fatal(rows)
	}
}

func TestProcessStartIdentifiesOneProcessInstance(t *testing.T) {
	a, err := processStart(os.Getpid())
	if err != nil || a == "" {
		t.Fatalf("%q %v", a, err)
	}
	b, err := processStart(os.Getpid())
	if err != nil || a != b {
		t.Fatalf("%q %v", b, err)
	}
	child := statusChild(t)
	c, err := processStart(child.Process.Pid)
	if err != nil || c == a {
		t.Fatalf("%q %v", c, err)
	}
	_ = child.Process.Kill()
	_ = child.Wait()
	if _, err := processStart(child.Process.Pid); err == nil {
		t.Fatal("dead process has a token")
	}
}

func TestKmUsageListsTheStatusCommandAndDispatchesIt(t *testing.T) {
	var diagnostics bytes.Buffer
	if code := execute(nil, &diagnostics); code != 2 || !strings.Contains(diagnostics.String(), "km status") {
		t.Fatalf("%d %s", code, &diagnostics)
	}
	diagnostics.Reset()
	if code := execute([]string{"status", "-invalid"}, &diagnostics); code != 1 || !strings.Contains(diagnostics.String(), "flag provided") {
		t.Fatalf("%d %s", code, &diagnostics)
	}
}

func TestReportStillReadsARunThatHasCurrentJSON(t *testing.T) {
	root := t.TempDir()
	s := statusFixture(t, root, "first")
	s.Playing = []loop.ActiveStep{{Step: "plan", Started: statusNow}}
	saveStatusFixture(t, root, "first", s)
	writeFile(t, filepath.Join(root, "first", "telemetry.jsonl"), "")
	out, diagnostics := captureReport(t, "-json", root)
	if diagnostics != "" {
		t.Fatalf("a first step is valid: %s", diagnostics)
	}
	var report runReport
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatal(err)
	}
	if report.Totals.Runs != 1 || report.Totals.Outcomes["unfinished"] != 1 || report.Runs[0].Started == nil || !report.Runs[0].Started.Equal(s.Started) || report.Runs[0].Repository != s.Repository {
		t.Fatalf("%+v", report)
	}
	reportFixture(t, root, "first", "2026-11-11T05:00:00Z", s.Repository, s.Ensemble, "approved")
	rows := reportRows(t, root)
	if len(rows) != 1 || rows[0].(map[string]any)["outcome"] != "approved" {
		t.Fatal(rows)
	}
}

func TestStatusKeepsRunsWithUnreadableCurrentStateAndDiagnosesTheRecords(t *testing.T) {
	root := t.TempDir()
	for name, body := range map[string]string{"malformed": "{", "null": "null", "oversized": strings.Repeat(" ", (1<<20)+1), "no-start": "{}"} {
		writeFile(t, filepath.Join(root, name, "current.json"), body)
		writeFile(t, filepath.Join(root, name, "telemetry.jsonl"), "{\"at\":\"2026-11-09T14:00:00Z\"}\n")
	}
	rows := statusRows(t, root)
	if len(rows) != 4 {
		t.Fatal(rows)
	}
	for _, row := range rows {
		if row.Liveness != "unknown" || len(row.Playing) != 0 || row.Started == nil {
			t.Fatal(row)
		}
	}
	_, diagnostics := captureStatus(t, root)
	for _, name := range []string{"malformed", "null", "oversized", "no-start"} {
		if !strings.Contains(diagnostics, filepath.Join(root, name, "current.json")) {
			t.Fatal(diagnostics)
		}
	}
}

func TestStatusSaysUnknownWithoutARecordedProcessIdentity(t *testing.T) {
	for _, change := range []func(*runState){func(s *runState) { s.PID = 0 }, func(s *runState) { s.PID = -1 }, func(s *runState) { s.Host = "" }, func(s *runState) { s.ProcessStart = "" }} {
		root := t.TempDir()
		s := statusFixture(t, root, "unknown")
		change(&s)
		saveStatusFixture(t, root, "unknown", s)
		if got := statusRows(t, root)[0].Liveness; got != "unknown" {
			t.Fatal(got)
		}
	}
}

func TestStatusListsARunWithEmptyTelemetryAsInProgressWithNothingOnStderr(t *testing.T) {
	for _, body := range []string{"", "\n \t\n"} {
		t.Run(body, func(t *testing.T) {
			root := t.TempDir()
			id := "20261110-143005-a1b2c3"
			reportWithoutTelemetryRecords(t, root, id, body)
			out, diagnostics := captureStatus(t, "-json", root)
			var doc struct {
				Runs []statusRun `json:"runs"`
			}
			if err := json.Unmarshal([]byte(out), &doc); err != nil {
				t.Fatal(err)
			}
			if diagnostics != "" || len(doc.Runs) != 1 {
				t.Fatalf("status = %s, diagnostics = %s", out, diagnostics)
			}
			run := doc.Runs[0]
			if run.RunID != id || run.Started == nil || run.Started.Format(time.RFC3339) != "2026-11-10T14:30:05Z" || run.Liveness != "unknown" || len(run.Playing) != 0 {
				t.Fatalf("run = %+v", run)
			}
		})
	}
}
