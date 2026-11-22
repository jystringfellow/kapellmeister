package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"syscall"
	"time"

	"fmt"
	"github.com/jystringfellow/kapellmeister/internal/loop"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSaveWritesTheExactBytesFromAFile(t *testing.T) {
	home, wt := t.TempDir(), t.TempDir()
	t.Setenv("KAPELLMEISTER_HOME", home)
	writeFile(t, filepath.Join(home, "config.toml"), "[agents]\navailable = [\"claude\", \"codex\", \"copilot\", \"agy\"]\n")
	content := strings.TrimSuffix(sectionedEnsemble, "\n")
	src := filepath.Join(wt, "input.toml")
	writeFile(t, src, content)
	var diag bytes.Buffer
	out, err := captureStdout(t, func() error {
		if code := execute([]string{"ensemble", "save", "-name=sample", "-worktree=" + wt, src}, &diag); code != 0 {
			t.Errorf("exit %d: %s", code, diag.String())
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(home, "ensembles", "sample.toml"))
	if err != nil || string(data) != content || !strings.Contains(out, "saved sample to ") {
		t.Fatalf("output=%q data=%q err=%v", out, data, err)
	}
}

func TestSaveRefusesConflictsAndLinksAndReplacesOnlyWhenAllowed(t *testing.T) {
	for _, repo := range []bool{false, true} {
		for _, replace := range []bool{false, true} {
			t.Run(fmt.Sprintf("repo=%t/replace=%t", repo, replace), func(t *testing.T) {
				home, wt := t.TempDir(), t.TempDir()
				safetyGit(t, wt, "init", "-q")
				t.Setenv("KAPELLMEISTER_HOME", home)
				writeFile(t, filepath.Join(home, "config.toml"), "[agents]\navailable = ['claude', 'codex', 'copilot', 'agy']\n")
				dest := filepath.Join(home, "ensembles", "sample.toml")
				if repo {
					dest = filepath.Join(wt, ".kapellmeister", "ensembles", "sample.toml")
				}
				writeFile(t, dest, "old")
				args := []string{"-name=sample", "-worktree=" + wt, "-json"}
				if repo {
					args = append(args, "-repository")
				}
				if replace {
					args = append(args, "-replace")
				}
				args = append(args, "-")
				var out, diag bytes.Buffer
				err := saveCommand(args, strings.NewReader(sectionedEnsemble), &out, &diag)
				data, e := os.ReadFile(dest)
				if e != nil {
					t.Fatal(e)
				}
				if replace {
					if err != nil || string(data) != sectionedEnsemble || !strings.Contains(out.String(), `"replaced":true`) {
						t.Fatal(err, out.String())
					}
				} else if err == nil || string(data) != "old" || !strings.Contains(out.String(), "-replace") {
					t.Fatal(err, out.String())
				}
				if e = os.Remove(dest); e != nil {
					t.Fatal(e)
				}
				target := filepath.Join(wt, "target")
				writeFile(t, target, "untouched")
				if e = os.Symlink(target, dest); e != nil {
					t.Fatal(e)
				}
				out.Reset()
				err = saveCommand(args, strings.NewReader(sectionedEnsemble), &out, &diag)
				data, _ = os.ReadFile(target)
				if err == nil || string(data) != "untouched" {
					t.Fatal(err, out.String())
				}
			})
		}
	}
}

func TestSaveReportsCheckErrorsAndReadiness(t *testing.T) {
	home, wt := t.TempDir(), t.TempDir()
	t.Setenv("KAPELLMEISTER_HOME", home)
	writeFile(t, filepath.Join(home, "config.toml"), "[agents]\navailable = ['claude']\n")
	var out, diag bytes.Buffer
	err := saveCommand([]string{"-name=sample", "-worktree=" + wt, "-"}, strings.NewReader("bad = true"), &out, &diag)
	if err == nil {
		t.Fatal(out.String())
	}
	if _, err = os.Stat(filepath.Join(home, "ensembles")); !os.IsNotExist(err) {
		t.Fatal(err)
	}
	out.Reset()
	err = saveCommand([]string{"-name=sample", "-worktree=" + wt, "-"}, strings.NewReader(sectionedEnsemble), &out, &diag)
	if err == nil || !strings.Contains(out.String(), "text ensembles may only use names") {
		t.Fatal(err, out.String())
	}
}

func TestSaveReadsARunRecordByPrefixAndRefusesANamedRun(t *testing.T) {
	home, wt, records := t.TempDir(), t.TempDir(), t.TempDir()
	safetyGit(t, wt, "init", "-q")
	t.Setenv("KAPELLMEISTER_HOME", home)
	writeFile(t, filepath.Join(home, "config.toml"), "[agents]\navailable = ['claude', 'codex', 'copilot', 'agy']\n[report]\ndirs = ['"+records+"']\n")
	reportFixture(t, records, "recorded", "", "app", "original", "approved")
	writeFile(t, filepath.Join(records, "recorded", "ensemble.toml"), sectionedEnsemble)
	var out, diag bytes.Buffer
	err := saveCommand([]string{"-name=sample", "-worktree=" + wt, "-repository", "-run=reco"}, nil, &out, &diag)
	data, e := os.ReadFile(filepath.Join(wt, ".kapellmeister", "ensembles", "sample.toml"))
	if err != nil || e != nil || string(data) != sectionedEnsemble {
		t.Fatal(err, e, out.String(), diag.String())
	}
	reportFixture(t, records, "named", "", "app", "original", "approved")
	out.Reset()
	err = saveCommand([]string{"-name=other", "-worktree=" + wt, "-run=named"}, nil, &out, &diag)
	if err == nil || !strings.Contains(out.String(), "original") {
		t.Fatal(err, out.String())
	}
	out.Reset()
	err = saveCommand([]string{"-name=other", "-worktree=" + wt, "-run=absent"}, nil, &out, &diag)
	if err == nil || !strings.Contains(out.String(), "absent") {
		t.Fatal(err, out.String())
	}
}

func TestSaveHonorsShadowingAndConfiguredNames(t *testing.T) {
	for _, kind := range []string{"user-shadows-repository", "repository-shadowed-by-user", "config"} {
		for _, replace := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/%t", kind, replace), func(t *testing.T) {
				home, wt := t.TempDir(), t.TempDir()
				safetyGit(t, wt, "init", "-q")
				t.Setenv("KAPELLMEISTER_HOME", home)
				config := "[agents]\navailable = ['claude','codex','copilot','agy']\n"
				existing := filepath.Join(wt, ".kapellmeister", "ensembles", "sample.toml")
				if kind == "repository-shadowed-by-user" {
					existing = filepath.Join(home, "ensembles", "sample.toml")
				}
				if kind == "config" {
					config += "[ensembles.sample]\ngate = 'claude'\n"
				} else {
					writeFile(t, existing, "old")
				}
				writeFile(t, filepath.Join(home, "config.toml"), config)
				args := []string{"-json", "-name=sample", "-worktree=" + wt}
				if kind == "repository-shadowed-by-user" {
					args = append(args, "-repository")
				}
				if replace {
					args = append(args, "-replace")
				}
				args = append(args, "-")
				var out, diag bytes.Buffer
				err := saveCommand(args, strings.NewReader(sectionedEnsemble), &out, &diag)
				allowed := kind == "user-shadows-repository" && replace
				if (err == nil) != allowed {
					t.Fatal(err, out.String(), diag.String())
				}
				dest := filepath.Join(home, "ensembles", "sample.toml")
				if kind == "repository-shadowed-by-user" {
					dest = filepath.Join(wt, ".kapellmeister", "ensembles", "sample.toml")
				}
				if kind != "config" {
					data, e := os.ReadFile(existing)
					if e != nil || string(data) != "old" {
						t.Fatal(e, string(data))
					}
				}
				if allowed {
					data, e := os.ReadFile(dest)
					var doc saveResult
					if e != nil || string(data) != sectionedEnsemble || json.Unmarshal(out.Bytes(), &doc) != nil || doc.Replaced {
						t.Fatal(e, out.String())
					}
				} else if _, e := os.Lstat(dest); !os.IsNotExist(e) {
					t.Fatal(e)
				}
				if !allowed {
					expected := existing
					if kind == "config" {
						expected = "[ensembles.sample]"
						if !strings.Contains(out.String(), filepath.Join(home, "config.toml")) {
							t.Fatal(out.String())
						}
					}
					if !strings.Contains(out.String(), expected) {
						t.Fatal(out.String())
					}
				}
			})
		}
	}
}

