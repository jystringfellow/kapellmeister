package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jystringfellow/kapellmeister/internal/loop"
)

func TestCheckCommandsReportLoadErrorsAsJSON(t *testing.T) {
	for _, kind := range []string{"ensemble", "score"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("KAPELLMEISTER_HOME", t.TempDir())
			path := filepath.Join(dir, "sample.toml")
			if err := os.WriteFile(path, []byte("bad = true\n"), 0600); err != nil {
				t.Fatal(err)
			}
			var diag bytes.Buffer
			code := 0
			out, err := captureStdout(t, func() error {
				code = execute([]string{kind, "check", "-json", "-worktree", dir, path}, &diag)
				return nil
			})
			if err != nil || code != 1 || !strings.Contains(out, `"ok":false`) || !strings.Contains(out, path) {
				t.Fatalf("code=%d out=%s diag=%s err=%v", code, out, diag.String(), err)
			}
		})
	}
}

func TestChecksReportReadinessFitNamesAndInputLimits(t *testing.T) {
	home := t.TempDir()
	t.Setenv("KAPELLMEISTER_HOME", home)
	wt := t.TempDir()
	writeFile(t, filepath.Join(home, "config.toml"), "[agents]\navailable = [\"claude\", \"codex\", \"copilot\", \"agy\"]\n")
	cases := []struct {
		kind, name, content string
		ok, ready           bool
		problem             string
	}{
		{"ensemble", "good", sectionedEnsemble, true, true, ""},
		{"ensemble", "unfit", strings.Replace(sectionedEnsemble, `part = "gate"`, `part = "planner"`, 1), false, false, "planner"},
		{"ensemble", "podium", sectionedEnsemble, false, false, "podium"},
		{"score", "default", "x", false, false, "reserved"},
		{"score", "Bad_Name", "x", false, false, "invalid score name"},
		{"score", "large", strings.Repeat(" ", 65537), false, false, "65536-byte limit"},
	}
	for _, tc := range cases {
		t.Run(tc.kind+tc.name, func(t *testing.T) {
			var out, diag bytes.Buffer
			err := checkCommand(tc.kind, []string{"-json", "-worktree=" + wt, "-name=" + tc.name, "-"}, strings.NewReader(tc.content), &out, &diag, "")
			var doc struct {
				OK     bool     `json:"ok"`
				Ready  bool     `json:"ready"`
				Errors []string `json:"errors"`
			}
			if e := json.Unmarshal(out.Bytes(), &doc); e != nil {
				t.Fatal(e, out.String())
			}
			if doc.OK != tc.ok || (tc.kind == "ensemble" && doc.Ready != tc.ready) || (tc.ok && err != nil) || (!tc.ok && (err == nil || !strings.Contains(strings.Join(doc.Errors, " "), tc.problem))) {
				t.Fatalf("%+v %v %s", doc, err, diag.String())
			}
		})
	}
	var out, diag bytes.Buffer
	if err := checkCommand("score", []string{"-"}, strings.NewReader(""), &out, &diag, ""); err == nil || !strings.Contains(err.Error(), "-name is required") {
		t.Fatal(err)
	}
}

func TestMcpRepertoireAndCheckReturnDocuments(t *testing.T) {
	mcpRecords(t)
	wt := t.TempDir()
	c := startMCPClient(t, false)
	r := mcpData(t, c.call("repertoire", map[string]any{"worktree": wt}))
	if r["schema"] != float64(1) {
		t.Fatal(r)
	}
	for _, kind := range []string{"score", "ensemble"} {
		result := mcpData(t, c.call("check", map[string]any{"kind": kind, "name": "sample", "content": "bad = true", "worktree": wt}))
		if result["ok"] != false || !strings.Contains(result["errors"].([]any)[0].(string), "sample.toml") {
			t.Fatal(result)
		}
	}
	if msg := mcpError(t, c.call("check", map[string]any{"kind": "score", "name": "sample", "content": strings.Repeat(" ", 65537)})); !strings.Contains(msg, "64 KiB") {
		t.Fatal(msg)
	}
}

