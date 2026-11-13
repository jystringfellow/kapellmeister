package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jystringfellow/kapellmeister/internal/loop"
)

func TestWaitTimesOutWithItsOwnStatusAndLeavesTheRunAlone(t *testing.T) {
	root := t.TempDir()
	statusFixture(t, root, "alive")
	path := filepath.Join(root, "alive", "current.json")
	before, _ := os.ReadFile(path)
	out, diag, code := waitSubprocess(t, "alive", "-timeout", "100ms", root)
	after, _ := os.ReadFile(path)
	if code != 3 || out != "" || !strings.Contains(diag, "timed out") || !bytes.Equal(before, after) {
		t.Fatalf("%d %s %s", code, out, diag)
	}
	out, diag, code = waitSubprocess(t, "alive", "-json", "-timeout", "10ms", root)
	if code != 3 || diag != "" || !strings.Contains(out, `"state":"timeout"`) {
		t.Fatalf("%d %s %s", code, out, diag)
	}
}

func TestWaitReportsARunWhoseProcessIsGoneOrUnknownAsUnfinishedNeverApproved(t *testing.T) {
	for _, kind := range []string{"gone", "unknown", "old"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			s := statusFixture(t, root, kind)
			if kind == "gone" {
				s.ProcessStart += "different"
			} else {
				s.Host += "elsewhere"
			}
			saveStatusFixture(t, root, kind, s)
			if kind == "old" {
				os.Remove(filepath.Join(root, kind, "current.json"))
				writeFile(t, filepath.Join(root, kind, "telemetry.jsonl"), "{\"at\":\"2026-11-09T14:00:00Z\"}\n")
			}
			out, diag, code := waitSubprocess(t, kind, root)
			if code != 4 || out != "" || !strings.Contains(diag, "unfinished") {
				t.Fatalf("%d %s %s", code, out, diag)
			}
			out, diag, code = waitSubprocess(t, kind, "-json", root)
			if code != 4 || diag != "" || !strings.Contains(out, `"state":"unfinished"`) {
				t.Fatalf("%d %s %s", code, out, diag)
			}
		})
	}
}

func TestWaitAcceptsAUniquePrefixAndRefusesAnAmbiguousOrUnknownRun(t *testing.T) {
	root := t.TempDir()
	reportFixture(t, root, "same-one", "", "app", "quick", "approved")
	reportFixture(t, root, "same-two", "", "app", "quick", "failed")
	for _, id := range []string{"same-o", "same-one"} {
		out, diag, code := waitSubprocess(t, id, "-json", root)
		if code != 0 || diag != "" || !strings.Contains(out, `"run_id":"same-one"`) || !strings.Contains(out, `"state":"finished"`) {
			t.Fatalf("%d %s %s", code, out, diag)
		}
	}
	for _, id := range []string{"same", "absent"} {
		_, diag, code := waitSubprocess(t, id, root)
		if code != 1 || !strings.Contains(diag, id) {
			t.Fatalf("%d %s", code, diag)
		}
	}
}

func TestWaitFindsRunsWhereStatusFindsThemAndReadsOldResults(t *testing.T) {
	home, root := t.TempDir(), t.TempDir()
	t.Setenv(loop.HomeEnv, home)
	reportFixture(t, root, "configured", "", "app", "quick", "approved")
	writeFile(t, loop.UserConfigPath(home), "[report]\ndirs = ['"+root+"']\n")
	out, diag, code := waitSubprocess(t, "configured")
	if code != 0 || diag != "" || !strings.Contains(out, "approved after") {
		t.Fatalf("%d %s %s", code, out, diag)
	}
}

func TestWaitRetriesAResultThatIsStillBeingWritten(t *testing.T) {
	root := t.TempDir()
	s := statusFixture(t, root, "partial")
	s.ProcessStart += "gone"
	saveStatusFixture(t, root, "partial", s)
	path := filepath.Join(root, "partial", "result.json")
	writeFile(t, path, `{"run_id":`)
	done := make(chan error, 1)
	go func() {
		time.Sleep(150 * time.Millisecond)
		done <- os.WriteFile(path, []byte(`{"run_id":"partial","outcome":"approved","rounds":1}`), 0600)
	}()
	out, diag, code := waitSubprocess(t, "partial", root)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if code != 0 || diag != "" || !strings.Contains(out, "approved after") {
		t.Fatalf("%d %s %s", code, out, diag)
	}
}

func TestWaitDoesNotWaitForeverForAnInvalidResult(t *testing.T) {
	root := t.TempDir()
	statusFixture(t, root, "broken")
	writeFile(t, filepath.Join(root, "broken", "result.json"), `null`)
	_, diag, code := waitSubprocess(t, "broken", root)
	if code != 1 || !strings.Contains(diag, "result") {
		t.Fatalf("%d %s", code, diag)
	}
}