func TestSaveNamesRunRecordErrorsAndOmitsAnUnknownSource(t *testing.T) {
	home, wt, records := t.TempDir(), t.TempDir(), t.TempDir()
	t.Setenv("KAPELLMEISTER_HOME", home)
	writeFile(t, filepath.Join(home, "config.toml"), "[report]\ndirs = ['"+records+"']\n")
	reportFixture(t, records, "named", "", "app", "original", "approved")
	var out, diag bytes.Buffer
	err := saveCommand([]string{"-name=sample", "-worktree=" + wt, "-run=named"}, nil, &out, &diag)
	if err == nil || strings.Contains(out.String(), "()") || !strings.Contains(out.String(), `ensemble "original"`) {
		t.Error(err, out.String())
	}
	path := filepath.Join(records, "named", "ensemble.toml")
	if e := os.Symlink("missing", path); e != nil {
		t.Fatal(e)
	}
	out.Reset()
	err = saveCommand([]string{"-name=sample", "-worktree=" + wt, "-run=named"}, nil, &out, &diag)
	if err == nil || !strings.Contains(out.String(), path) {
		t.Error(err, out.String())
	}
}

func TestSaveReportsTheUserFileWhenBothDestinationsExist(t *testing.T) {
	home, wt := t.TempDir(), t.TempDir()
	safetyGit(t, wt, "init", "-q")
	t.Setenv("KAPELLMEISTER_HOME", home)
	writeFile(t, filepath.Join(home, "config.toml"), "[agents]\navailable = ['claude','codex','copilot','agy']\n")
	user := filepath.Join(home, "ensembles", "sample.toml")
	repo := filepath.Join(wt, ".kapellmeister", "ensembles", "sample.toml")
	writeFile(t, user, "user")
	writeFile(t, repo, "repo")
	var out, diag bytes.Buffer
	err := saveCommand([]string{"-name=sample", "-worktree=" + wt, "-repository", "-"}, strings.NewReader(sectionedEnsemble), &out, &diag)
	if err == nil || !strings.Contains(out.String(), user) || strings.Contains(out.String(), "-replace") {
		t.Fatal(err, out.String())
	}
}

