package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
	"unicode"

	"github.com/jystringfellow/kapellmeister/internal/loop"
)

func reportFixture(t *testing.T, root, id, at, repository, ensemble, outcome string) string {
	t.Helper()
	dir := filepath.Join(root, id)
	data, err := json.Marshal(map[string]any{"run_id": id, "repository": repository, "ensemble": ensemble, "outcome": outcome, "rounds": 2, "duration_ms": 1111000})
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "result.json"), string(data))
	if at != "" {
		writeFile(t, filepath.Join(dir, "telemetry.jsonl"), fmt.Sprintf("{\"at\":%q,\"run_id\":%q,\"step\":\"start\"}\n", at, id))
	}
	return dir
}

func captureReport(t *testing.T, args ...string) (string, string) {
	t.Helper()
	var out, diagnostics bytes.Buffer
	if err := reportCommand(args, &out, &diagnostics); err != nil {
		t.Fatal(err)
	}
	return out.String(), diagnostics.String()
}

func reportJSON(t *testing.T, args ...string) map[string]any {
	t.Helper()
	out, _ := captureReport(t, append([]string{"-json"}, args...)...)
	var document map[string]any
	if err := json.Unmarshal([]byte(out), &document); err != nil {
		t.Fatalf("invalid JSON: %v: %s", err, out)
	}
	return document
}

func reportRows(t *testing.T, args ...string) []any {
	t.Helper()
	return reportJSON(t, args...)["runs"].([]any)
}

func TestReportPrintsOneRowPerRunOldestFirstThenTotalsByOutcomeRepositoryAndEnsemble(t *testing.T) {
	root := recordRoot(t)
	reportFixture(t, root, "a", "2026-11-10T14:00:00Z", "host/owner/app", "thorough", "failed")
	reportFixture(t, root, "b", "2026-11-09T14:00:00Z", "host/owner/app", "thorough", "approved")
	reportFixture(t, root, "c", "2026-11-11T14:00:00Z", "host/owner/other", "quick", "rounds_exhausted")
	reportFixture(t, root, "d", "2026-11-12T14:00:00Z", "host/owner/other", "quick", "untested")
	out, diagnostics := captureReport(t, root)
	if diagnostics != "" {
		t.Fatal(diagnostics)
	}
	var lines []string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		lines = append(lines, strings.Join(strings.Fields(line), " "))
	}
	want := []string{
		"STARTED REPOSITORY ENSEMBLE OUTCOME ROUNDS TOOK TOKENS",
		"2026-11-09 14:00:00 host/owner/app thorough approved 2 18m31s unknown",
		"2026-11-10 14:00:00 host/owner/app thorough failed 2 18m31s unknown",
		"2026-11-11 14:00:00 host/owner/other quick rounds_exhausted 2 18m31s unknown",
		"2026-11-12 14:00:00 host/owner/other quick untested 2 18m31s unknown",
		"", "runs: 4", "usage: unknown (4 without usage)", "by outcome", "approved 1", "failed 1", "rounds_exhausted 1", "untested 1",
		"by repository", "host/owner/app 2 unknown (2 without usage)", "host/owner/other 2 unknown (2 without usage)", "by ensemble", "quick 2 unknown (2 without usage)", "thorough 2 unknown (2 without usage)",
	}
	if !reflect.DeepEqual(lines, want) {
		t.Fatalf("report:\n%s\nwant lines: %q", out, want)
	}
}

func TestReportStillCountsIdenticalCopiesOfARunOnce(t *testing.T) {
	a, b := recordRoot(t), recordRoot(t)
	first := reportFixture(t, a, "same", "2026-11-09T14:00:00Z", "app", "first", "approved")
	reportFixture(t, filepath.Join(b, "sandbox", "state", "runs"), "same", "2026-11-09T14:00:00Z", "app", "first", "approved")
	rows := reportRows(t, a, b, a)
	if len(rows) != 1 || rows[0].(map[string]any)["path"] != first || rows[0].(map[string]any)["outcome"] != "approved" {
		t.Fatalf("runs = %v", rows)
	}
}

func TestReportPrefersAFinishedCopyOverAnUnfinishedOneOfTheSameRun(t *testing.T) {
	a, b := recordRoot(t), recordRoot(t)
	writeFile(t, filepath.Join(a, "same", "telemetry.jsonl"), "{\"at\":\"2026-11-09T14:00:00Z\"}\n")
	reportFixture(t, b, "same", "2026-11-09T14:00:00Z", "app", "thorough", "approved")
	for _, dirs := range [][]string{{a, b}, {b, a}} {
		rows := reportRows(t, dirs...)
		if len(rows) != 1 || rows[0].(map[string]any)["outcome"] != "approved" {
			t.Fatalf("runs = %v", rows)
		}
	}
}

