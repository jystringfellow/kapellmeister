package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/jystringfellow/kapellmeister/internal/loop"
)

func setReportDirs(t *testing.T, dirs ...string) {
	t.Helper()
	quoted := make([]string, len(dirs))
	for i, dir := range dirs {
		quoted[i] = fmt.Sprintf("%q", dir)
	}
	writeFile(t, loop.UserConfigPath(loop.Home()), "[report]\ndirs = ["+strings.Join(quoted, ", ")+"]\n")
}

func isolateReportConfig(t *testing.T) {
	t.Helper()
	t.Setenv(loop.HomeEnv, t.TempDir())
	t.Setenv(loop.StateEnv, t.TempDir())
}

func recordCommands() map[string]func([]string, io.Writer, io.Writer) error {
	return map[string]func([]string, io.Writer, io.Writer) error{
		"report": reportCommand,
		"status": func(args []string, out, diag io.Writer) error {
			return statusAt(args, out, diag, statusNow)
		},
	}
}

func TestBareReportCoversEveryConfiguredDirectory(t *testing.T) {
	isolateReportConfig(t)
	a, b := t.TempDir(), t.TempDir()
	reportFixture(t, a, "one", "", "app", "first", "approved")
	reportFixture(t, b, "two", "", "other", "second", "failed")
	setReportDirs(t, a, b)
	doc := reportJSON(t)
	if len(doc["runs"].([]any)) != 2 || doc["totals"].(map[string]any)["runs"] != float64(2) {
		t.Fatal(doc)
	}
}

func TestBareStatusCoversEveryConfiguredDirectory(t *testing.T) {
	isolateReportConfig(t)
	a, b := t.TempDir(), t.TempDir()
	statusFixture(t, a, "one")
	statusFixture(t, b, "two")
	setReportDirs(t, a, b)
	if rows := statusRows(t); len(rows) != 2 || rows[0].RunID != "one" || rows[1].RunID != "two" {
		t.Fatal(rows)
	}
}

func TestCommandLineDirectoriesReplaceTheStateRunsAndTheConfiguredOnes(t *testing.T) {
	for name, command := range recordCommands() {
		t.Run(name, func(t *testing.T) {
			isolateReportConfig(t)
			configured, named := t.TempDir(), t.TempDir()
			statusFixture(t, filepath.Join(loop.StateDir(), "runs"), "state")
			statusFixture(t, configured, "configured")
			statusFixture(t, named, "named")
			for _, malformed := range []bool{false, true} {
				setReportDirs(t, configured)
				if malformed {
					writeFile(t, loop.UserConfigPath(loop.Home()), "[broken")
				}
				var out, diag bytes.Buffer
				if err := command([]string{"-json", named}, &out, &diag); err != nil {
					t.Fatal(err)
				}
				var doc map[string]any
				if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
					t.Fatal(err)
				}
				rows := doc["runs"].([]any)
				if len(rows) != 1 || rows[0].(map[string]any)["run_id"] != "named" || doc["searched"] != nil {
					t.Fatal(doc)
				}
			}
		})
	}
}

func TestConfiguredReportDirsAreSearchedInAdditionToTheStateRuns(t *testing.T) {
	for name, command := range recordCommands() {
		t.Run(name, func(t *testing.T) {
			isolateReportConfig(t)
			stateRuns, kept := filepath.Join(loop.StateDir(), "runs"), t.TempDir()
			statusFixture(t, stateRuns, "state")
			statusFixture(t, kept, "kept")
			setReportDirs(t, kept)
			var out, diag bytes.Buffer
			if err := command([]string{"-json"}, &out, &diag); err != nil {
				t.Fatal(err)
			}
			var doc struct {
				Runs []struct {
					RunID string `json:"run_id"`
				} `json:"runs"`
			}
			if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
				t.Fatal(err)
			}
			if len(doc.Runs) != 2 || doc.Runs[0].RunID != "kept" || doc.Runs[1].RunID != "state" || diag.Len() != 0 {
				t.Fatalf("stdout = %s, stderr = %s", &out, &diag)
			}
		})
	}
}