func TestSaveSourcesAppearInRepertoireWithTheirFitAndReadiness(t *testing.T) {
	for _, source := range []string{"file", "stdin", "run"} {
		for _, location := range []string{"user", "repository"} {
			t.Run(source+"/"+location, func(t *testing.T) {
				home, wt, records := t.TempDir(), t.TempDir(), t.TempDir()
				safetyGit(t, wt, "init", "-q")
				t.Setenv(loop.HomeEnv, home)
				writeFile(t, filepath.Join(home, "config.toml"), "[agents]\navailable = ['claude','codex','copilot','agy']\n[report]\ndirs = ['"+records+"']\n")
				content := strings.TrimSuffix(sectionedEnsemble, "\n") + "  "
				args := []string{"-json", "-name=sample", "-worktree=" + wt}
				if location == "repository" {
					args = append(args, "-repository")
				}
				var input *strings.Reader
				switch source {
				case "file":
					path := filepath.Join(wt, "input.toml")
					writeFile(t, path, content)
					args = append(args, path)
				case "stdin":
					args = append(args, "-")
					input = strings.NewReader(content)
				case "run":
					reportFixture(t, records, "recorded", "", "app", "sample", "approved")
					writeFile(t, filepath.Join(records, "recorded", "ensemble.toml"), content)
					args = append(args, "-run=reco")
				}
				var out, diag bytes.Buffer
				var err error
				if input == nil {
					err = saveCommand(args, nil, &out, &diag)
				} else {
					err = saveCommand(args, input, &out, &diag)
				}
				if err != nil {
					t.Fatal(err, out.String(), diag.String())
				}
				var result saveResult
				if err = json.Unmarshal(out.Bytes(), &result); err != nil {
					t.Fatal(err)
				}
				dir := filepath.Join(home, "ensembles")
				if location == "repository" {
					dir = filepath.Join(wt, ".kapellmeister", "ensembles")
				}
				path := filepath.Join(dir, "sample.toml")
				data, err := os.ReadFile(path)
				if err != nil || string(data) != content || result.Path != path || !result.Ready || result.Replaced || result.Errors == nil || len(result.Errors) != 0 {
					t.Fatal(err, result)
				}
				info, err := os.Stat(path)
				if err != nil || info.Mode().Perm() != 0o644 {
					t.Fatal(info, err)
				}
				entries, err := os.ReadDir(dir)
				if err != nil || len(entries) != 1 || entries[0].Name() != "sample.toml" {
					t.Fatal(entries, err)
				}
				out.Reset()
				if err = repertoireCommand([]string{"-json", "-worktree=" + wt}, &out, &diag); err != nil {
					t.Fatal(err)
				}
				var rep struct {
					Ensembles []repertoireEnsemble `json:"ensembles"`
				}
				if err = json.Unmarshal(out.Bytes(), &rep); err != nil {
					t.Fatal(err)
				}
				if len(rep.Ensembles) != 1 {
					t.Fatal(rep)
				}
				row := rep.Ensembles[0]
				if row.Name != "sample" || row.Source != location || row.Score != "default" || row.Chairs != 6 || !row.Ready || row.Reason != "" {
					t.Fatal(row)
				}
			})
		}
	}
}

