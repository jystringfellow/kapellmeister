package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/jystringfellow/kapellmeister/internal/loop"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func TestEnsembleFileDoesNotGoWithScoreOrChairFlags(t *testing.T) {
	for _, tc := range []struct{ flag, want string }{{"-score=default", "-score does not go with -ensemble-file"}, {"-planner=codex", "-planner does not go with -ensemble-file"}} {
		t.Run(tc.flag, func(t *testing.T) {
			var out, diag bytes.Buffer
			err := runAt(nil, []string{"-ensemble-file=sample.toml", tc.flag}, &out, &diag, nil)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error: %v, diagnostics: %s", err, diag.String())
			}
		})
	}
}

func TestAWhyThatIsBlankOrOver1024BytesOrNotUTF8IsRefusedBeforeAnyWork(t *testing.T) {
	for _, why := range []string{"", " ", strings.Repeat("a", 1025), string([]byte{0xff})} {
		var out, diag bytes.Buffer
		err := runAt(nil, []string{"-why=" + why}, &out, &diag, nil)
		if err == nil || !strings.Contains(err.Error(), "-why must be") {
			t.Fatalf("error: %v, diagnostics: %s", err, diag.String())
		}
	}
}

func TestARunPlaysAndRecordsAnEnsembleGivenAsAFile(t *testing.T) {
	f := &runCAO{}
	_, args := runSessionArgs(t, f)
	path := filepath.Join(t.TempDir(), "custom.toml")
	writeFile(t, path, recordedEnsemble)
	if err := runCommand(append(args[:6:6], "-ensemble-file", path, "-why", "fits")); err != nil {
		t.Fatal(err)
	}
	dir := onlyRunRecord(t)
	data, err := os.ReadFile(filepath.Join(dir, "ensemble.toml"))
	if err != nil || string(data) != recordedEnsemble {
		t.Fatalf("copy: %q, %v", data, err)
	}
	result := recordedResult(t, dir)
	if result["ensemble"] != "custom" || result["ensemble_source"] != "text" || result["why"] != "fits" {
		t.Fatal(result)
	}
}

func TestStatusMarksATextEnsembleAndShowsWhy(t *testing.T) {
	var out bytes.Buffer
	err := printStatus(&out, []statusRun{{RunID: "one", Ensemble: statusSeating{Name: "sample"}, EnsembleSource: "text", Why: "fits"}}, time.Now())
	if err != nil || !strings.Contains(out.String(), "sample (from text)") || !strings.Contains(out.String(), "why one: fits") {
		t.Fatalf("%s %v", out.String(), err)
	}
}

func TestAnEnsembleFileNamedDashDoesNotReadStandardInput(t *testing.T) {
	_, err := readTextFile("ensemble", "-", "-", nil)
	if err == nil {
		t.Fatal("expected a file error")
	}
}

func TestRecordedSeatingKeepsItsExplicitSourceWhenAFileAppears(t *testing.T) {
	t.Setenv(loop.HomeEnv, t.TempDir())
	writeFile(t, filepath.Join(loop.Home(), "ensembles", "sample.toml"), recordedEnsemble)
	for _, source := range []string{"text", "config"} {
		got := recordSeating(ensembleRun{Ensemble: "sample", EnsembleSource: source}, loop.DefaultScore())
		if got.Source != source {
			t.Errorf("source = %q, want %q", got.Source, source)
		}
	}
}