func TestAnEmptyReportDirsListLeavesTheStateRunsDefaultAndOutputUnchanged(t *testing.T) {
	isolateReportConfig(t)
	statusFixture(t, filepath.Join(loop.StateDir(), "runs"), "state")
	for name, command := range recordCommands() {
		t.Run(name, func(t *testing.T) {
			for _, flags := range [][]string{nil, {"-json"}} {
				writeFile(t, loop.UserConfigPath(loop.Home()), "")
				var before, after, named, diag bytes.Buffer
				if err := command(flags, &before, &diag); err != nil {
					t.Fatal(err)
				}
				if err := command(append(append([]string{}, flags...), filepath.Join(loop.StateDir(), "runs")), &named, &diag); err != nil {
					t.Fatal(err)
				}
				if before.String() != named.String() || strings.Contains(before.String(), "searched") && name == "status" {
					t.Fatalf("bare: %s; named: %s", &before, &named)
				}
				setReportDirs(t)
				if err := command(flags, &after, &diag); err != nil {
					t.Fatal(err)
				}
				if before.String() != after.String() {
					t.Fatalf("before: %s; empty setting: %s", &before, &after)
				}
				if len(flags) > 0 {
					var doc map[string]any
					if err := json.Unmarshal(after.Bytes(), &doc); err != nil {
						t.Fatal(err)
					}
					if _, exists := doc["searched"]; exists {
						t.Fatal(doc)
					}
				}
			}
		})
	}
}

func TestRecordCommandsNameTheConfiguredDirectoriesTheySearchedInTextAndJSON(t *testing.T) {
	for name, command := range recordCommands() {
		for _, withRuns := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/runs=%t", name, withRuns), func(t *testing.T) {
				isolateReportConfig(t)
				a, b := t.TempDir(), t.TempDir()
				t.Setenv("HOME", filepath.Dir(a))
				if withRuns {
					statusFixture(t, a, "one")
					statusFixture(t, b, "two")
				}
				setReportDirs(t, b, "~/"+filepath.Base(a)+"/./")
				var out, diag bytes.Buffer
				if err := command(nil, &out, &diag); err != nil {
					t.Fatal(err)
				}
				want := "searched: " + plain(b) + "\nsearched: " + plain(a) + "\n"
				if !strings.HasSuffix(out.String(), want) {
					t.Fatalf("output = %s, want suffix %q", &out, want)
				}
				if !withRuns && !strings.HasPrefix(out.String(), "no runs") {
					t.Fatal(&out)
				}
				out.Reset()
				if err := command([]string{"-json"}, &out, &diag); err != nil {
					t.Fatal(err)
				}
				var doc struct {
					Searched []string `json:"searched"`
				}
				if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(doc.Searched, []string{b, a}) {
					t.Fatal(doc.Searched)
				}
			})
		}
	}
}

func TestAConfiguredPathThatIsNotADirectoryFailsBeforePrintingAndNamesTheSetting(t *testing.T) {
	for name, command := range recordDiscoveryCommands() {
		t.Run(name, func(t *testing.T) {
			isolateReportConfig(t)
			good := t.TempDir()
			statusFixture(t, good, "good")
			file := filepath.Join(t.TempDir(), "file")
			writeFile(t, file, "")
			setReportDirs(t, good, file)
			var out, diag bytes.Buffer
			err := command(nil, &out, &diag)
			if err == nil || out.Len() != 0 || !strings.Contains(err.Error(), "not a directory") || !strings.Contains(err.Error(), file) || !strings.Contains(err.Error(), "from [report] dirs in "+loop.UserConfigPath(loop.Home())) {
				t.Fatalf("error = %v, output = %s", err, &out)
			}
		})
	}
}

func recordDiscoveryCommands() map[string]func([]string, io.Writer, io.Writer) error {
	commands := recordCommands()
	commands["wait"] = func(args []string, out, diag io.Writer) error {
		return waitCommand(append([]string{"good"}, args...), out, diag)
	}
	return commands
}

func TestRecordCommandsSkipAConfiguredDirectoryThatDoesNotExistAndSearchTheRest(t *testing.T) {
	for name, command := range recordCommands() {
		t.Run(name, func(t *testing.T) {
			isolateReportConfig(t)
			good, missing := t.TempDir(), filepath.Join(t.TempDir(), "absent")
			statusFixture(t, good, "found")
			setReportDirs(t, good)
			var before, out, diag bytes.Buffer
			if err := command(nil, &before, &diag); err != nil {
				t.Fatal(err)
			}
			setReportDirs(t, missing, good)
			if err := command(nil, &out, &diag); err != nil {
				t.Fatal(err)
			}
			wantDiag := "km: " + name + ": " + missing + ": not searched: directory does not exist\n"
			if diag.String() != wantDiag || out.String() != before.String() || strings.Contains(out.String(), missing) || !strings.Contains(out.String(), "searched: "+good+"\n") || strings.Contains(out.String(), "not searched") {
				t.Fatalf("stdout = %s, stderr = %s", &out, &diag)
			}
		})
	}
}