func TestReportCountsADirectoryWithTelemetryAndNoResultAsUnfinished(t *testing.T) {
	root := recordRoot(t)
	writeFile(t, filepath.Join(root, "unfinished", "telemetry.jsonl"), "{\"at\":\"2026-11-09T15:00:00+01:00\"}\nnot JSON\n")
	out, diagnostics := captureReport(t, root)
	if diagnostics != "" || !strings.Contains(strings.Join(strings.Fields(out), " "), "2026-11-09 14:00:00 unknown unknown unfinished unknown unknown") || !strings.Contains(out, "runs: 1") {
		t.Fatalf("report = %s, diagnostics = %s", out, diagnostics)
	}
	row := reportRows(t, root)[0].(map[string]any)
	if row["outcome"] != "unfinished" || row["rounds"] != nil || row["duration_ms"] != nil {
		t.Fatalf("unfinished = %v", row)
	}
}

func TestReportShowsUnknownForAnOlderRecordWithoutEnsembleOrRepositoryAndDoesNotFail(t *testing.T) {
	root := recordRoot(t)
	writeFile(t, filepath.Join(root, "old", "result.json"), `{"run_id":"old","outcome":"approved","rounds":0,"duration_ms":0}`)
	out, diagnostics := captureReport(t, root)
	if diagnostics != "" || !strings.Contains(strings.Join(strings.Fields(out), " "), "unknown unknown unknown approved 0 0s") {
		t.Fatalf("report = %s, diagnostics = %s", out, diagnostics)
	}
	row := reportRows(t, root)[0].(map[string]any)
	for _, name := range []string{"started", "repository", "ensemble", "reason"} {
		if _, ok := row[name]; ok {
			t.Errorf("absent field %s was emitted: %v", name, row)
		}
	}
}

func TestReportReportsAMalformedRecordOnStderrWithItsPathCountsItUnreadableAndKeepsGoing(t *testing.T) {
	root := recordRoot(t)
	for name, body := range map[string]string{
		"bad-json": "{", "wrong-type": `{"rounds":"two"}`, "oversized": strings.Repeat(" ", (1<<20)+1), "null": "null", "array": "[]",
	} {
		writeFile(t, filepath.Join(root, name, "result.json"), body)
	}
	writeFile(t, filepath.Join(root, "bad-telemetry", "telemetry.jsonl"), "not JSON\n")
	reportFixture(t, root, "good", "2026-11-09T14:00:00Z", "app", "thorough", "approved")
	out, diagnostics := captureReport(t, root)
	if !strings.Contains(out, "runs: 7") || !strings.Contains(strings.Join(strings.Fields(out), " "), "unreadable 6") || !strings.Contains(out, "approved") {
		t.Fatal(out)
	}
	for _, name := range []string{"bad-json", "wrong-type", "oversized", "null", "array", "bad-telemetry"} {
		if !strings.Contains(diagnostics, "km: report: "+filepath.Join(root, name)) {
			t.Errorf("diagnostics omit %s: %s", name, diagnostics)
		}
	}
}

func TestReportJSONPrintsTheSameRunsAndTotalsAsTheTable(t *testing.T) {
	root := recordRoot(t)
	dir := reportFixture(t, root, "a", "2026-11-09T14:00:00Z", "app", "thorough", "approved")
	writeFile(t, filepath.Join(root, "b", "result.json"), `{"run_id":"b","outcome":"failed","reason":"chair stopped"}`)
	got := reportJSON(t, root)
	want := map[string]any{
		"runs": []any{
			map[string]any{"run_id": "a", "path": dir, "started": "2026-11-09T14:00:00Z", "repository": "app", "ensemble": "thorough", "outcome": "approved", "rounds": float64(2), "duration_ms": float64(1111000)},
			map[string]any{"run_id": "b", "path": filepath.Join(root, "b"), "outcome": "failed", "reason": "chair stopped"},
		},
		"totals": map[string]any{"runs": float64(2), "outcomes": map[string]any{"approved": float64(1), "failed": float64(1)}, "repositories": map[string]any{"app": float64(1), "unknown": float64(1)}, "ensembles": map[string]any{"thorough": float64(1), "unknown": float64(1)}},
	}
	want["not_searched"] = []any{}
	want["not_searched_total"] = float64(0)
	for _, row := range want["runs"].([]any) {
		row.(map[string]any)["usage"] = map[string]any{"known": false}
	}
	unknownTotal := func(n int) map[string]any {
		return map[string]any{"known_runs": float64(0), "unknown_runs": float64(n), "input_tokens": float64(0), "output_tokens": float64(0), "cache_read_tokens": float64(0), "cache_write_tokens": float64(0)}
	}
	totals := want["totals"].(map[string]any)
	totals["usage"] = unknownTotal(2)
	totals["ensemble_usage"] = map[string]any{"thorough": unknownTotal(1), "unknown": unknownTotal(1)}
	totals["repository_usage"] = map[string]any{"app": unknownTotal(1), "unknown": unknownTotal(1)}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("JSON = %#v, want %#v", got, want)
	}
}