func TestATextEnsembleLeavesNoRecordWhenDiskSpaceIsInsufficient(t *testing.T) {
	f := &runCAO{}
	_, args := runSessionArgs(t, f)
	path := filepath.Join(t.TempDir(), "sample.toml")
	writeFile(t, path, recordedEnsemble)
	original := loop.FreeBytes
	loop.FreeBytes = func(string) (uint64, error) { return 10 << 20, nil }
	t.Cleanup(func() { loop.FreeBytes = original })
	_, err := captureStdout(t, func() error { return runCommand(append(args[:6:6], "-ensemble-file", path)) })
	if err == nil || !strings.Contains(err.Error(), "low disk") {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(loop.StateDir(), "runs"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("orphan run records: %v", entries)
	}
}

func TestADetachedTextEnsembleUsesItsRecordedScoreAndWorktree(t *testing.T) {
	s, _, _, wt := smallRepositoryScore(t)
	runID := "20260101-000000-abcdef"
	dir := filepath.Join(loop.StateDir(), "runs", runID)
	writeFile(t, filepath.Join(dir, "ensemble.toml"), smallScoreEnsemble)
	launch := detachedLaunchRecord{RunID: runID, Worktree: wt, Task: "probe", Score: &s.Score}
	if err := writeJSON(filepath.Join(dir, "launch.json"), launch); err != nil {
		t.Fatal(err)
	}
	f := &runCAO{createFails: true}
	srv := f.serve(t)
	var out, diag bytes.Buffer
	err := runAt(nil, []string{"-repo=" + t.TempDir(), "-ensemble-file=gone.toml", "-ensemble=custom", "-detached-launch=" + filepath.Join(dir, "launch.json"), "-cao=" + srv.URL}, &out, &diag, nil)
	if err == nil || !strings.Contains(err.Error(), "listing CAO profiles") {
		t.Fatalf("error %v output %s diagnostics %s", err, out.String(), diag.String())
	}
}

func TestARepositoryEnsembleOnTheSelectedBaseRecordsItsSource(t *testing.T) {
	f := runBaseFixture(t, false)
	seed := filepath.Join(t.TempDir(), "seed")
	safetyGit(t, f.repo, "clone", "-q", "-b", "stack", f.origin, seed)
	safetyGit(t, seed, "config", "user.name", "Test")
	safetyGit(t, seed, "config", "user.email", "test@example.invalid")
	writeFile(t, loop.UserConfigPath(loop.Home()), `[agents]
available = ["mock"]
[agents.define]
mock = "p:planner"
`)
	portable := regexp.MustCompile(`p:[a-z]+:m`).ReplaceAllString(recordedEnsemble, "mock")
	writeFile(t, filepath.Join(seed, ".kapellmeister/ensembles/custom.toml"), portable)
	safetyGit(t, seed, "add", "-f", ".kapellmeister/ensembles/custom.toml")
	safetyGit(t, seed, "-c", "commit.gpgsign=false", "commit", "-qm", "fixture")
	safetyGit(t, f.origin, "fetch", "-q", seed, "HEAD")
	safetyGit(t, f.origin, "update-ref", "refs/heads/stack", safetyGit(t, seed, "rev-parse", "HEAD"))
	_, err := captureStdout(t, func() error { return runCommand(append(f.args[:6:6], "-base", "stack", "-ensemble", "custom")) })
	if err != nil {
		t.Fatal(err)
	}
	result := recordedResult(t, onlyRunRecord(t))
	if result["ensemble_source"] != "repository" {
		t.Fatal(result)
	}
}

func TestADetachedRunPlaysItsRecordedEnsembleAfterTheFileChangesOrDisappears(t *testing.T) {
	for _, change := range []string{"rewrite", "remove"} {
		t.Run(change, func(t *testing.T) {
			hold := make(chan struct{})
			f := &runCAO{hold: hold}
			_, args := runSessionArgs(t, f)
			path := filepath.Join(t.TempDir(), "original.toml")
			content := strings.ReplaceAll(recordedEnsemble, "\n", "\r\n") + "  \r\n"
			writeFile(t, path, content)
			h, _ := launchDetached(t, append(args[:6:6], "-ensemble-file", path, "-ensemble", "chosen", "-why", "fits"))
			dir := filepath.Dir(h["output"])
			data, err := os.ReadFile(filepath.Join(dir, "ensemble.toml"))
			if err != nil || string(data) != content {
				t.Fatalf("copy %q: %v", data, err)
			}
			eventually(t, func() bool {
				state := readCurrent(t, dir)
				return len(state.Playing) > 0 && state.Playing[0].Step != "preflight"
			})
			state := readCurrent(t, dir)
			if state.Ensemble != "chosen" || state.EnsembleSource != "text" || state.Why != "fits" {
				t.Fatal(state)
			}
			if change == "rewrite" {
				writeFile(t, path, strings.ReplaceAll(recordedEnsemble, "security", "changed"))
			} else if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			close(hold)
			sum := readDetachedSummary(t, h)
			if sum.Ensemble != "chosen" || sum.EnsembleSource != "text" || sum.Why != "fits" || sum.Outcome != loop.Approved {
				t.Fatal(sum)
			}
			want := map[string][]string{}
			for step, addresses := range recordedAddresses {
				for _, address := range addresses {
					want[step] = append(want[step], strings.ReplaceAll(address, "@thorough", "@chosen"))
				}
			}
			requireRecordedAddresses(t, dir, want, 6)
		})
	}
}

func TestInvalidTextEnsemblesAreRefusedBeforeAnyWork(t *testing.T) {
	cases := []struct{ name, want string }{
		{"load", "unknown"}, {"fit", "planner"}, {"not-ready", "not ready here"}, {"reserved", "reserved"},
		{"user", "choose another"}, {"repository", "choose another"}, {"config", "choose another"},
		{"score", "-score does not go with -ensemble-file"}, {"chair", "-planner does not go with -ensemble-file"}, {"pipe", "named pipe is not a regular file"},
	}
	for _, tc := range cases {
		for _, detach := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/detach=%t", tc.name, detach), func(t *testing.T) {
				standing := playStanding(t)
				wt := standing.Worktree
				var calls atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
				defer server.Close()
				path := filepath.Join(t.TempDir(), "custom.toml")
				content := recordedEnsemble
				name := "custom"
				var extra []string
				switch tc.name {
				case "load":
					content = "unknown = true\n"
				case "fit":
					content = strings.Replace(recordedEnsemble, `part = "gate"`, `part = "planner"`, 1)
				case "not-ready":
					content = sectionedEnsemble
					writeFile(t, loop.UserConfigPath(loop.Home()), "[agents]\navailable = ['claude']\n")
				case "reserved":
					name = "podium"
				case "user":
					writeFile(t, filepath.Join(loop.Home(), "ensembles", "custom.toml"), recordedEnsemble)
				case "repository":
					writeFile(t, filepath.Join(wt, ".kapellmeister/ensembles/custom.toml"), recordedEnsemble)
				case "config":
					writeFile(t, loop.UserConfigPath(loop.Home()), "[ensembles.custom]\ngate = 'claude'\n")
				case "score":
					extra = append(extra, "-score=default")
				case "chair":
					extra = append(extra, "-planner=p:x")
				}
				if tc.name == "pipe" {
					if err := syscall.Mkfifo(path, 0600); err != nil {
						t.Fatal(err)
					}
				} else {
					writeFile(t, path, content)
				}
				before := safetyGit(t, wt, "worktree", "list", "--porcelain")
				args := []string{"-repo=" + wt, "-task=probe", "-cao=" + server.URL, "-ensemble-file=" + path, "-ensemble=" + name}
				if detach {
					args = append(args, "-detach")
				}
				args = append(args, extra...)
				var out, diag bytes.Buffer
				done := make(chan error, 1)
				go func() { done <- runAt(context.Background(), args, &out, &diag, nil) }()
				select {
				case err := <-done:
					if err == nil || !strings.Contains(err.Error(), tc.want) || ((tc.name == "load" || tc.name == "pipe") && !strings.Contains(err.Error(), path)) {
						t.Fatalf("err=%v out=%s diag=%s", err, out.String(), diag.String())
					}
				case <-time.After(2 * time.Second):
					t.Fatal("run blocked")
				}
				if calls.Load() != 0 || before != safetyGit(t, wt, "worktree", "list", "--porcelain") {
					t.Fatalf("requests=%d or worktrees changed", calls.Load())
				}
				entries, err := os.ReadDir(filepath.Join(loop.StateDir(), "runs"))
				if err != nil && !os.IsNotExist(err) {
					t.Fatal(err)
				}
				if len(entries) != 0 {
					t.Fatalf("records %v", entries)
				}
			})
		}
	}
}