func TestRecordCommandsListSkippedConfiguredDirectoriesInJSON(t *testing.T) {
	for name, command := range recordCommands() {
		t.Run(name, func(t *testing.T) {
			isolateReportConfig(t)
			good := t.TempDir()
			statusFixture(t, good, "found")
			missing := []string{filepath.Join(t.TempDir(), "absent"), filepath.Join(t.TempDir(), "parent", "absent")}
			for _, skip := range []bool{true, false} {
				setReportDirs(t, good)
				if skip {
					setReportDirs(t, missing[0], good, missing[1])
				}
				var out, diag bytes.Buffer
				if err := command([]string{"-json"}, &out, &diag); err != nil {
					t.Fatal(err)
				}
				var doc map[string]any
				if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(doc["searched"], []any{good}) || len(doc["runs"].([]any)) != 1 {
					t.Fatal(doc)
				}
				if skip {
					if !reflect.DeepEqual(doc["skipped_dirs"], []any{missing[0], missing[1]}) || strings.Count(diag.String(), "does not exist\n") != 2 {
						t.Fatalf("doc = %v, stderr = %s", doc, &diag)
					}
				} else if _, exists := doc["skipped_dirs"]; exists || diag.Len() != 0 {
					t.Fatalf("doc = %v, stderr = %s", doc, &diag)
				}
				if name == "report" && (doc["not_searched_total"] != float64(0) || len(doc["not_searched"].([]any)) != 0) {
					t.Fatal(doc)
				}
			}
		})
	}
}

func TestRecordCommandsSucceedWithNoRunsWhenEveryConfiguredDirectoryIsMissing(t *testing.T) {
	for name, command := range recordCommands() {
		t.Run(name, func(t *testing.T) {
			isolateReportConfig(t)
			missing := filepath.Join(t.TempDir(), "absent")
			setReportDirs(t, missing)
			for _, args := range [][]string{nil, {"-json"}} {
				var out, diag bytes.Buffer
				if err := command(args, &out, &diag); err != nil {
					t.Fatal(err)
				}
				if diag.String() != "km: "+name+": "+missing+": not searched: directory does not exist\n" {
					t.Fatal(&diag)
				}
				if len(args) == 0 {
					if !strings.HasPrefix(out.String(), "no runs") || strings.Contains(out.String(), "searched") {
						t.Fatal(&out)
					}
					continue
				}
				var doc map[string]any
				if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
					t.Fatal(err)
				}
				if len(doc["runs"].([]any)) != 0 || doc["searched"] != nil || !reflect.DeepEqual(doc["skipped_dirs"], []any{missing}) {
					t.Fatal(doc)
				}
			}
		})
	}
}

func TestRecordCommandsStillFailOnAnExplicitMissingDirectory(t *testing.T) {
	for name, command := range recordDiscoveryCommands() {
		t.Run(name, func(t *testing.T) {
			isolateReportConfig(t)
			setReportDirs(t, t.TempDir())
			missing := filepath.Join(t.TempDir(), "absent")
			var out, diag bytes.Buffer
			err := command([]string{missing}, &out, &diag)
			if err == nil || !strings.Contains(err.Error(), missing) || !strings.Contains(err.Error(), "no such file or directory") || strings.Contains(err.Error(), "from [report] dirs") || out.Len() != 0 || diag.Len() != 0 {
				t.Fatalf("error = %v, stdout = %s, stderr = %s", err, &out, &diag)
			}
		})
	}
}