func TestSaveRefusesChairsAndAmbiguousRuns(t *testing.T) {
	home, wt, records := t.TempDir(), t.TempDir(), t.TempDir()
	t.Setenv(loop.HomeEnv, home)
	writeFile(t, filepath.Join(home, "config.toml"), "[report]\ndirs = ['"+records+"']\n")
	reportFixture(t, records, "chairs", "", "app", "", "approved")
	reportFixture(t, records, "same-one", "", "app", "x", "approved")
	reportFixture(t, records, "same-two", "", "app", "x", "approved")
	for _, tc := range []struct{ id, want string }{{"chairs", "[chairs]"}, {"same", "ambiguous"}, {"absent", "absent"}} {
		var out, diag bytes.Buffer
		err := saveCommand([]string{"-name=sample", "-worktree=" + wt, "-run=" + tc.id}, nil, &out, &diag)
		if err == nil || !strings.Contains(out.String(), tc.want) {
			t.Error(err, out.String())
		}
	}
}

func TestSaveRefusesNamedPipesWithoutBlocking(t *testing.T) {
	home, wt := t.TempDir(), t.TempDir()
	t.Setenv(loop.HomeEnv, home)
	path := filepath.Join(wt, "source")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan string, 1)
	go func() {
		var out, diag bytes.Buffer
		_ = saveCommand([]string{"-name=sample", "-worktree=" + wt, path}, nil, &out, &diag)
		done <- out.String()
	}()
	select {
	case out := <-done:
		if !strings.Contains(out, path+": named pipe is not a regular file") {
			t.Fatal(out)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("save blocked on named pipe")
	}
}

func TestSaveRefusesDanglingLinksAndDirectoriesAtTheDestination(t *testing.T) {
	for _, kind := range []string{"dangling", "directory"} {
		for _, replace := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/%t", kind, replace), func(t *testing.T) {
				home, wt := t.TempDir(), t.TempDir()
				t.Setenv(loop.HomeEnv, home)
				writeFile(t, filepath.Join(home, "config.toml"), "[agents]\navailable=['claude','codex','copilot','agy']\n")
				dest := filepath.Join(home, "ensembles", "sample.toml")
				if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
					t.Fatal(err)
				}
				var err error
				if kind == "dangling" {
					err = os.Symlink(filepath.Join(wt, "missing"), dest)
				} else {
					err = os.Mkdir(dest, 0o755)
				}
				if err != nil {
					t.Fatal(err)
				}
				args := []string{"-name=sample", "-worktree=" + wt}
				if replace {
					args = append(args, "-replace")
				}
				args = append(args, "-")
				var out, diag bytes.Buffer
				err = saveCommand(args, strings.NewReader(sectionedEnsemble), &out, &diag)
				if err == nil || !strings.Contains(out.String(), dest) {
					t.Fatal(err, out.String())
				}
				info, err := os.Lstat(dest)
				if err != nil || (kind == "dangling" && info.Mode()&os.ModeSymlink == 0) || (kind == "directory" && !info.IsDir()) {
					t.Fatal(info, err)
				}
			})
		}
	}
}