func TestReportSinceKeepsOnlyRunsStartedOnOrAfterTheDate(t *testing.T) {
	root := recordRoot(t)
	reportFixture(t, root, "before", "2026-11-08T23:59:59Z", "app", "thorough", "approved")
	reportFixture(t, root, "at", "2026-11-09T00:00:00Z", "app", "thorough", "approved")
	reportFixture(t, root, "after", "2026-11-09T01:00:00Z", "other", "thorough", "approved")
	reportFixture(t, root, "unknown", "", "app", "thorough", "approved")
	for _, since := range []string{"2026-11-09", "2026-11-09T01:00:00+01:00"} {
		rows := reportRows(t, root, "-since", since)
		if len(rows) != 2 || rows[0].(map[string]any)["run_id"] != "at" {
			t.Fatalf("since %s: %v", since, rows)
		}
	}
	var out, diagnostics bytes.Buffer
	if err := reportCommand([]string{"-since", "yesterday", root}, &out, &diagnostics); err == nil || out.Len() != 0 {
		t.Fatalf("invalid date: err = %v, out = %s", err, &out)
	}
}

func TestReportRepositoryKeepsOnlyRunsOfThatRepositoryAndCombinesWithSince(t *testing.T) {
	root := recordRoot(t)
	reportFixture(t, root, "before", "2026-11-08T23:59:59Z", "app", "thorough", "approved")
	reportFixture(t, root, "after", "2026-11-09T00:00:00Z", "app", "thorough", "approved")
	reportFixture(t, root, "other", "2026-11-09T01:00:00Z", "app-extra", "thorough", "approved")
	reportFixture(t, root, "unknown", "", "", "", "approved")
	writeFile(t, filepath.Join(root, "bad", "result.json"), "{")
	if rows := reportRows(t, root, "-repository", "app"); len(rows) != 2 {
		t.Fatal(rows)
	}
	rows := reportRows(t, "-since", "2026-11-09", root, "-repository=app")
	if len(rows) != 1 || rows[0].(map[string]any)["run_id"] != "after" {
		t.Fatal(rows)
	}
	out, diagnostics := captureReport(t, root, "-repository", "missing")
	if out != "no runs found\n" || !strings.Contains(diagnostics, "bad/result.json") {
		t.Fatalf("out = %s, diagnostics = %s", out, diagnostics)
	}
}

func TestReportEscapesControlCharactersInEveryValueItPrints(t *testing.T) {
	root := recordRoot(t)
	value := "bad\x1b[31m\n\t\u202e\u2028\x7f\u0085"
	reportFixture(t, root, "good", "2026-11-09T14:00:00Z", value, value, value)
	writeFile(t, filepath.Join(root, value, "result.json"), "{")
	out, diagnostics := captureReport(t, root)
	for _, text := range []string{out, diagnostics} {
		for _, r := range text {
			if !unicode.IsPrint(r) && r != '\n' && r != '\t' {
				t.Fatalf("raw control %U in %q", r, text)
			}
		}
		for _, escaped := range []string{`\x1b`, `\n`, `\t`, `\u202e`, `\u2028`, `\x7f`, `\u0085`} {
			if !strings.Contains(text, escaped) {
				t.Errorf("missing %s in %q", escaped, text)
			}
		}
	}
	if lines := strings.Split(strings.Split(out, "\n\n")[0], "\n"); len(lines) != 3 || len(strings.Fields(lines[1])) != 8 {
		t.Fatalf("row broken: %q", out)
	}
	jsonOut, _ := captureReport(t, "-json", root)
	if strings.ContainsRune(jsonOut, '\x1b') || !json.Valid([]byte(jsonOut)) {
		t.Fatalf("unsafe JSON: %q", jsonOut)
	}
	if rows := reportRows(t, root); rows[0].(map[string]any)["repository"] != value {
		t.Fatal(rows)
	}
}

func TestReportNeverFollowsASymbolicLinkWhileSearching(t *testing.T) {
	root, outside := recordRoot(t), recordRoot(t)
	dir := reportFixture(t, outside, "outside", "", "outside", "outside", "approved")
	for link, target := range map[string]string{
		"directory": outside, "loop": root, "linked-result/result.json": filepath.Join(dir, "result.json"), "linked-telemetry/telemetry.jsonl": filepath.Join(dir, "result.json"),
	} {
		path := filepath.Join(root, link)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, path); err != nil {
			t.Fatal(err)
		}
	}
	rows := reportRows(t, root)
	if len(rows) != 2 {
		t.Fatal(rows)
	}
	for _, row := range rows {
		if row.(map[string]any)["outcome"] != "unreadable" {
			t.Fatal(rows)
		}
	}
	alias := filepath.Join(recordRoot(t), "chosen-root")
	if err := os.Symlink(outside, alias); err != nil {
		t.Fatal(err)
	}
	if rows := reportRows(t, alias); len(rows) != 1 || rows[0].(map[string]any)["run_id"] != "outside" {
		t.Fatal(rows)
	}
}