func TestAResultIsWrittenAtomicallyWithoutLeavingTemporaryFiles(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "result.json")
	stop := make(chan struct{})
	bad := make(chan string, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
			}
			data, err := os.ReadFile(path)
			if err == nil && !json.Valid(data) {
				select {
				case bad <- string(data):
				default:
				}
				return
			}
		}
	}()
	for i := 0; i < 100; i++ {
		if err := writeJSON(path, map[string]string{"data": strings.Repeat("a", 10000)}); err != nil {
			t.Fatal(err)
		}
	}
	close(stop)
	<-done
	select {
	case data := <-bad:
		t.Fatalf("partial result: %q", data)
	default:
	}
	files, _ := os.ReadDir(root)
	if len(files) != 1 || files[0].Name() != "result.json" {
		t.Fatal(files)
	}
}

func TestWaitRefusesToFollowALinkToAResult(t *testing.T) {
	root := t.TempDir()
	statusFixture(t, root, "linked")
	outside := filepath.Join(t.TempDir(), "result.json")
	writeFile(t, outside, `{"run_id":"linked","outcome":"approved"}`)
	if err := os.Symlink(outside, filepath.Join(root, "linked", "result.json")); err != nil {
		t.Fatal(err)
	}
	out, diag, code := waitSubprocess(t, "linked", root)
	if code != 1 || strings.Contains(out, "approved") || !strings.Contains(diag, "result") {
		t.Fatalf("%d %s %s", code, out, diag)
	}
}

func TestWaitRefusesConflictingResultsForTheSameRunId(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	reportFixture(t, a, "same", "", "app", "quick", "approved")
	reportFixture(t, b, "same", "", "app", "quick", "failed")
	out, diag, code := waitSubprocess(t, "same", a, b)
	if code != 1 || strings.Contains(out, "approved") || !strings.Contains(diag, "conflict") {
		t.Fatalf("%d %s %s", code, out, diag)
	}
}

func TestWaitReportsAnExitedProcessWithNoResultAsUnfinished(t *testing.T) {
	root := t.TempDir()
	s := statusFixture(t, root, "exited")
	child := statusChild(t)
	s.PID = child.Process.Pid
	var err error
	s.ProcessStart, err = processStart(s.PID)
	if err != nil {
		t.Fatal(err)
	}
	saveStatusFixture(t, root, "exited", s)
	if err := child.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = child.Wait()
	out, diag, code := waitSubprocess(t, "exited", root)
	if code != 4 || out != "" || !strings.Contains(diag, "process is gone") {
		t.Fatalf("%d %s %s", code, out, diag)
	}
}

func TestWaitRefusesAnEmptyRunInsteadOfReportingAnotherRunsResult(t *testing.T) {
	for _, state := range []string{"approved", "failed", "alive", "no records"} {
		t.Run(state, func(t *testing.T) {
			root := t.TempDir()
			switch state {
			case "alive":
				statusFixture(t, root, "20261113-041100-efb4a2")
			case "approved", "failed":
				reportFixture(t, root, "20261113-041100-efb4a2", "", "app", "quick", state)
			}
			for _, id := range []string{"", " ", "\t\n"} {
				for _, asJSON := range []bool{false, true} {
					args := []string{id, "-timeout", "50ms", root}
					if asJSON {
						args = append(args, "-json")
					}
					out, diag, code := waitSubprocess(t, args...)
					if code != 1 || out != "" || !strings.Contains(diag, "RUN must not be empty") {
						t.Errorf("RUN=%q json=%t: status=%d stdout=%q stderr=%q", id, asJSON, code, out, diag)
					}
				}
			}
		})
	}
}

func TestWaitRefusesAOneCharacterPrefixEvenWhenOnlyOneRunMatches(t *testing.T) {
	for _, id := range []string{"2", "é"} {
		t.Run(id, func(t *testing.T) {
			root := t.TempDir()
			reportFixture(t, root, id+"0261113-041100-efb4a2", "", "app", "quick", "approved")
			for _, asJSON := range []bool{false, true} {
				args := []string{id, root}
				if asJSON {
					args = append(args, "-json")
				}
				out, diag, code := waitSubprocess(t, args...)
				if code != 1 || out != "" || !strings.Contains(diag, "prefix must be at least 2 characters") {
					t.Errorf("RUN=%q json=%t: status=%d stdout=%q stderr=%q", id, asJSON, code, out, diag)
				}
			}
		})
	}
}

func TestWaitStillAcceptsTwoCharacterPrefixesAndExactOneCharacterRunIds(t *testing.T) {
	root := t.TempDir()
	reportFixture(t, root, "20261113-041100-efb4a2", "", "app", "quick", "approved")
	reportFixture(t, root, "2", "", "app", "quick", "failed")
	for _, tc := range []struct {
		id, outcome string
		code        int
	}{{"20", "approved", 0}, {"2", "failed", 1}} {
		out, diag, code := waitSubprocess(t, tc.id, "-json", root)
		var doc waitResult
		if err := json.Unmarshal([]byte(out), &doc); err != nil || code != tc.code || diag != "" || doc.Result == nil || doc.Result.Outcome != tc.outcome {
			t.Errorf("RUN=%q: status=%d stdout=%q stderr=%q", tc.id, code, out, diag)
		}
	}
}