func TestSaveReportsItsOutputAndExitCodesWithoutCAORequests(t *testing.T) {
	home, wt := t.TempDir(), t.TempDir()
	t.Setenv(loop.HomeEnv, home)
	writeFile(t, filepath.Join(home, "config.toml"), "[agents]\navailable=['claude','codex','copilot','agy']\n")
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1) }))
	defer server.Close()
	t.Setenv(loop.CAOURLEnv, server.URL)
	source := filepath.Join(wt, "source.toml")
	writeFile(t, source, sectionedEnsemble)
	call := func(args ...string) (int, string) {
		t.Helper()
		var diag bytes.Buffer
		code := 0
		out, err := captureStdout(t, func() error { code = execute(append([]string{"ensemble", "save"}, args...), &diag); return nil })
		if err != nil {
			t.Fatal(err)
		}
		return code, out
	}
	dest := filepath.Join(home, "ensembles", "sample.toml")
	code, out := call("-name=sample", "-worktree="+wt, source)
	if code != 0 || out != "saved sample to "+dest+"\n" {
		t.Fatal(code, out)
	}
	code, out = call("-json", "-name=other", "-worktree="+wt, source)
	var doc saveResult
	if err := json.Unmarshal([]byte(out), &doc); err != nil || code != 0 || doc.Schema != 1 || doc.Name != "other" || doc.Path != filepath.Join(home, "ensembles", "other.toml") || doc.Replaced || !doc.Ready || doc.Reason != "" || doc.Errors == nil || len(doc.Errors) != 0 {
		t.Fatal(code, out, err)
	}
	for _, jsonMode := range []bool{false, true} {
		args := []string{"-name=sample", "-worktree=" + wt}
		if jsonMode {
			args = append(args, "-json")
		}
		args = append(args, source)
		code, out = call(args...)
		if code != 1 {
			t.Fatal(code, out)
		}
		if jsonMode {
			if err := json.Unmarshal([]byte(out), &doc); err != nil || doc.Path != "" || doc.Replaced || doc.Ready || len(doc.Errors) == 0 {
				t.Fatal(out, err)
			}
		} else if !strings.Contains(out, dest) {
			t.Fatal(out)
		}
	}
	for _, args := range [][]string{{source}, {"-name=x"}, {"-name=x", source, source}, {"-name=x", "-run=x", source}, {"-name=x", "-run="}, {"-unknown"}} {
		code, out = call(args...)
		if code != 2 {
			t.Fatal(args, code, out)
		}
	}
	if requests.Load() != 0 {
		t.Fatal(requests.Load())
	}
}

func TestSaveRefusesUnavailableNamesInTextAndJSON(t *testing.T) {
	home, wt := t.TempDir(), t.TempDir()
	t.Setenv(loop.HomeEnv, home)
	writeFile(t, filepath.Join(home, "config.toml"), "[agents]\navailable=['claude']\n")
	for _, flag := range []string{"", "-json"} {
		args := []string{"-name=sample", "-worktree=" + wt}
		if flag != "" {
			args = append(args, flag)
		}
		args = append(args, "-")
		var out, diag bytes.Buffer
		err := saveCommand(args, strings.NewReader(sectionedEnsemble), &out, &diag)
		if err == nil || !strings.Contains(out.String(), "text ensembles may only use names") {
			t.Fatal(err, out.String())
		}
	}
}

func TestSaveRequiresPortableTextAndAnExistingRepositoryWorktree(t *testing.T) {
	home, wt := t.TempDir(), t.TempDir()
	t.Setenv(loop.HomeEnv, home)
	writeFile(t, filepath.Join(home, "config.toml"), "[agents]\navailable=['claude','codex','copilot','agy']\n")
	content := sectionedEnsemble
	var out, diag bytes.Buffer
	err := saveCommand([]string{"-name=user", "-worktree=" + wt, "-"}, strings.NewReader(content), &out, &diag)
	if err != nil {
		t.Fatal(err, out.String())
	}
	out.Reset()
	err = saveCommand([]string{"-name=repo", "-repository", "-worktree=" + wt, "-"}, strings.NewReader(strings.Replace(content, `agents = ["claude"]`, `agents = ["p:planner:m"]`, 1)), &out, &diag)
	if err == nil {
		t.Fatal(out.String())
	}
	file := filepath.Join(wt, "file")
	writeFile(t, file, "x")
	for _, bad := range []string{filepath.Join(wt, "missing"), file} {
		out.Reset()
		err = saveCommand([]string{"-name=repo", "-repository", "-worktree=" + bad, "-"}, strings.NewReader(sectionedEnsemble), &out, &diag)
		if err == nil {
			t.Fatal(bad, out.String())
		}
		if _, e := os.Stat(filepath.Join(bad, ".kapellmeister", "ensembles", "repo.toml")); e == nil {
			t.Fatal(bad)
		}
	}
}