func TestReportRejectsNonregularRecordsWithoutBlocking(t *testing.T) {
	root := recordRoot(t)
	if err := os.MkdirAll(filepath.Join(root, "directory", "result.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "fifo"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(root, "fifo", "result.json"), 0o600); err != nil {
		t.Fatal(err)
	}
	rows := reportRows(t, root)
	if len(rows) != 2 {
		t.Fatal(rows)
	}
	for _, row := range rows {
		if row.(map[string]any)["outcome"] != "unreadable" {
			t.Fatal(rows)
		}
	}
}

func TestReportReadsTheStateDirectoryRunsWhenGivenNoDirectory(t *testing.T) {
	t.Setenv(loop.HomeEnv, t.TempDir())
	state := recordRoot(t)
	t.Setenv(loop.StateEnv, state)
	if out, _ := captureReport(t); out != "no runs found\n" {
		t.Fatal(out)
	}
	if got := reportJSON(t)["runs"].([]any); len(got) != 0 {
		t.Fatal(got)
	}
	reportFixture(t, filepath.Join(state, "runs"), "default", "", "app", "thorough", "approved")
	if rows := reportRows(t); len(rows) != 1 || rows[0].(map[string]any)["run_id"] != "default" {
		t.Fatal(rows)
	}
}

func TestReportOnlyReadsAndNeverWritesToTheDirectoriesItIsGiven(t *testing.T) {
	root := recordRoot(t)
	reportFixture(t, root, "read-only", "2026-11-09T14:00:00Z", "app", "thorough", "approved")
	type entry struct {
		Size     int64
		Mode     fs.FileMode
		Modified time.Time
		Body     string
	}
	snapshot := func() map[string]entry {
		result := map[string]entry{}
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			var body []byte
			if !d.IsDir() {
				body, err = os.ReadFile(path)
				if err != nil {
					return err
				}
			}
			result[path] = entry{info.Size(), info.Mode(), info.ModTime(), string(body)}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	for _, path := range []string{root, filepath.Join(root, "read-only")} {
		if err := os.Chmod(path, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(path, 0o700) })
	}
	before := snapshot()
	captureReport(t, root)
	reportJSON(t, root)
	if after := snapshot(); !reflect.DeepEqual(before, after) {
		t.Fatalf("report changed input: before %v, after %v", before, after)
	}
}

func TestReportRejectsAMissingExplicitDirectoryBeforePrinting(t *testing.T) {
	root := recordRoot(t)
	reportFixture(t, root, "good", "", "app", "thorough", "approved")
	for _, path := range []string{filepath.Join(root, "missing"), filepath.Join(root, "good", "result.json")} {
		var out, diagnostics bytes.Buffer
		if err := reportCommand([]string{root, path}, &out, &diagnostics); err == nil || out.Len() != 0 {
			t.Fatalf("path %s: err = %v, output = %s", path, err, &out)
		}
	}
}

func TestReportSearchesEachDirectoryAtAnyDepthAndDoesNotDescendIntoARunDirectory(t *testing.T) {
	root := recordRoot(t)
	dir := reportFixture(t, filepath.Join(root, "copies", "sandbox-a", "state", "runs"), "real", "", "app", "thorough", "approved")
	reportFixture(t, filepath.Join(dir, "replies"), "phantom", "", "app", "thorough", "failed")
	rows := reportRows(t, root)
	if len(rows) != 1 || rows[0].(map[string]any)["run_id"] != "real" {
		t.Fatal(rows)
	}
}

func TestKmUsageListsTheReportCommandAndDispatchesIt(t *testing.T) {
	var diagnostics bytes.Buffer
	if code := execute(nil, &diagnostics); code != 2 || !strings.Contains(diagnostics.String(), "km report") {
		t.Fatalf("usage: %d %s", code, &diagnostics)
	}
	diagnostics.Reset()
	if code := execute([]string{"report", "-since", "bad-date"}, &diagnostics); code != 1 || !strings.Contains(diagnostics.String(), "-since") {
		t.Fatalf("dispatch: %d %s", code, &diagnostics)
	}
}

func TestReportReadsOnlyRecordedResultFieldsAndTakesItsStartFromTelemetry(t *testing.T) {
	root := recordRoot(t)
	dir := filepath.Join(root, "20261101-120000-local")
	writeFile(t, filepath.Join(dir, "result.json"), `{"outcome":"approved","started":[],"path":false}`)
	writeFile(t, filepath.Join(dir, "telemetry.jsonl"), "{\"at\":\"2026-11-09T14:00:00Z\"}\n{\"at\":\"2026-11-10T14:00:00Z\"}\n")
	rows := reportRows(t, root)
	row := rows[0].(map[string]any)
	if row["outcome"] != "approved" || row["started"] != "2026-11-09T14:00:00Z" || row["path"] != dir || row["run_id"] != filepath.Base(dir) || row["rounds"] != nil || row["duration_ms"] != nil {
		t.Fatalf("run = %v", row)
	}
}

func TestReportKeepsAFinishedResultWhenItsTelemetryIsUnusable(t *testing.T) {
	for _, body := range []string{"{", "{}", `{"at":"yesterday"}`, strings.Repeat("x", (1<<20)+1)} {
		t.Run(fmt.Sprint(len(body), body[:min(8, len(body))]), func(t *testing.T) {
			root := recordRoot(t)
			dir := reportFixture(t, root, "finished", "", "app", "thorough", "approved")
			writeFile(t, filepath.Join(dir, "telemetry.jsonl"), body)
			_, diagnostics := captureReport(t, root)
			if !strings.Contains(diagnostics, filepath.Join(dir, "telemetry.jsonl")) {
				t.Fatal(diagnostics)
			}
			row := reportRows(t, root)[0].(map[string]any)
			if row["outcome"] != "approved" || row["started"] != nil {
				t.Fatal(row)
			}
			if rows := reportRows(t, root, "-since", "2026-01-01"); len(rows) != 0 {
				t.Fatal(rows)
			}
		})
	}
}

func TestReportPrefersAnUnfinishedOrFinishedCopyOverAnUnreadableOneAndStillDiagnosesIt(t *testing.T) {
	a, b := recordRoot(t), recordRoot(t)
	writeFile(t, filepath.Join(a, "same", "result.json"), "{")
	writeFile(t, filepath.Join(b, "same", "telemetry.jsonl"), "{\"at\":\"2026-11-09T14:00:00Z\"}\n")
	for _, outcome := range []string{"unfinished", "approved"} {
		if outcome == "approved" {
			reportFixture(t, b, "same", "", "app", "thorough", "approved")
		}
		for _, dirs := range [][]string{{a, b}, {b, a}} {
			out, diagnostics := captureReport(t, append([]string{"-json"}, dirs...)...)
			var document struct{ Runs []struct{ Outcome string } }
			if err := json.Unmarshal([]byte(out), &document); err != nil {
				t.Fatal(err)
			}
			if len(document.Runs) != 1 || document.Runs[0].Outcome != outcome || !strings.Contains(diagnostics, filepath.Join(a, "same", "result.json")) {
				t.Fatalf("out = %s, diagnostics = %s", out, diagnostics)
			}
		}
	}
}

func TestReportOrdersTiedAndUnknownStartsByRunIDAndTotalsByCountThenName(t *testing.T) {
	root := recordRoot(t)
	for _, id := range []string{"b", "a"} {
		reportFixture(t, root, id, "2026-11-09T14:00:00Z", "z", "z", "failed")
	}
	for _, id := range []string{"d", "c"} {
		reportFixture(t, root, id, "", "a", "a", "approved")
	}
	reportFixture(t, root, "e", "", "z", "z", "failed")
	rows := reportRows(t, root)
	for i, id := range []string{"a", "b", "c", "d", "e"} {
		if rows[i].(map[string]any)["run_id"] != id {
			t.Fatal(rows)
		}
	}
	out, _ := captureReport(t, root)
	flat := strings.Join(strings.Fields(out), " ")
	for _, section := range []string{"by outcome failed 3 approved 2", "by repository z 3 unknown (3 without usage) a 2 unknown (2 without usage)", "by ensemble z 3 unknown (3 without usage) a 2 unknown (2 without usage)"} {
		if !strings.Contains(flat, section) {
			t.Fatalf("missing %s in %s", section, out)
		}
	}
}

func TestReportStopsAtAnUnfinishedRunAndLimitsOnlyTheFirstTelemetryLine(t *testing.T) {
	root := recordRoot(t)
	dir := filepath.Join(root, "unfinished")
	writeFile(t, filepath.Join(dir, "telemetry.jsonl"), "{\"at\":\"2026-11-09T14:00:00Z\"}\n"+strings.Repeat("x", (1<<20)+1))
	reportFixture(t, filepath.Join(dir, "scratch"), "phantom", "", "app", "thorough", "approved")
	rows := reportRows(t, root)
	if len(rows) != 1 || rows[0].(map[string]any)["outcome"] != "unfinished" {
		t.Fatal(rows)
	}
}

func TestReportJSONEscapesEveryNonprintingRuneWithoutChangingTheDecodedValues(t *testing.T) {
	root := recordRoot(t)
	value := "quoted\"\\\x00\x07\x1b\x7f\u0085\u009b\u202e\u2028\u2029\u2066\U000e0001"
	dir := reportFixture(t, root, strings.ReplaceAll(value, "\x00", ""), "", value, value, value)
	data, err := json.Marshal(map[string]any{"run_id": value, "repository": value, "ensemble": value, "outcome": value, "reason": value})
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "result.json"), string(data))
	out, diagnostics := captureReport(t, "-json", root)
	if diagnostics != "" {
		t.Fatal(diagnostics)
	}
	for _, r := range out {
		if !unicode.IsPrint(r) && r != '\n' {
			t.Errorf("JSON contains raw nonprinting rune %U", r)
		}
	}
	var document map[string]any
	if err := json.Unmarshal([]byte(out), &document); err != nil {
		t.Fatal(err)
	}
	row := document["runs"].([]any)[0].(map[string]any)
	for _, name := range []string{"run_id", "repository", "ensemble", "outcome", "reason"} {
		if row[name] != value {
			t.Errorf("decoded %s = %q, want %q", name, row[name], value)
		}
	}
	if row["path"] != dir {
		t.Errorf("decoded path = %q, want %q", row["path"], dir)
	}
	totals := document["totals"].(map[string]any)
	for _, name := range []string{"outcomes", "repositories", "ensembles"} {
		if totals[name].(map[string]any)[value] != float64(1) {
			t.Errorf("decoded %s = %v", name, totals[name])
		}
	}
}