func TestWaitFindsARunInTheConfiguredDirectoriesThatExistWhenAnotherDoesNot(t *testing.T) {
	isolateReportConfig(t)
	root, missing := t.TempDir(), filepath.Join(t.TempDir(), "absent")
	reportFixture(t, root, "configured", "", "app", "quick", "approved")
	for _, skip := range []bool{true, false} {
		setReportDirs(t, root)
		wantDiag := ""
		if skip {
			setReportDirs(t, missing, root)
			wantDiag = "km: wait: " + missing + ": not searched: directory does not exist\n"
		}
		for _, flags := range [][]string{nil, {"-json"}} {
			out, diag, code := waitSubprocess(t, append([]string{"configured"}, flags...)...)
			if code != 0 || diag != wantDiag {
				t.Fatalf("exit = %d, stdout = %s, stderr = %s", code, out, diag)
			}
			if len(flags) == 0 {
				if !strings.Contains(out, "approved after") {
					t.Fatal(out)
				}
				continue
			}
			var doc map[string]any
			if err := json.Unmarshal([]byte(out), &doc); err != nil {
				t.Fatal(err)
			}
			if skip {
				dirs, ok := doc["skipped_dirs"].([]any)
				if !ok || len(dirs) != 1 || dirs[0] != missing {
					t.Fatal(doc)
				}
			} else if _, exists := doc["skipped_dirs"]; exists {
				t.Fatal(doc)
			}
		}
	}
}

func TestWaitNamesSkippedDirectoriesEvenWhenTheRunIsUnknown(t *testing.T) {
	isolateReportConfig(t)
	missing := filepath.Join(t.TempDir(), "absent")
	for _, dirs := range [][]string{{missing}, {missing, t.TempDir()}} {
		setReportDirs(t, dirs...)
		out, diag, code := waitSubprocess(t, "unknown")
		want := "km: wait: " + missing + ": not searched: directory does not exist\nkm: wait: unknown run \"unknown\"\n"
		if code != 1 || out != "" || diag != want {
			t.Fatalf("exit = %d, stdout = %s, stderr = %s", code, out, diag)
		}
	}
}

func TestWaitFindsARunInTheStateRunsWhenReportDirsNamesAnotherDirectory(t *testing.T) {
	isolateReportConfig(t)
	stateRuns, kept := filepath.Join(loop.StateDir(), "runs"), t.TempDir()
	reportFixture(t, stateRuns, "state", "", "app", "quick", "approved")
	reportFixture(t, kept, "kept", "", "app", "quick", "approved")
	setReportDirs(t, kept)
	for _, id := range []string{"state", "kept"} {
		out, diag, code := waitSubprocess(t, id, "-json")
		var doc waitResult
		if err := json.Unmarshal([]byte(out), &doc); err != nil || code != 0 || diag != "" || doc.RunID != id || doc.State != "finished" {
			t.Fatalf("exit = %d, stdout = %s, stderr = %s", code, out, diag)
		}
	}
}

func TestWaitSilentlySkipsAMissingStateRunsEvenWhenItIsListed(t *testing.T) {
	isolateReportConfig(t)
	stateRuns, kept := filepath.Join(loop.StateDir(), "runs"), t.TempDir()
	reportFixture(t, kept, "kept", "", "app", "quick", "approved")
	for _, dirs := range [][]string{nil, {kept}, {stateRuns, kept}} {
		setReportDirs(t, dirs...)
		out, diag, code := waitSubprocess(t, "kept", "-json")
		if len(dirs) == 0 {
			if code != 1 || diag != "km: wait: unknown run \"kept\"\n" {
				t.Fatalf("exit = %d, stdout = %s, stderr = %s", code, out, diag)
			}
			continue
		}
		var doc waitResult
		if err := json.Unmarshal([]byte(out), &doc); err != nil || code != 0 || diag != "" || len(doc.SkippedDirs) != 0 || doc.RunID != "kept" {
			t.Fatalf("exit = %d, stdout = %s, stderr = %s", code, out, diag)
		}
	}
}

func TestWaitCommandLineDirectoriesReplaceTheDefaultAndConfigWithoutReadingIt(t *testing.T) {
	isolateReportConfig(t)
	stateRuns, kept, named := filepath.Join(loop.StateDir(), "runs"), t.TempDir(), t.TempDir()
	reportFixture(t, stateRuns, "state", "", "app", "quick", "approved")
	reportFixture(t, kept, "kept", "", "app", "quick", "approved")
	reportFixture(t, named, "named", "", "app", "quick", "approved")
	for _, malformed := range []bool{false, true} {
		setReportDirs(t, kept)
		if malformed {
			writeFile(t, loop.UserConfigPath(loop.Home()), "[broken")
		}
		for _, id := range []string{"named", "state", "kept"} {
			out, diag, code := waitSubprocess(t, id, named)
			if id == "named" {
				if code != 0 || diag != "" || !strings.Contains(out, "approved after") {
					t.Fatalf("exit = %d, stdout = %s, stderr = %s", code, out, diag)
				}
			} else if code != 1 || !strings.Contains(diag, "unknown run") || strings.Contains(diag, "config") {
				t.Fatalf("exit = %d, stdout = %s, stderr = %s", code, out, diag)
			}
		}
	}
}