func TestChecksUseTheSameReadinessAsRepertoireAndAcceptScores(t *testing.T) {
	home, wt := t.TempDir(), t.TempDir()
	t.Setenv("KAPELLMEISTER_HOME", home)
	writeFile(t, filepath.Join(home, "config.toml"), "[agents]\navailable = [\"claude\"]\n")
	writeFile(t, filepath.Join(home, "ensembles", "sample.toml"), sectionedEnsemble)
	var out, diag bytes.Buffer
	if err := checkCommand("ensemble", []string{"-json", "-worktree=" + wt, "-name=sample", "-"}, strings.NewReader(sectionedEnsemble), &out, &diag, ""); err != nil {
		t.Fatal(err)
	}
	var check checkResult
	if err := json.Unmarshal(out.Bytes(), &check); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	diag.Reset()
	if err := repertoireCommand([]string{"-json", "-worktree=" + wt}, &out, &diag); err != nil {
		t.Fatal(err)
	}
	var repertoire struct {
		Ensembles []repertoireEnsemble `json:"ensembles"`
	}
	if err := json.Unmarshal(out.Bytes(), &repertoire); err != nil {
		t.Fatal(err)
	}
	if check.Ready == nil || *check.Ready || check.Reason == "" || len(repertoire.Ensembles) != 1 || check.Reason != repertoire.Ensembles[0].Reason {
		t.Fatalf("%+v %+v", check, repertoire)
	}
	out.Reset()
	diag.Reset()
	if err := checkCommand("score", []string{"-json", "-worktree=" + wt, "-name=copy", "-"}, strings.NewReader(loop.DefaultScore().File()), &out, &diag, ""); err != nil {
		t.Fatal(err, out.String())
	}
	var doc map[string]any
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil || doc["ok"] != true || doc["ready"] != nil || len(doc["errors"].([]any)) != 0 {
		t.Fatal(doc, err)
	}
}

func TestDefaultEnsembleCheckDoesNotReadRepositoryScores(t *testing.T) {
	t.Setenv("KAPELLMEISTER_HOME", t.TempDir())
	var out, diag bytes.Buffer
	err := checkCommand("ensemble", []string{"-name=sample", "-worktree=" + t.TempDir(), "-"}, strings.NewReader(sectionedEnsemble), &out, &diag, "")
	if err != nil || diag.Len() != 0 {
		t.Fatalf("%v: %s", err, diag.String())
	}
}