func TestReportDiagnosesAnUnreadableSubdirectoryAndContinuesWithOtherRuns(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("directory permissions require an unprivileged user")
	}
	root := recordRoot(t)
	denied := filepath.Join(root, "deny\x1b\u202e")
	if err := os.Mkdir(denied, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(denied, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(denied, 0o700) })
	reportFixture(t, root, "good", "", "app", "thorough", "approved")
	for _, args := range [][]string{{root}, {"-json", root}} {
		out, diagnostics := captureReport(t, args...)
		if !strings.Contains(out, "approved") || !strings.Contains(out, "unreadable") {
			t.Fatalf("report lost runs: %s", out)
		}
		if !strings.Contains(diagnostics, "km: report: "+plain(denied)) || !strings.Contains(diagnostics, "permission denied") {
			t.Fatalf("missing directory diagnostic: %q", diagnostics)
		}
		for _, r := range diagnostics {
			if !unicode.IsPrint(r) && r != '\n' {
				t.Fatalf("unescaped directory diagnostic: %q", diagnostics)
			}
		}
	}
	var out, diagnostics bytes.Buffer
	if err := reportCommand([]string{denied}, &out, &diagnostics); err == nil || out.Len() != 0 {
		t.Fatalf("unreadable explicit root: err = %v, out = %s", err, &out)
	}
}

