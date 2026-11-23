package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestMcpSaveKeepsContentWhenAsked(t *testing.T) {
	home, wt := t.TempDir(), t.TempDir()
	t.Setenv("KAPELLMEISTER_HOME", home)
	writeFile(t, filepath.Join(home, "config.toml"), "[agents]\navailable = [\"claude\", \"codex\", \"copilot\", \"agy\"]\n")
	c := startMCPClient(t, false)
	result := mcpData(t, c.call("save", map[string]any{"name": "sample", "content": sectionedEnsemble, "worktree": wt}))
	if result["ready"] != true || result["path"] != filepath.Join(home, "ensembles", "sample.toml") {
		t.Fatal(result)
	}
}

func TestMcpSaveRejectsInvalidSourcesAndOversizedContent(t *testing.T) {
	c := startMCPClient(t, false)
	for _, tc := range []struct {
		args map[string]any
		want string
	}{
		{map[string]any{"name": "x"}, "exactly one"},
		{map[string]any{"name": "x", "content": "x", "run": "xx"}, "exactly one"},
		{map[string]any{"name": "x", "content": strings.Repeat("x", 65537)}, "64 KiB"},
	} {
		if msg := mcpError(t, c.call("save", tc.args)); !strings.Contains(msg, tc.want) {
			t.Fatal(msg)
		}
	}
}

func TestMcpSaveUsesRunRecordsRepositoryLocationAndReplace(t *testing.T) {
	home, wt, records := t.TempDir(), t.TempDir(), t.TempDir()
	safetyGit(t, wt, "init", "-q")
	t.Setenv("KAPELLMEISTER_HOME", home)
	writeFile(t, filepath.Join(home, "config.toml"), "[agents]\navailable=['claude','codex','copilot','agy']\n[report]\ndirs=['"+records+"']\n")
	reportFixture(t, records, "recorded", "", "app", "text", "approved")
	content := strings.TrimSuffix(sectionedEnsemble, "\n")
	writeFile(t, filepath.Join(records, "recorded", "ensemble.toml"), content)
	c := startMCPClient(t, false)
	args := map[string]any{"name": "sample", "run": "reco", "location": "repository", "worktree": wt}
	got := mcpData(t, c.call("save", args))
	path := filepath.Join(wt, ".kapellmeister", "ensembles", "sample.toml")
	data, err := os.ReadFile(path)
	if err != nil || string(data) != content || got["path"] != path || got["replaced"] != false || got["ready"] != true {
		t.Fatal(got, err)
	}
	var out, diag bytes.Buffer
	err = saveCommand([]string{"-json", "-name=sample", "-worktree=" + wt, "-repository", "-run=reco"}, nil, &out, &diag)
	var cli map[string]any
	if err == nil || json.Unmarshal(out.Bytes(), &cli) != nil {
		t.Fatal(err, out.String())
	}
	refused := mcpData(t, c.call("save", args))
	if !reflect.DeepEqual(cli, refused) || len(refused["errors"].([]any)) == 0 {
		t.Fatal(cli, refused)
	}
	data, err = os.ReadFile(path)
	if err != nil || string(data) != content {
		t.Fatal(err)
	}
	replacement := sectionedEnsemble + "\n"
	args = map[string]any{"name": "sample", "content": replacement, "location": "repository", "worktree": wt, "replace": true}
	got = mcpData(t, c.call("save", args))
	data, err = os.ReadFile(path)
	if err != nil || string(data) != replacement || got["replaced"] != true || len(got["errors"].([]any)) != 0 {
		t.Fatal(got, err)
	}
	out.Reset()
	err = saveCommand([]string{"-json", "-name=sample", "-worktree=" + wt, "-repository", "-replace", "-"}, strings.NewReader(replacement), &out, &diag)
	if err != nil || json.Unmarshal(out.Bytes(), &cli) != nil || !reflect.DeepEqual(cli, got) {
		t.Fatal(err, cli, got)
	}
	msg := mcpError(t, c.call("save", map[string]any{"name": "x", "content": sectionedEnsemble, "location": "elsewhere"}))
	if !strings.Contains(msg, "location must be user or repository") {
		t.Fatal(msg)
	}
}

func TestMcpSaveDescriptionSaysWhenItWritesAFile(t *testing.T) {
	for _, tool := range mcpTools() {
		if tool.Name == "save" {
			if !strings.Contains(tool.Description, "Writes a file") || !strings.Contains(tool.Description, "user asks to keep an ensemble") {
				t.Fatal(tool.Description)
			}
			return
		}
	}
	t.Fatal("save tool missing")
}

func TestMcpSaveRefusesAnExplicitSpecWithoutWriting(t *testing.T) {
	home, wt := t.TempDir(), t.TempDir()
	t.Setenv("KAPELLMEISTER_HOME", home)
	writeFile(t, filepath.Join(home, "config.toml"), "[agents]\navailable=['claude']\n")
	content := strings.Replace(sectionedEnsemble, `agents = ["claude"]`, `agents = ["p:planner:m"]`, 1)
	c := startMCPClient(t, false)
	for _, location := range []string{"user", "repository"} {
		got := mcpData(t, c.call("save", map[string]any{"name": "sample", "content": content, "worktree": wt, "location": location}))
		if !strings.Contains(got["errors"].([]any)[0].(string), "text ensembles may only use names from [agents] available") {
			t.Fatal(got)
		}
	}
	for _, dest := range []string{filepath.Join(home, "ensembles", "sample.toml"), filepath.Join(wt, ".kapellmeister", "ensembles", "sample.toml")} {
		if _, err := os.Stat(dest); !os.IsNotExist(err) {
			t.Fatal(dest, err)
		}
	}
}