func TestAConfiguredDirectoryThatCannotBeReadStillFails(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can read directories without permission")
	}
	for name, command := range recordDiscoveryCommands() {
		for _, inaccessibleParent := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/parent=%t", name, inaccessibleParent), func(t *testing.T) {
				isolateReportConfig(t)
				good, denied := t.TempDir(), t.TempDir()
				path := denied
				if inaccessibleParent {
					path = filepath.Join(denied, "records")
					if err := os.Mkdir(path, 0700); err != nil {
						t.Fatal(err)
					}
				}
				if err := os.Chmod(denied, 0); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { os.Chmod(denied, 0700) })
				if _, err := os.ReadDir(path); !errors.Is(err, fs.ErrPermission) {
					t.Fatalf("expected permission denial, got %v", err)
				}
				setReportDirs(t, good, path)
				var out, diag bytes.Buffer
				err := command(nil, &out, &diag)
				if err == nil || out.Len() != 0 || !strings.Contains(err.Error(), "permission denied") || !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), "from [report] dirs in "+loop.UserConfigPath(loop.Home())) {
					t.Fatalf("error = %v, stdout = %s, stderr = %s", err, &out, &diag)
				}
			})
		}
	}
}

func TestConfiguredDirectoriesThatAreDuplicatesOrNestedCountEachRunOnce(t *testing.T) {
	isolateReportConfig(t)
	root := t.TempDir()
	nested := filepath.Join(root, "nested")
	statusFixture(t, root, "one")
	statusFixture(t, nested, "two")
	setReportDirs(t, root, root+"/", nested)
	if rows := statusRows(t); len(rows) != 2 {
		t.Fatal(rows)
	}
	link := filepath.Join(t.TempDir(), "linked")
	if err := os.Symlink(root, link); err != nil {
		t.Fatal(err)
	}
	setReportDirs(t, root, root+"/", nested, link)
	doc := reportJSON(t)
	if len(doc["runs"].([]any)) != 2 || doc["totals"].(map[string]any)["runs"] != float64(2) {
		t.Fatal(doc)
	}
	if !reflect.DeepEqual(doc["searched"], []any{root, nested, link}) {
		t.Fatal(doc["searched"])
	}
}

func TestMaxDepthAndMaxEntriesApplyAcrossConfiguredDirectories(t *testing.T) {
	for name, command := range recordCommands() {
		for _, limit := range []string{"-max-depth", "-max-entries"} {
			t.Run(name+"/"+limit, func(t *testing.T) {
				isolateReportConfig(t)
				base := recordRoot(t)
				a, b := filepath.Join(base, "a"), filepath.Join(base, "b")
				statusFixture(t, a, "one")
				statusFixture(t, b, "two")
				setReportDirs(t, a, b)
				value, wantRuns := "1", 0
				if limit == "-max-entries" {
					value, wantRuns = "2", 1
				}
				var out, diag bytes.Buffer
				if err := command([]string{limit, value}, &out, &diag); err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(diag.String(), "not searched") || !strings.Contains(diag.String(), limit) {
					t.Fatalf("output = %s, diagnostics = %s", &out, &diag)
				}
				if !strings.HasSuffix(out.String(), "searched: "+b+"\n") && name == "status" {
					t.Fatal(&out)
				}
				if name == "report" && (!strings.Contains(out.String(), "not searched:") || strings.Index(out.String(), "searched: "+a) > strings.Index(out.String(), "not searched:")) {
					t.Fatal(&out)
				}
				out.Reset()
				diag.Reset()
				if err := command([]string{limit, value, "-json"}, &out, &diag); err != nil {
					t.Fatal(err)
				}
				var doc map[string]any
				if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
					t.Fatal(err)
				}
				if len(doc["runs"].([]any)) != wantRuns || doc["searched"] == nil {
					t.Fatal(doc)
				}
				if name == "report" {
					found := false
					for _, item := range doc["not_searched"].([]any) {
						path := item.(map[string]any)["path"].(string)
						found = found || path == b || strings.HasPrefix(path, b+string(filepath.Separator))
					}
					if !found || doc["not_searched_total"].(float64) < 1 {
						t.Fatal(doc)
					}
				}
			})
		}
	}
}

func TestReportFiltersAndPartsApplyAcrossConfiguredDirectories(t *testing.T) {
	isolateReportConfig(t)
	a, b := t.TempDir(), t.TempDir()
	reportFixture(t, a, "early", "2026-11-09T14:00:00Z", "app", "first", "approved")
	partsFixture(t, b, "later", "approved", 1, partStep("implement", "model", "impl", 1))
	reportFixture(t, b, "other", "2026-11-10T14:00:00Z", "other", "second", "approved")
	setReportDirs(t, a, b)
	for _, tc := range []struct {
		args  []string
		count int
	}{
		{[]string{"-since", "2026-11-10"}, 2},
		{[]string{"-repository", "app"}, 2},
		{[]string{"-since", "2026-11-10", "-repository", "app", "-parts"}, 1},
	} {
		doc := reportJSON(t, tc.args...)
		if len(doc["runs"].([]any)) != tc.count {
			t.Fatal(doc)
		}
		if len(tc.args) > 4 && doc["parts"] == nil {
			t.Fatal(doc)
		}
	}
	if rows := partRows(t); len(rows) != 1 || rows[0].(map[string]any)["part"] != "implementer" {
		t.Fatal(rows)
	}
}