func TestEachEnsembleSourceAndWhyAppearInTheRecordStatusAndReport(t *testing.T) {
	for _, source := range []string{"text", "user", "repository", "config", "chairs"} {
		t.Run(source, func(t *testing.T) {
			hold := make(chan struct{})
			f := &runCAO{hold: hold}
			wt, allArgs := runSessionArgs(t, f)
			args := append([]string{}, allArgs[:6]...)
			why := "chosen\n\x1b[31mfit"
			args = append(args, "-why", why)
			switch source {
			case "text":
				path := filepath.Join(t.TempDir(), "original.toml")
				writeFile(t, path, recordedEnsemble)
				args = append(args, "-ensemble-file", path, "-ensemble", "chosen")
			case "user":
				writeFile(t, filepath.Join(loop.Home(), "ensembles/chosen.toml"), recordedEnsemble)
				args = append(args, "-ensemble", "chosen")
			case "repository":
				writeFile(t, loop.UserConfigPath(loop.Home()), "[agents]\navailable = ['mock']\n[agents.define]\nmock = 'p:planner'\n")
				portable := regexp.MustCompile(`p:[a-z]+:m`).ReplaceAllString(recordedEnsemble, "mock")
				writeFile(t, filepath.Join(wt, ".kapellmeister/ensembles/chosen.toml"), portable)
				args = append(args, "-ensemble", "chosen")
			case "config":
				var config strings.Builder
				config.WriteString("[ensembles.chosen]\n")
				roles := runRoleArgs()
				for i := 0; i < len(roles); i += 2 {
					fmt.Fprintf(&config, "%s = %q\n", strings.TrimPrefix(roles[i], "-"), roles[i+1])
				}
				writeFile(t, loop.UserConfigPath(loop.Home()), config.String())
				args = append(args, "-ensemble", "chosen")
			case "chairs":
				args = append(args, allArgs[6:]...)
			}
			h, _ := launchDetached(t, args)
			dir := filepath.Dir(h["output"])
			eventually(t, func() bool { state := readCurrent(t, dir); return state.Seating != nil })
			state := readCurrent(t, dir)
			raw, err := os.ReadFile(filepath.Join(dir, "current.json"))
			if err != nil {
				t.Fatal(err)
			}
			var stateJSON map[string]any
			if err := json.Unmarshal(raw, &stateJSON); err != nil {
				t.Fatal(err)
			}
			want := source
			if source == "chairs" {
				want = ""
			}
			if (want == "" && stateJSON["ensemble_source"] != nil) || (want != "" && stateJSON["ensemble_source"] != want) || stateJSON["why"] != why {
				t.Fatal(stateJSON)
			}
			statusJSON, _ := captureStatus(t, "-json")
			var statusDoc struct {
				Runs []map[string]any `json:"runs"`
			}
			if err := json.Unmarshal([]byte(statusJSON), &statusDoc); err != nil || len(statusDoc.Runs) != 1 {
				t.Fatalf("%s: %v", statusJSON, err)
			}
			statusMap := statusDoc.Runs[0]
			if (want == "" && statusMap["ensemble_source"] != nil) || (want != "" && statusMap["ensemble_source"] != want) || statusMap["why"] != why {
				t.Fatal(statusMap)
			}
			if state.EnsembleSource != want || state.Why != why || state.Seating.Source != want {
				t.Fatalf("state: %+v seating: %+v", state, state.Seating)
			}
			rows := statusRows(t)
			if len(rows) != 1 || rows[0].EnsembleSource != want || rows[0].Why != why || rows[0].Ensemble.Source != want {
				t.Fatalf("status: %+v", rows)
			}
			text, _ := captureStatus(t)
			if strings.Contains(text, "\x1b") || strings.Contains(text, "chosen\n\x1b") || !strings.Contains(text, "why "+h["run"]+":") {
				t.Fatalf("status text %q", text)
			}
			if source == "text" && !strings.Contains(text, "chosen (from text)") {
				t.Fatal(text)
			}
			report := reportRows(t)
			if len(report) != 1 {
				t.Fatal(report)
			}
			row := report[0].(map[string]any)
			if row["why"] != why || (want != "" && row["ensemble_source"] != want) || (want == "" && row["ensemble_source"] != nil) {
				t.Fatal(row)
			}
			close(hold)
			sum := readDetachedSummary(t, h)
			if sum.Why != why || sum.EnsembleSource != want {
				t.Fatal(sum)
			}
			result := recordedResult(t, dir)
			if result["why"] != why || (want != "" && result["ensemble_source"] != want) || (want == "" && result["ensemble_source"] != nil) {
				t.Fatal(result)
			}
			report = reportRows(t)
			row = report[0].(map[string]any)
			if row["why"] != why || (want != "" && row["ensemble_source"] != want) || (want == "" && row["ensemble_source"] != nil) {
				t.Fatal(row)
			}
		})
	}
}

func TestAWhyOf1024BytesIsAcceptedAndRecorded(t *testing.T) {
	f := &runCAO{}
	_, args := runSessionArgs(t, f)
	why := strings.Repeat("é", 512)
	if err := runCommand(append(args, "-why", why)); err != nil {
		t.Fatal(err)
	}
	if got := recordedResult(t, onlyRunRecord(t))["why"]; got != why {
		t.Fatal(got)
	}
}