func TestReportContinuesWhenSubdirectoriesDisappearDuringTheSearch(t *testing.T) {
	root := recordRoot(t)
	reportFixture(t, root, "good", "", "app", "thorough", "approved")
	for i := 0; i < 200; i++ {
		if err := os.Mkdir(filepath.Join(root, fmt.Sprintf("churn-%03d", i)), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	stop := make(chan struct{})
	var workers sync.WaitGroup
	workers.Add(1)
	go func() {
		defer workers.Done()
		for {
			for i := 0; i < 200; i++ {
				select {
				case <-stop:
					return
				default:
				}
				path := filepath.Join(root, fmt.Sprintf("churn-%03d", i))
				_ = os.Remove(path)
				_ = os.Mkdir(path, 0o700)
			}
		}
	}()
	defer func() { close(stop); workers.Wait() }()
	for i := 0; i < 30; i++ {
		out, _ := captureReport(t, root)
		if !strings.Contains(out, "approved") {
			t.Fatalf("report lost stable run: %s", out)
		}
	}
}

func TestReportNeverReadsOutsideRunsWhileADirectoryIsSwappedForASymlink(t *testing.T) {
	root, outside := recordRoot(t), recordRoot(t)
	reportFixture(t, root, "good", "", "app", "thorough", "approved")
	writeFile(t, filepath.Join(outside, "result.json"), `{"run_id":"outside-sentinel","outcome":"outside-sentinel"}`)
	changing, held := filepath.Join(root, "changing"), filepath.Join(recordRoot(t), "held")
	writeFile(t, filepath.Join(changing, "result.json"), `{"run_id":"changing","outcome":"approved"}`)
	stop := make(chan struct{})
	var workers sync.WaitGroup
	workers.Add(1)
	go func() {
		defer workers.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			if os.Rename(changing, held) != nil {
				continue
			}
			_ = os.Symlink(outside, changing)
			_ = os.Remove(changing)
			_ = os.Rename(held, changing)
		}
	}()
	defer func() { close(stop); workers.Wait() }()
	for i := 0; i < 100; i++ {
		out, diagnostics := captureReport(t, "-json", root)
		if strings.Contains(out, "outside-sentinel") || strings.Contains(diagnostics, "outside-sentinel") {
			t.Fatalf("followed a swapped directory symlink: %s\n%s", out, diagnostics)
		}
		if !strings.Contains(out, "good") {
			t.Fatalf("report lost stable run: %s", out)
		}
	}
}

func reportWithoutTelemetryRecords(t *testing.T, root, id, body string) string {
	t.Helper()
	dir := filepath.Join(root, id)
	data, err := json.Marshal(loop.Config{RunID: id, Worktree: "/scratch/app", MaxRounds: 3})
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "config.json"), string(data))
	writeFile(t, filepath.Join(dir, "telemetry.jsonl"), body)
	return dir
}

func TestReportCountsARunWithEmptyTelemetryAsUnfinishedWithNothingOnStderr(t *testing.T) {
	root := recordRoot(t)
	reportWithoutTelemetryRecords(t, root, "first-step", "")
	out, diagnostics := captureReport(t, "-json", root)
	var report runReport
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatal(err)
	}
	if diagnostics != "" || len(report.Runs) != 1 || report.Totals.Outcomes["unfinished"] != 1 {
		t.Fatalf("report = %s, diagnostics = %s", out, diagnostics)
	}
	run := report.Runs[0]
	if run.Started != nil || run.Repository != "" || run.Ensemble != "" || run.Rounds != nil || run.DurationMS != nil {
		t.Fatalf("run = %+v", run)
	}
}