func TestCheckCommandsReportTextJSONNamesAndUsage(t *testing.T) {
	home, wt := t.TempDir(), t.TempDir()
	t.Setenv(loop.HomeEnv, home)
	writeFile(t, filepath.Join(home, "config.toml"), "[agents]\navailable = [\"claude\", \"codex\", \"copilot\", \"agy\"]\n")
	for _, kind := range []string{"ensemble", "score"} {
		content := sectionedEnsemble
		if kind == "score" {
			content = loop.DefaultScore().File()
		}
		path := filepath.Join(wt, "sample.toml")
		writeFile(t, path, content)
		for _, jsonMode := range []bool{false, true} {
			args := []string{"-worktree=" + wt}
			if jsonMode {
				args = append(args, "-json")
			}
			args = append(args, path)
			var out, diag bytes.Buffer
			if err := checkCommand(kind, args, strings.NewReader(""), &out, &diag, ""); err != nil {
				t.Fatal(kind, err, out.String())
			}
			if jsonMode {
				var doc map[string]any
				if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
					t.Fatal(err)
				}
				if doc["schema"] != float64(1) || doc["kind"] != kind || doc["name"] != "sample" || doc["ok"] != true || doc["reason"] != "" || len(doc["errors"].([]any)) != 0 {
					t.Fatal(doc)
				}
				_, hasReady := doc["ready"]
				if hasReady != (kind == "ensemble") {
					t.Fatal(doc)
				}
			} else if out.String() != "ok\n" {
				t.Fatal(out.String())
			}
			for _, stdinName := range []string{"", "-name=override"} {
				a := []string{"-worktree=" + wt}
				if jsonMode {
					a = append(a, "-json")
				}
				if stdinName != "" {
					a = append(a, stdinName)
				}
				a = append(a, "-")
				out.Reset()
				diag.Reset()
				err := checkCommand(kind, a, strings.NewReader(content), &out, &diag, "")
				if stdinName == "" {
					var status *exitStatusError
					if !errors.As(err, &status) || status.code != 2 || !strings.Contains(err.Error(), "-name is required") {
						t.Fatal(err)
					}
				} else if err != nil {
					t.Fatal(err, out.String())
				}
			}
			out.Reset()
			diag.Reset()
			a := []string{"-name=override"}
			if jsonMode {
				a = append(a, "-json")
			}
			a = append(a, "-")
			err := checkCommand(kind, a, strings.NewReader("bad = true"), &out, &diag, "")
			var status *exitStatusError
			if !errors.As(err, &status) || status.code != 1 || !strings.Contains(out.String(), "stdin") {
				t.Fatal(err, out.String())
			}
		}
		var out, diag bytes.Buffer
		for _, args := range [][]string{{}, {path, path}} {
			var status *exitStatusError
			if err := checkCommand(kind, args, strings.NewReader(""), &out, &diag, ""); !errors.As(err, &status) || status.code != 2 {
				t.Fatal(err)
			}
		}
		for _, name := range []string{"podium", "Bad_name"} {
			out.Reset()
			if err := checkCommand(kind, []string{"-json", "-name=" + name, "-"}, strings.NewReader(content), &out, &diag, ""); err == nil || !strings.Contains(out.String(), name) {
				t.Fatal(name, err, out.String())
			}
		}
		if kind == "score" {
			for _, name := range []string{"default", "review"} {
				out.Reset()
				if err := checkCommand(kind, []string{"-json", "-name=" + name, "-"}, strings.NewReader(content), &out, &diag, ""); err == nil || !strings.Contains(out.String(), "reserved") {
					t.Fatal(name, err, out.String())
				}
			}
		}
	}
}

func TestScoreChecksResolveUserAndCommittedRepositoryPartsAndRefuseMissingParts(t *testing.T) {
	home, wt := t.TempDir(), t.TempDir()
	t.Setenv(loop.HomeEnv, home)
	safetyGit(t, wt, "init", "-q")
	writeFile(t, filepath.Join(wt, ".kapellmeister/parts/drafter.md"), "Draft {reply}")
	writeFile(t, filepath.Join(wt, ".kapellmeister/parts/judge.md"), "Judge {reply}")
	safetyGit(t, wt, "add", "-f", ".kapellmeister/parts")
	safetyGit(t, wt, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-qm", "parts")
	for _, tc := range []struct {
		name    string
		config  string
		user    bool
		ok      bool
		problem string
	}{
		{"repository", "", false, true, ""}, {"refused", "[run]\nrepository_scores = false\n", false, false, "repository"}, {"user", "[run]\nrepository_scores = false\n", true, true, ""}, {"missing", "", false, false, "unknown part"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			writeFile(t, filepath.Join(home, "config.toml"), tc.config)
			if tc.user {
				writeFile(t, filepath.Join(home, "parts/drafter.md"), "Draft {reply}")
				writeFile(t, filepath.Join(home, "parts/judge.md"), "Judge {reply}")
			}
			text := repositorySmallScore
			if tc.name == "missing" {
				text = strings.ReplaceAll(text, `part = "drafter"`, `part = "missing"`)
			}
			for _, jsonMode := range []bool{false, true} {
				args := []string{"-worktree=" + wt, "-name=sample"}
				if jsonMode {
					args = append(args, "-json")
				}
				args = append(args, "-")
				var out, diag bytes.Buffer
				err := checkCommand("score", args, strings.NewReader(text), &out, &diag, "")
				if (err == nil) != tc.ok || (!tc.ok && (!strings.Contains(out.String(), tc.problem) || !strings.Contains(out.String(), "stdin"))) {
					t.Fatal(err, out.String(), diag.String())
				}
				if tc.ok && !jsonMode && out.String() != "ok\n" {
					t.Fatal(out.String())
				}
			}
			if tc.user {
				os.RemoveAll(filepath.Join(home, "parts"))
			}
		})
	}
}