func TestSaveRefusesConfiguredNamesInTheRepositoryWithOrWithoutReplace(t *testing.T) {
	for _, replace := range []bool{false, true} {
		t.Run(fmt.Sprint(replace), func(t *testing.T) {
			home, wt := t.TempDir(), t.TempDir()
			safetyGit(t, wt, "init", "-q")
			t.Setenv(loop.HomeEnv, home)
			config := filepath.Join(home, "config.toml")
			writeFile(t, config, "[agents]\navailable=['claude','codex','copilot','agy']\n[ensembles.sample]\ngate='claude'\n")
			args := []string{"-name=sample", "-worktree=" + wt, "-repository"}
			if replace {
				args = append(args, "-replace")
			}
			args = append(args, "-")
			var out, diag bytes.Buffer
			err := saveCommand(args, strings.NewReader(sectionedEnsemble), &out, &diag)
			if err == nil || !strings.Contains(out.String(), config) || !strings.Contains(out.String(), "[ensembles.sample]") {
				t.Fatal(err, out.String())
			}
			if _, err = os.Stat(filepath.Join(wt, ".kapellmeister", "ensembles")); !os.IsNotExist(err) {
				t.Fatal(err)
			}
		})
	}
}

func TestSaveRefusesAnExplicitSpecAndWritesNothing(t *testing.T) {
	home, wt := t.TempDir(), t.TempDir()
	t.Setenv("KAPELLMEISTER_HOME", home)
	writeFile(t, filepath.Join(home, "config.toml"), "[agents]\navailable = ['claude']\n")
	content := strings.Replace(sectionedEnsemble, `agents = ["claude"]`, `agents = ["p:planner:m"]`, 1)
	var out, diag bytes.Buffer
	err := saveCommand([]string{"-name=sample", "-worktree=" + wt, "-"}, strings.NewReader(content), &out, &diag)
	if err == nil || !strings.Contains(out.String(), "text ensembles may only use names from [agents] available") {
		t.Fatalf("%v %s", err, out.String())
	}
	if _, err := os.Stat(filepath.Join(home, "ensembles", "sample.toml")); !os.IsNotExist(err) {
		t.Fatalf("saved: %v", err)
	}
}

func TestAllSaveSourcesRefuseExplicitSpecsWithoutWriting(t *testing.T) {
	for _, source := range []string{"file", "stdin", "run"} {
		for _, repo := range []bool{false, true} {
			for _, jsonOutput := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/repository=%t/json=%t", source, repo, jsonOutput), func(t *testing.T) {
					home, wt, records := t.TempDir(), t.TempDir(), t.TempDir()
					t.Setenv(loop.HomeEnv, home)
					writeFile(t, filepath.Join(home, "config.toml"), "[agents]\navailable=['claude']\n[report]\ndirs=['"+records+"']\n")
					content := strings.Replace(sectionedEnsemble, `agents = ["claude"]`, `agents = ["p:planner:m"]`, 1)
					path := filepath.Join(t.TempDir(), "input.toml")
					writeFile(t, path, content)
					reportFixture(t, records, "recorded", "", "app", "text", "approved")
					writeFile(t, filepath.Join(records, "recorded", "ensemble.toml"), content)
					args := []string{"-name=sample", "-worktree=" + wt}
					if repo {
						args = append(args, "-repository")
					}
					if jsonOutput {
						args = append(args, "-json")
					}
					switch source {
					case "file":
						args = append(args, path)
					case "stdin":
						args = append(args, "-")
					case "run":
						args = append(args, "-run=recorded")
					}
					var out, diag bytes.Buffer
					err := saveCommand(args, strings.NewReader(content), &out, &diag)
					if err == nil || !strings.Contains(out.String(), "text ensembles may only use names from [agents] available") || !strings.Contains(out.String(), "p:planner:m") {
						t.Fatal(err, out.String(), diag.String())
					}
					for _, dest := range []string{filepath.Join(home, "ensembles", "sample.toml"), filepath.Join(wt, ".kapellmeister", "ensembles", "sample.toml")} {
						if _, err := os.Stat(dest); !os.IsNotExist(err) {
							t.Fatalf("%s: %v", dest, err)
						}
					}
				})
			}
		}
	}
}