func TestReportCountsARunWhoseTelemetryHoldsOnlyBlankLinesAsUnfinishedWithNothingOnStderr(t *testing.T) {
	root := recordRoot(t)
	reportWithoutTelemetryRecords(t, root, "first-step", "\n\n  \n\t\r\n \t")
	out, diagnostics := captureReport(t, root)
	if diagnostics != "" || !strings.Contains(strings.Join(strings.Fields(out), " "), "unknown unknown unknown unfinished unknown unknown") {
		t.Fatalf("report = %s, diagnostics = %s", out, diagnostics)
	}
}

func TestReportTakesTheStartOfAnUnfinishedRunWithoutTelemetryFromItsRunID(t *testing.T) {
	for _, tc := range []struct {
		id      string
		started any
	}{
		{"20261110-143005-a1b2c3", "2026-11-10T14:30:05Z"},
		{"first-step", nil},
		{"20261310-250000-aaaaaa", nil},
		{"20261110-143005", nil},
		{"20261110-143005-", nil},
	} {
		t.Run(tc.id, func(t *testing.T) {
			root := recordRoot(t)
			reportWithoutTelemetryRecords(t, root, tc.id, "")
			row := reportRows(t, root)[0].(map[string]any)
			if row["started"] != tc.started {
				t.Fatalf("run = %v, want start %v", row, tc.started)
			}
			if tc.started != nil {
				out, diagnostics := captureReport(t, root, "-since", "2026-11-10")
				if diagnostics != "" || !strings.Contains(out, "2026-11-10 14:30:05") || !strings.Contains(out, "runs: 1") {
					t.Fatalf("report = %s, diagnostics = %s", out, diagnostics)
				}
				if rows := reportRows(t, root, "-since", "2026-11-10T14:30:06Z"); len(rows) != 0 {
					t.Fatal(rows)
				}
			}
		})
	}
}

func TestReportReadsTheRunIDFromConfigJSONOfARunWithoutTelemetryRecords(t *testing.T) {
	root := recordRoot(t)
	dir := reportWithoutTelemetryRecords(t, root, "kept-copy", "")
	data, err := json.Marshal(loop.Config{RunID: "20261110-143005-a1b2c3", Worktree: "/scratch/app", Chairs: loop.Chairs{}})
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "config.json"), string(data))
	row := reportRows(t, root)[0].(map[string]any)
	if row["run_id"] != "20261110-143005-a1b2c3" || row["started"] != "2026-11-10T14:30:05Z" || row["repository"] != nil || row["ensemble"] != nil {
		t.Fatal(row)
	}
}

func TestReportDiagnosesAMalformedConfigJSONButStillCountsTheRunAsUnfinished(t *testing.T) {
	for _, body := range []string{"{", strings.Repeat(" ", (1<<20)+1)} {
		t.Run(fmt.Sprint(len(body)), func(t *testing.T) {
			root := recordRoot(t)
			dir := reportWithoutTelemetryRecords(t, root, "20261110-143005-a1b2c3", "")
			writeFile(t, filepath.Join(dir, "config.json"), body)
			out, diagnostics := captureReport(t, root)
			if !strings.Contains(diagnostics, filepath.Join(dir, "config.json")) || !strings.Contains(out, "unfinished") || !strings.Contains(out, "2026-11-10 14:30:05") {
				t.Fatalf("report = %s, diagnostics = %s", out, diagnostics)
			}
		})
	}
}

func TestReportPrefersCurrentJSONOverTheRunIDForTheStartOfARunWithEmptyTelemetry(t *testing.T) {
	root := recordRoot(t)
	id := "20261110-143005-a1b2c3"
	dir := reportWithoutTelemetryRecords(t, root, id, "")
	s := statusFixture(t, root, id)
	writeFile(t, filepath.Join(dir, "config.json"), `{"RunID":"20261109-120000-aaaaaa"}`)
	out, diagnostics := captureReport(t, "-json", root)
	var report runReport
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatal(err)
	}
	if diagnostics != "" || len(report.Runs) != 1 {
		t.Fatalf("report = %s, diagnostics = %s", out, diagnostics)
	}
	run := report.Runs[0]
	if run.RunID != id || run.Started == nil || !run.Started.Equal(s.Started) || run.Repository != s.Repository || run.Ensemble != s.Ensemble {
		t.Fatalf("run = %+v", run)
	}
}

func TestReportUsesTheValidLinesOfTelemetryWhoseLastLineWasCutOffMidWrite(t *testing.T) {
	root := recordRoot(t)
	dir := filepath.Join(root, "unfinished")
	valid := "{\"at\":\"2026-11-09T14:00:00Z\"}\n{\"at\":\"2026-11-09T14:00:01Z\"}\n"
	writeFile(t, filepath.Join(dir, "telemetry.jsonl"), valid)
	before, diagnostics := captureReport(t, "-json", root)
	if diagnostics != "" {
		t.Fatal(diagnostics)
	}
	writeFile(t, filepath.Join(dir, "telemetry.jsonl"), valid+`{"at":"2026-11-0`)
	after, diagnostics := captureReport(t, "-json", root)
	if after != before || diagnostics != "" {
		t.Fatalf("report = %s, want %s, diagnostics = %s", after, before, diagnostics)
	}
	row := reportRows(t, root)[0].(map[string]any)
	if row["outcome"] != "unfinished" || row["started"] != "2026-11-09T14:00:00Z" {
		t.Fatal(row)
	}
}