func TestCheckInputSizeBoundariesAndUnavailableMisfit(t *testing.T) {
	t.Setenv(loop.HomeEnv, t.TempDir())
	wt := t.TempDir()
	for _, kind := range []string{"ensemble", "score"} {
		content := sectionedEnsemble
		if kind == "score" {
			content = loop.DefaultScore().File()
		}
		content += "\n#" + strings.Repeat("x", 65536-len(content)-2)
		for _, n := range []int{65536, 65537} {
			text := content
			if n > 65536 {
				text += "x"
			}
			var out, diag bytes.Buffer
			err := checkCommand(kind, []string{"-json", "-worktree=" + wt, "-name=sample", "-"}, strings.NewReader(text), &out, &diag, "")
			if (err == nil) != (n == 65536) {
				t.Fatal(kind, n, err, out.String())
			}
		}
	}
	text := strings.Replace(sectionedEnsemble, `part = "gate"`, `part = "planner"`, 1)
	var out, diag bytes.Buffer
	err := checkCommand("ensemble", []string{"-worktree=" + wt, "-name=sample", "-"}, strings.NewReader(text), &out, &diag, "")
	if err == nil || !strings.Contains(out.String(), "planner") || strings.Contains(out.String(), "not ready") {
		t.Fatal(err, out.String())
	}
}

func TestChecksAndMcpChecksMakeNoRequestsAndWriteNoFiles(t *testing.T) {
	home, wt := t.TempDir(), t.TempDir()
	t.Setenv(loop.HomeEnv, home)
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); http.Error(w, "unexpected", 500) }))
	defer server.Close()
	t.Setenv(loop.CAOURLEnv, server.URL)
	writeFile(t, filepath.Join(home, "config.toml"), "[agents]\navailable = [\"claude\", \"codex\", \"copilot\", \"agy\"]\n")
	snapshot := func() map[string]string {
		result := map[string]string{}
		for _, root := range []string{home, wt} {
			err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if d.IsDir() {
					result[path] = "dir"
					return nil
				}
				b, e := os.ReadFile(path)
				if e != nil {
					return e
				}
				result[path] = string(b)
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
		}
		return result
	}
	before := snapshot()
	c := startMCPClient(t, false)
	for _, kind := range []string{"ensemble", "score"} {
		text := sectionedEnsemble
		if kind == "score" {
			text = loop.DefaultScore().File()
		}
		var out, diag bytes.Buffer
		if err := checkCommand(kind, []string{"-worktree=" + wt, "-name=sample", "-"}, strings.NewReader(text), &out, &diag, ""); err != nil {
			t.Fatal(err)
		}
		d := mcpData(t, c.call("check", map[string]any{"kind": kind, "name": "sample", "content": text, "worktree": wt}))
		if d["ok"] != true {
			t.Fatal(d)
		}
	}
	if requests.Load() != 0 || !reflect.DeepEqual(before, snapshot()) {
		t.Fatalf("requests=%d, before=%v after=%v", requests.Load(), before, snapshot())
	}
}