func TestStatusRefusesAConfiguredDirectoryThatIsItselfASymlink(t *testing.T) {
	isolateReportConfig(t)
	root, link := t.TempDir(), filepath.Join(t.TempDir(), "link")
	statusFixture(t, root, "one")
	if err := os.Symlink(root, link); err != nil {
		t.Fatal(err)
	}
	setReportDirs(t, link)
	var out, diag bytes.Buffer
	err := statusCommand(nil, &out, &diag)
	if err == nil || out.Len() != 0 || !strings.Contains(err.Error(), "from [report] dirs in") {
		t.Fatalf("error = %v, output = %s", err, &out)
	}
}

func TestAMalformedUserConfigStopsBareRecordCommandsButNotOnesNamingADirectory(t *testing.T) {
	for name, command := range recordCommands() {
		for _, body := range []string{"[broken", "[report]\nunknown = []\n", "[report]\ndirs = ['relative']\n"} {
			t.Run(name+"/"+body, func(t *testing.T) {
				isolateReportConfig(t)
				writeFile(t, loop.UserConfigPath(loop.Home()), body)
				var out, diag bytes.Buffer
				err := command(nil, &out, &diag)
				if err == nil || out.Len() != 0 || !strings.Contains(err.Error(), loop.UserConfigPath(loop.Home())) {
					t.Fatalf("error = %v, output = %s", err, &out)
				}
				if err := command([]string{t.TempDir()}, &out, &diag); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

func TestRecordCommandsLoadReportDirsWithoutValidatingUnrelatedSettings(t *testing.T) {
	isolateReportConfig(t)
	root := t.TempDir()
	statusFixture(t, root, "one")
	setReportDirs(t, root)
	file, err := os.OpenFile(loop.UserConfigPath(loop.Home()), os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("[chairs]\nnonexistent = 'invalid'\n"); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if rows := reportRows(t); len(rows) != 1 {
		t.Fatal(rows)
	}
	if rows := statusRows(t); len(rows) != 1 {
		t.Fatal(rows)
	}
}

func TestConfigShowsTheReportDirectoriesOnlyWhenSet(t *testing.T) {
	isolateReportConfig(t)
	repo, a, b := t.TempDir(), t.TempDir(), t.TempDir()
	for _, configured := range []bool{false, true} {
		body := builtInAgentConfig
		if configured {
			body += fmt.Sprintf("[report]\ndirs = [%q, %q]\n", a, b)
		}
		writeFile(t, loop.UserConfigPath(loop.Home()), body)
		out, err := captureStdout(t, func() error { return configCommand([]string{"-worktree", repo, "-cao", unreachableCAO}) })
		if err != nil {
			t.Fatal(err)
		}
		if configured {
			if !strings.Contains(out, "report dirs "+a+", "+b+" (in addition to "+filepath.Join(loop.StateDir(), "runs")+")\n") {
				t.Fatal(out)
			}
		} else if strings.Contains(out, "report dirs") {
			t.Fatal(out)
		}
	}
	writeFile(t, filepath.Join(repo, loop.ConfigFile), "[report]\ndirs = []\n")
	_, err := captureStdout(t, func() error { return configCommand([]string{"-worktree", repo}) })
	if err == nil || !strings.Contains(err.Error(), "[report] belongs in "+loop.UserConfigPath(loop.Home())) {
		t.Fatal(err)
	}
}

func TestSearchedListsTheStateRunsFirstWhenItExists(t *testing.T) {
	for name, command := range recordCommands() {
		t.Run(name, func(t *testing.T) {
			isolateReportConfig(t)
			stateRuns, kept := filepath.Join(loop.StateDir(), "runs"), t.TempDir()
			statusFixture(t, stateRuns, "state")
			setReportDirs(t, kept)
			var out, diag bytes.Buffer
			if err := command(nil, &out, &diag); err != nil {
				t.Fatal(err)
			}
			want := "searched: " + stateRuns + "\nsearched: " + kept + "\n"
			if !strings.HasSuffix(out.String(), want) {
				t.Fatalf("output = %s, want suffix %q", &out, want)
			}
			out.Reset()
			if err := command([]string{"-json"}, &out, &diag); err != nil {
				t.Fatal(err)
			}
			var doc struct {
				Searched []string `json:"searched"`
			}
			if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(doc.Searched, []string{stateRuns, kept}) {
				t.Fatal(doc.Searched)
			}
		})
	}
}

func TestListingTheStateRunsInReportDirsSearchesItOnceAndCountsEachRunOnce(t *testing.T) {
	for name, command := range recordCommands() {
		for _, spelling := range []string{"", "/", "/../runs"} {
			t.Run(name+spelling, func(t *testing.T) {
				isolateReportConfig(t)
				stateRuns, kept := filepath.Join(loop.StateDir(), "runs"), t.TempDir()
				statusFixture(t, stateRuns, "state")
				statusFixture(t, kept, "kept")
				setReportDirs(t, kept, stateRuns+spelling, stateRuns)
				var out, diag bytes.Buffer
				if err := command([]string{"-json"}, &out, &diag); err != nil {
					t.Fatal(err)
				}
				var doc map[string]any
				if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
					t.Fatal(err)
				}
				if len(doc["runs"].([]any)) != 2 || !reflect.DeepEqual(doc["searched"], []any{stateRuns, kept}) {
					t.Fatal(doc)
				}
				if name == "report" && doc["totals"].(map[string]any)["runs"] != float64(2) {
					t.Fatal(doc)
				}
			})
		}
	}
}

func TestAMissingStateRunsIsSilentWhetherOrNotReportDirsIsSet(t *testing.T) {
	for name, command := range recordCommands() {
		t.Run(name, func(t *testing.T) {
			isolateReportConfig(t)
			stateRuns, kept := filepath.Join(loop.StateDir(), "runs"), t.TempDir()
			statusFixture(t, kept, "kept")
			for _, dirs := range [][]string{nil, {kept}, {stateRuns, kept}, {stateRuns}} {
				setReportDirs(t, dirs...)
				var out, diag bytes.Buffer
				if err := command([]string{"-json"}, &out, &diag); err != nil {
					t.Fatal(err)
				}
				var doc map[string]any
				if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
					t.Fatal(err)
				}
				if diag.Len() != 0 || doc["skipped_dirs"] != nil {
					t.Fatalf("stdout = %s, stderr = %s", &out, &diag)
				}
				if len(dirs) > 0 && dirs[len(dirs)-1] == kept {
					if !reflect.DeepEqual(doc["searched"], []any{kept}) || len(doc["runs"].([]any)) != 1 {
						t.Fatal(doc)
					}
				} else if doc["searched"] != nil || len(doc["runs"].([]any)) != 0 {
					t.Fatal(doc)
				}
			}
		})
	}
}

func TestAnErrorFromTheStateRunsIsNotAttributedToReportDirs(t *testing.T) {
	for name, command := range recordDiscoveryCommands() {
		for _, kind := range []string{"file", "loop", "denied"} {
			t.Run(name+"/"+kind, func(t *testing.T) {
				isolateReportConfig(t)
				stateRuns := filepath.Join(loop.StateDir(), "runs")
				want := ""
				switch kind {
				case "file":
					writeFile(t, stateRuns, "")
					want = "not a directory"
				case "loop":
					if err := os.Symlink(stateRuns, stateRuns); err != nil {
						t.Fatal(err)
					}
					want = "too many"
				case "denied":
					if os.Geteuid() == 0 {
						t.Skip("root can read directories without permission")
					}
					if err := os.Mkdir(stateRuns, 0); err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { os.Chmod(stateRuns, 0700) })
					want = "permission denied"
				}
				for _, explicit := range []bool{false, true} {
					setReportDirs(t, t.TempDir())
					if explicit {
						setReportDirs(t, stateRuns)
					}
					var out, diag bytes.Buffer
					err := command(nil, &out, &diag)
					if err == nil || !strings.Contains(err.Error(), want) || strings.Contains(err.Error(), "from [report] dirs") || out.Len() != 0 {
						t.Fatalf("error = %v, stdout = %s, stderr = %s", err, &out, &diag)
					}
				}
			})
		}
	}
}