func TestReportSkipsBlankLinesBeforeTheFirstTelemetryRecord(t *testing.T) {
	root := recordRoot(t)
	writeFile(t, filepath.Join(root, "first", "telemetry.jsonl"), "\n \t\r\n{\"at\":\"2026-11-09T14:00:00Z\"}\n")
	out, diagnostics := captureReport(t, root)
	if diagnostics != "" || !strings.Contains(strings.Join(strings.Fields(out), " "), "2026-11-09 14:00:00 unknown unknown unfinished") {
		t.Fatalf("report = %s, diagnostics = %s", out, diagnostics)
	}
}

func TestReportStillCountsTelemetryWithRealUnparseableContentAsUnreadableAndNamesItsPathOnStderr(t *testing.T) {
	for _, body := range []string{`{"at":`, "not JSON\n", "\n\nnot JSON", "{}"} {
		t.Run(body, func(t *testing.T) {
			root := recordRoot(t)
			dir := reportWithoutTelemetryRecords(t, root, "20261110-143005-a1b2c3", body)
			out, diagnostics := captureReport(t, "-json", root)
			var report runReport
			if err := json.Unmarshal([]byte(out), &report); err != nil {
				t.Fatal(err)
			}
			if len(report.Runs) != 1 || report.Totals.Outcomes["unreadable"] != 1 || !strings.Contains(diagnostics, filepath.Join(dir, "telemetry.jsonl")) {
				t.Fatalf("report = %s, diagnostics = %s", out, diagnostics)
			}
			run := report.Runs[0]
			if run.Started != nil || run.Repository != "" || run.Ensemble != "" || run.Rounds != nil || run.DurationMS != nil {
				t.Fatalf("run = %+v", run)
			}
		})
	}
}

func TestReportKeepsAFinishedResultAndSaysNothingWhenItsTelemetryIsEmpty(t *testing.T) {
	for _, id := range []string{"finished", "20261110-143005-a1b2c3"} {
		t.Run(id, func(t *testing.T) {
			root := recordRoot(t)
			dir := reportFixture(t, root, id, "", "app", "thorough", "approved")
			writeFile(t, filepath.Join(dir, "telemetry.jsonl"), "")
			writeFile(t, filepath.Join(dir, "config.json"), `{"RunID":"other"}`)
			out, diagnostics := captureReport(t, "-json", root)
			if diagnostics != "" {
				t.Fatal(diagnostics)
			}
			var report runReport
			if err := json.Unmarshal([]byte(out), &report); err != nil {
				t.Fatal(err)
			}
			run := report.Runs[0]
			if run.Outcome != "approved" || run.RunID != id || run.Repository != "app" || run.Ensemble != "thorough" {
				t.Fatalf("run = %+v", run)
			}
			if id == "finished" && run.Started != nil || id != "finished" && (run.Started == nil || run.Started.Format(time.RFC3339) != "2026-11-10T14:30:05Z") {
				t.Fatalf("start = %v", run.Started)
			}
		})
	}
}

func TestReportLimitsTheBytesConsumedBeforeTheFirstNonBlankTelemetryLine(t *testing.T) {
	for _, body := range []string{
		strings.Repeat("\n", (1<<20)+1),
		strings.Repeat("\n", 1<<20) + "{\"at\":\"2026-11-09T14:00:00Z\"}\n",
	} {
		t.Run(fmt.Sprint(len(body)), func(t *testing.T) {
			root := recordRoot(t)
			dir := reportWithoutTelemetryRecords(t, root, "first", body)
			out, diagnostics := captureReport(t, root)
			if !strings.Contains(out, "unreadable") || !strings.Contains(diagnostics, filepath.Join(dir, "telemetry.jsonl")) || !strings.Contains(diagnostics, "record exceeds 1 MiB") {
				t.Fatalf("report = %s, diagnostics = %s", out, diagnostics)
			}
		})
	}
}

func TestReportPrefersAnEmptyTelemetryCopyOverAnUnreadableCopyAndAFinishedCopyOverBoth(t *testing.T) {
	a, b, c := recordRoot(t), recordRoot(t), recordRoot(t)
	id := "20261110-143005-a1b2c3"
	writeFile(t, filepath.Join(a, id, "telemetry.jsonl"), "{")
	reportWithoutTelemetryRecords(t, b, id, "")
	reportFixture(t, c, id, "", "app", "thorough", "approved")
	for _, dirs := range [][]string{{a, b}, {b, a}, {a, b, c}, {c, b, a}} {
		rows := reportRows(t, dirs...)
		want := "unfinished"
		if len(dirs) == 3 {
			want = "approved"
		}
		if len(rows) != 1 || rows[0].(map[string]any)["outcome"] != want {
			t.Fatal(rows)
		}
	}
}

func recordRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return root
}