func TestMcpChecksValidateContentAndRepertoireMatchesCommands(t *testing.T) {
	home, wt := t.TempDir(), t.TempDir()
	t.Setenv(loop.HomeEnv, home)
	writeFile(t, filepath.Join(home, "config.toml"), "[agents]\navailable = [\"claude\", \"codex\", \"copilot\", \"agy\"]\n")
	c := startMCPClient(t, false)
	for _, worktree := range []string{"", wt} {
		args := []string{"-json"}
		call := map[string]any{}
		if worktree != "" {
			args = append(args, "-worktree="+worktree)
			call["worktree"] = worktree
		}
		var out, diag bytes.Buffer
		if err := repertoireCommand(args, &out, &diag); err != nil {
			t.Fatal(err)
		}
		want, err := mcpDocument(out.Bytes(), diag.String(), mcpResultLimit, "")
		if err != nil {
			t.Fatal(err)
		}
		got := mcpData(t, c.call("repertoire", call))
		encoded, _ := json.Marshal(want)
		var normalized map[string]any
		json.Unmarshal(encoded, &normalized)
		if !reflect.DeepEqual(normalized, got) {
			t.Fatalf("%v != %v", want, got)
		}
	}
	for _, kind := range []string{"ensemble", "score"} {
		text := sectionedEnsemble
		if kind == "score" {
			text = loop.DefaultScore().File()
		}
		padded := text + "\n#" + strings.Repeat("x", 65536-len(text)-2)
		for _, content := range []string{text, padded} {
			got := mcpData(t, c.call("check", map[string]any{"kind": kind, "name": "sample", "content": content, "worktree": wt}))
			if got["ok"] != true {
				t.Fatal(got)
			}
		}
		if msg := mcpError(t, c.call("check", map[string]any{"kind": kind, "name": "sample", "content": padded + "x"})); !strings.Contains(msg, "64 KiB") {
			t.Fatal(msg)
		}
	}
	for _, args := range []map[string]any{{"kind": "bad", "name": "x", "content": "x"}, {"kind": "score", "content": "x"}, {"kind": "score", "name": "", "content": "x"}, {"kind": "score", "name": "x"}, {"kind": "score", "name": "x", "content": ""}, {"kind": "score", "name": "x", "content": "x", "extra": true}} {
		mcpError(t, c.call("check", args))
	}
	writeFile(t, filepath.Join(home, "config.toml"), "[agents]\navailable = [\"claude\"]\n")
	got := mcpData(t, c.call("check", map[string]any{"kind": "ensemble", "name": "sample", "content": sectionedEnsemble, "worktree": wt}))
	if got["ok"] != false || !strings.Contains(got["errors"].([]any)[0].(string), "text ensembles may only use names") {
		t.Fatal(got)
	}
}

func TestEnsembleCheckUsesItsNamedScoreBeforeReportingReadiness(t *testing.T) {
	home, wt := t.TempDir(), t.TempDir()
	t.Setenv(loop.HomeEnv, home)
	writeFile(t, filepath.Join(home, "scores/small.toml"), repositorySmallScore)
	writeFile(t, filepath.Join(home, "parts/drafter.md"), "Draft {reply}")
	writeFile(t, filepath.Join(home, "parts/judge.md"), "Judge {reply}")
	for _, jsonMode := range []bool{false, true} {
		args := []string{"-worktree=" + wt, "-name=sample"}
		if jsonMode {
			args = append(args, "-json")
		}
		args = append(args, "-")
		var out, diag bytes.Buffer
		if err := checkCommand("ensemble", args, strings.NewReader(smallScoreEnsemble), &out, &diag, ""); err != nil {
			t.Fatal(err, out.String())
		}
		if jsonMode {
			var doc checkResult
			if err := json.Unmarshal(out.Bytes(), &doc); err != nil || !doc.OK || doc.Ready == nil || *doc.Ready || doc.Reason == "" {
				t.Fatal(doc, err)
			}
		} else if !strings.HasPrefix(out.String(), "ok, not ready: ") {
			t.Fatal(out.String())
		}
		out.Reset()
		diag.Reset()
		text := strings.Replace(smallScoreEnsemble, `part = "judge"`, `part = "drafter"`, 1)
		if err := checkCommand("ensemble", args, strings.NewReader(text), &out, &diag, ""); err == nil || !strings.Contains(out.String(), "drafter") || strings.Contains(out.String(), "not ready") {
			t.Fatal(err, out.String())
		}
	}
}

