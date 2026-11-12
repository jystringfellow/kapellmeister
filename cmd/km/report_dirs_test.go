package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
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

func TestRecordDirectoriesOnTheCommandLineReplaceTheConfiguredOnes(t *testing.T) {
	for name, command := range recordCommands() {
		t.Run(name, func(t *testing.T) {
			isolateReportConfig(t)
			configured, named := t.TempDir(), t.TempDir()
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

func TestConfiguredReportDirsDoNotAddTheStateRunsUnlessListed(t *testing.T) {
	isolateReportConfig(t)
	stateRuns, kept := filepath.Join(loop.StateDir(), "runs"), t.TempDir()
	statusFixture(t, stateRuns, "state")
	statusFixture(t, kept, "kept")
	setReportDirs(t, kept)
	if rows := reportRows(t); len(rows) != 1 || rows[0].(map[string]any)["run_id"] != "kept" {
		t.Fatal(rows)
	}
	if rows := statusRows(t); len(rows) != 1 || rows[0].RunID != "kept" {
		t.Fatal(rows)
	}
	setReportDirs(t, kept, stateRuns)
	if rows := reportRows(t); len(rows) != 2 {
		t.Fatal(rows)
	}
	if rows := statusRows(t); len(rows) != 2 {
		t.Fatal(rows)
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

func TestAConfiguredDirectoryThatDoesNotExistFailsBeforePrintingAndNamesTheSetting(t *testing.T) {
	for name, command := range recordCommands() {
		t.Run(name, func(t *testing.T) {
			isolateReportConfig(t)
			good := t.TempDir()
			statusFixture(t, good, "good")
			for _, file := range []bool{false, true} {
				missing := filepath.Join(t.TempDir(), "unusable")
				if file {
					writeFile(t, missing, "")
				}
				setReportDirs(t, good, missing)
				var out, diag bytes.Buffer
				err := command(nil, &out, &diag)
				if err == nil || out.Len() != 0 || !strings.Contains(err.Error(), missing) || !strings.Contains(err.Error(), "from [report] dirs in "+loop.UserConfigPath(loop.Home())) {
					t.Fatalf("error = %v, output = %s", err, &out)
				}
			}
		})
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
			if !strings.Contains(out, "report dirs "+a+", "+b+"\n") {
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