func TestCheckCommandsExitWithTheirDocumentedStatusesAndHonorNameOverrides(t *testing.T) {
	home, wt := t.TempDir(), t.TempDir()
	t.Setenv(loop.HomeEnv, home)
	writeFile(t, filepath.Join(home, "config.toml"), "[agents]\navailable = [\"claude\", \"codex\", \"copilot\", \"agy\"]\n")
	for _, kind := range []string{"ensemble", "score"} {
		text := sectionedEnsemble
		if kind == "score" {
			text = loop.DefaultScore().File()
		}
		path := filepath.Join(wt, "sample.toml")
		writeFile(t, path, text)
		for _, tc := range []struct {
			flags    []string
			code     int
			contains string
		}{
			{[]string{"-worktree=" + wt, path}, 0, "ok\n"},
			{[]string{"-worktree=" + wt, "-json", "-name=chosen", path}, 0, `"name":"chosen"`},
			{[]string{"-worktree=" + wt, "-name=podium", path}, 1, path},
			{[]string{"-worktree=" + wt}, 2, ""},
		} {
			var diag bytes.Buffer
			code := 0
			out, err := captureStdout(t, func() error { code = execute(append([]string{kind, "check"}, tc.flags...), &diag); return nil })
			if err != nil || code != tc.code || !strings.Contains(out, tc.contains) {
				t.Fatal(kind, code, out, diag.String(), err)
			}
		}
	}
}

func TestEnsembleCheckAppliesTheTextRuleOnlyWithText(t *testing.T) {
	home, wt := t.TempDir(), t.TempDir()
	t.Setenv("KAPELLMEISTER_HOME", home)
	writeFile(t, filepath.Join(home, "config.toml"), "[agents]\navailable = [\"claude\"]\n")
	content := strings.Replace(sectionedEnsemble, `agents = ["claude"]`, `agents = ["p:planner:m"]`, 1)
	for _, held := range []bool{false, true} {
		args := []string{"-json", "-name=sample", "-worktree=" + wt}
		if held {
			args = append(args, "-text")
		}
		args = append(args, "-")
		var out, diag bytes.Buffer
		err := checkCommand("ensemble", args, strings.NewReader(content), &out, &diag, "")
		if !held && err != nil {
			t.Fatalf("ordinary check: %v %s", err, out.String())
		}
		if held && (err == nil || !strings.Contains(out.String(), "text ensembles may only use names from [agents] available") || !strings.Contains(out.String(), "p:planner:m")) {
			t.Fatalf("text check: %v %s %s", err, out.String(), diag.String())
		}
	}
}

func TestScoreCheckDoesNotAcceptTextFlag(t *testing.T) {
	var out, diag bytes.Buffer
	err := checkCommand("score", []string{"-text", "-name=sample", "-"}, strings.NewReader(""), &out, &diag, "")
	var status *exitStatusError
	if !errors.As(err, &status) || status.code != 2 {
		t.Fatal(err)
	}
}

func TestTextEnsembleAcceptsAnAvailableNameWithATierPinAndAChairTier(t *testing.T) {
	home, wt := t.TempDir(), t.TempDir()
	t.Setenv(loop.HomeEnv, home)
	writeFile(t, filepath.Join(home, "config.toml"), "[agents]\navailable=['claude']\n")
	content := strings.ReplaceAll(sectionedEnsemble, `agents = ["codex", "claude"]`, `agents = ["claude"]`)
	content = strings.ReplaceAll(content, `agents = ["copilot"]`, `agents = ["claude"]`)
	content = strings.ReplaceAll(content, `agents = ["agy", "claude@large"]`, `agents = ["claude@large"]`)
	content = strings.Replace(content, `part = "planner"`, "part = \"planner\"\ntier = \"large\"", 1)
	var out, diag bytes.Buffer
	if err := checkCommand("ensemble", []string{"-text", "-name=sample", "-worktree=" + wt, "-"}, strings.NewReader(content), &out, &diag, ""); err != nil {
		t.Fatal(err, out.String(), diag.String())
	}
}
