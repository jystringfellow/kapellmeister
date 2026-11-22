package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jystringfellow/kapellmeister/internal/loop"
)

func TestRepertoireReportsDanglingEnsembleSymlinksWithTheirSourceAndLoadError(t *testing.T) {
	for _, source := range []string{"repository", "user", "user-shadowing-repository"} {
		t.Run(source, func(t *testing.T) {
			home, wt := t.TempDir(), t.TempDir()
			t.Setenv(loop.HomeEnv, home)
			writeFile(t, filepath.Join(home, "config.toml"), "[agents]\navailable = [\"claude\", \"codex\", \"copilot\"]\n")
			writeFile(t, filepath.Join(wt, ".kapellmeister/ensembles/ready.toml"), sectionedEnsemble)
			dir, wantSource := filepath.Join(wt, ".kapellmeister/ensembles"), "repository"
			if source != "repository" {
				dir, wantSource = filepath.Join(home, "ensembles"), "user"
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			if source == "user-shadowing-repository" {
				writeFile(t, filepath.Join(wt, ".kapellmeister/ensembles/dangle.toml"), sectionedEnsemble)
			}
			path := filepath.Join(dir, "dangle.toml")
			if err := os.Symlink("missing.toml", path); err != nil {
				t.Fatal(err)
			}
			var out, diag bytes.Buffer
			if err := repertoireCommand([]string{"-worktree", wt, "-json"}, &out, &diag); err != nil {
				t.Fatal(err)
			}
			var got struct {
				Ensembles []repertoireEnsemble `json:"ensembles"`
			}
			if err := json.Unmarshal(out.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if len(got.Ensembles) != 2 || got.Ensembles[0].Name != "dangle" || got.Ensembles[1].Name != "ready" || !got.Ensembles[1].Ready {
				t.Fatalf("ensembles: %+v", got.Ensembles)
			}
			row := got.Ensembles[0]
			if row.Source != wantSource || row.Ready || row.Chairs != 0 || !strings.Contains(row.Reason, path) || !strings.Contains(row.Reason, "no such file or directory") {
				t.Errorf("dangling symlink row: %+v", row)
			}
			out.Reset()
			if err := repertoireCommand([]string{"-worktree", wt}, &out, &diag); err != nil {
				t.Fatal(err)
			}
			var line string
			for _, candidate := range strings.Split(out.String(), "\n") {
				if fields := strings.Fields(candidate); len(fields) > 0 && fields[0] == "dangle" {
					line = candidate
				}
			}
			if fields := strings.Fields(line); len(fields) < 2 || fields[1] != wantSource || !strings.Contains(line, "no (") || !strings.Contains(line, path) || !strings.Contains(line, "no such file or directory") {
				t.Errorf("dangling symlink text row: %q", line)
			}
		})
	}
}

func TestRepertoireListsBuiltInScoresWithoutContactingCAO(t *testing.T) {
	t.Setenv(loop.HomeEnv, t.TempDir())
	var diag bytes.Buffer
	var code int
	out, err := captureStdout(t, func() error {
		code = execute([]string{"repertoire", "-worktree", t.TempDir(), "-json"}, &diag)
		return nil
	})
	if err != nil || code != 0 {
		t.Fatalf("code %d: %v %s", code, err, diag.String())
	}
	var data struct {
		Schema int `json:"schema"`
		Scores []struct {
			Name        string `json:"name"`
			Description string `json:"description"`
		} `json:"scores"`
	}
	if err := json.Unmarshal([]byte(out), &data); err != nil || data.Schema != 1 || len(data.Scores) < 2 || data.Scores[0].Description == "" {
		t.Fatalf("output %s: %v", out, err)
	}
}

func TestRepertoireReportsSourcesReadinessAndErrorsWithoutCAORequests(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1) }))
	defer server.Close()
	home, wt := t.TempDir(), t.TempDir()
	t.Setenv(loop.HomeEnv, home)
	t.Setenv(loop.CAOURLEnv, server.URL)
	writeFile(t, filepath.Join(home, "config.toml"), "[agents]\navailable = [\"claude\", \"codex\", \"copilot\"]\n[ensembles.quartet]\nplanner = \"claude\"\nimplementer = \"codex\"\nreviewer = \"copilot\"\ngate = \"claude\"\nclassifier = \"claude\"\n")
	safetyGit(t, wt, "init", "-q")
	writeFile(t, filepath.Join(wt, ".kapellmeister/scores/shadow.toml"), "bad = true\n")
	writeFile(t, filepath.Join(wt, ".kapellmeister/scores/custom.toml"), "description = \"Custom\"\n[[steps]]\nname = \"draft\"\nkind = \"produce\"\npart = \"planner\"\nwrites = \"reply\"\nnext = { done = \"approved\" }\n")
	safetyGit(t, wt, "add", ".")
	safetyGit(t, wt, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-qm", "fixture")
	ready := "description = \"Ready\"\n" + sectionedEnsemble
	writeFile(t, filepath.Join(wt, ".kapellmeister/ensembles/ready.toml"), ready)
	writeFile(t, filepath.Join(wt, ".kapellmeister/ensembles/shadow.toml"), "bad = true\n")
	writeFile(t, filepath.Join(home, "ensembles/shadow.toml"), ready)
	writeFile(t, filepath.Join(wt, ".kapellmeister/ensembles/unavailable.toml"), strings.Replace(ready, `["agy", "claude@large"]`, `["agy"]`, 1))
	writeFile(t, filepath.Join(wt, ".kapellmeister/ensembles/invalid.toml"), "bad = true\n")
	writeFile(t, filepath.Join(home, "scores/broken.toml"), "bad = true\n")
	writeFile(t, filepath.Join(home, "scores/shadow.toml"), "[[steps]]\nname = \"first\"\nkind = \"produce\"\npart = \"planner\"\nwrites = \"reply\"\nnext = { done = \"second\" }\n[[steps]]\nname = \"second\"\nkind = \"produce\"\npart = \"planner\"\nwrites = \"reply\"\nnext = { done = \"approved\" }\n")
	var diag bytes.Buffer
	var code int
	out, _ := captureStdout(t, func() error { code = execute([]string{"repertoire", "-worktree", wt, "-json"}, &diag); return nil })
	if code != 0 {
		t.Fatalf("%d %s", code, diag.String())
	}
	var got struct {
		Ensembles []repertoireEnsemble `json:"ensembles"`
		Scores    []repertoireScore    `json:"scores"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	rows := map[string]repertoireEnsemble{}
	for _, row := range got.Ensembles {
		if _, exists := rows[row.Name]; exists {
			t.Fatalf("duplicate ensemble %s", row.Name)
		}
		rows[row.Name] = row
	}
	if !rows["ready"].Ready || rows["ready"].Chairs != 6 || rows["ready"].Description != "Ready" || rows["ready"].Source != "repository" || !rows["quartet"].Ready || rows["quartet"].Chairs != 5 || rows["shadow"].Source != "user" || rows["unavailable"].Reason != "review.style: none of agy is available" || rows["invalid"].Reason == "" {
		t.Fatalf("rows: %+v", rows)
	}
	scores := map[string]repertoireScore{}
	for _, row := range got.Scores {
		if _, exists := scores[row.Name]; exists {
			t.Fatalf("duplicate score %s", row.Name)
		}
		scores[row.Name] = row
	}
	if rows["quartet"].Source != "config" || rows["quartet"].Score != "default" || rows["ready"].Score != "default" || scores["default"].Source != "built-in" || scores["review"].Source != "built-in" || scores["shadow"].Source != "user" || !reflect.DeepEqual(scores["shadow"].Steps, []string{"first", "second"}) {
		t.Fatalf("rows: %+v scores: %+v", rows, scores)
	}
	if scores["custom"].Source != "repository" || scores["custom"].Description != "Custom" || len(scores["custom"].Steps) != 1 || scores["broken"].Error == "" || scores["review"].Description == "" {
		t.Fatalf("scores: %+v", scores)
	}
	var enames, snames []string
	for _, r := range got.Ensembles {
		enames = append(enames, r.Name)
	}
	for _, r := range got.Scores {
		snames = append(snames, r.Name)
	}
	if !sort.StringsAreSorted(enames) || !sort.StringsAreSorted(snames) {
		t.Fatalf("unsorted %v %v", enames, snames)
	}
	var raw struct {
		Ensembles []map[string]any `json:"ensembles"`
		Scores    []map[string]any `json:"scores"`
	}
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		t.Fatal(err)
	}
	for _, row := range raw.Ensembles {
		if row["name"] == "ready" {
			if _, ok := row["reason"]; ok {
				t.Fatal(row)
			}
		}
		if row["name"] == "quartet" {
			if _, ok := row["description"]; ok {
				t.Fatal(row)
			}
		}
	}
	text, _ := captureStdout(t, func() error { code = execute([]string{"repertoire", "-worktree", wt}, &diag); return nil })
	for _, parts := range [][]string{{"ready", "repository", "yes", "Ready"}, {"shadow", "user", "yes"}, {"quartet", "config", "yes"}, {"invalid", "repository", "no ("}, {"default", "built-in", "plan"}, {"custom", "repository", "Custom"}, {"broken", "user", "error:"}} {
		found := false
		for _, line := range strings.Split(text, "\n") {
			match := true
			for _, part := range parts {
				if !strings.Contains(line, part) {
					match = false
				}
			}
			if match {
				found = true
			}
		}
		if !found {
			t.Fatalf("missing %v in %s", parts, text)
		}
	}
	if code != 0 || !strings.Contains(text, "CHAIRS") || !strings.Contains(text, "STEPS") || !strings.Contains(text, "none of agy is available") || requests.Load() != 0 {
		t.Fatalf("text %s requests %d", text, requests.Load())
	}
}

func TestScoreShowPrintsDescriptionsThatLoadFromCopiedDefault(t *testing.T) {
	for _, name := range []string{"default", "review"} {
		var diag bytes.Buffer
		var code int
		out, _ := captureStdout(t, func() error { code = execute([]string{"score", "show", name}, &diag); return nil })
		if code != 0 || !strings.Contains(out, "description = ") {
			t.Fatalf("%s: %s %s", name, out, diag.String())
		}
		if name == "default" {
			home := t.TempDir()
			writeFile(t, filepath.Join(home, "scores/copy.toml"), out)
			got, err := loop.LoadScore("copy", loop.ScoreSource{Home: home})
			if err != nil || got.Score.Description != loop.DefaultScore().Description {
				t.Fatalf("%+v %v", got, err)
			}
		}
	}
}

func TestRepertoireRefusesRepositoryScoresAndReportsEnsembleFit(t *testing.T) {
	home, wt := t.TempDir(), t.TempDir()
	t.Setenv(loop.HomeEnv, home)
	writeFile(t, filepath.Join(home, "config.toml"), "[agents]\navailable = [\"claude\", \"codex\", \"copilot\"]\n[run]\nrepository_scores = false\n")
	safetyGit(t, wt, "init", "-q")
	writeFile(t, filepath.Join(wt, ".kapellmeister/scores/custom.toml"), "broken")
	safetyGit(t, wt, "add", ".")
	safetyGit(t, wt, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-qm", "fixture")
	writeFile(t, filepath.Join(wt, ".kapellmeister/ensembles/refused.toml"), "score = \"custom\"\n"+sectionedEnsemble)
	writeFile(t, filepath.Join(wt, ".kapellmeister/ensembles/unfit.toml"), strings.Replace(sectionedEnsemble, "part = \"gate\"", "part = \"planner\"", 1))
	var diag bytes.Buffer
	var code int
	out, _ := captureStdout(t, func() error { code = execute([]string{"repertoire", "-worktree", wt, "-json"}, &diag); return nil })
	if code != 0 {
		t.Fatal(diag.String())
	}
	var got struct {
		Ensembles []repertoireEnsemble `json:"ensembles"`
		Scores    []repertoireScore    `json:"scores"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	rows := map[string]repertoireEnsemble{}
	for _, r := range got.Ensembles {
		rows[r.Name] = r
	}
	scores := map[string]repertoireScore{}
	for _, s := range got.Scores {
		scores[s.Name] = s
	}
	for _, name := range []string{"refused", "unfit"} {
		r, ok := rows[name]
		if !ok || r.Ready {
			t.Fatalf("missing or ready: %s %+v", name, r)
		}
	}
	if !strings.Contains(rows["refused"].Reason, "repository_scores is false") || !strings.Contains(rows["unfit"].Reason, "planner") {
		t.Fatal(rows)
	}
	score, ok := scores["custom"]
	if !ok || score.Source != "repository" || len(score.Steps) != 0 || !strings.Contains(score.Error, "repository_scores is false") {
		t.Fatal(scores)
	}
	text, _ := captureStdout(t, func() error { code = execute([]string{"repertoire", "-worktree", wt}, &diag); return nil })
	for _, part := range []string{"refused", "unfit", "repository_scores is false", "error:"} {
		if !strings.Contains(text, part) {
			t.Fatalf("missing %s in %s", part, text)
		}
	}
}

func TestRepertoireReportsUnavailableAndConflictingConfigEnsembles(t *testing.T) {
	home, wt := t.TempDir(), t.TempDir()
	t.Setenv(loop.HomeEnv, home)
	writeFile(t, filepath.Join(home, "config.toml"), "[agents]\navailable = [\"claude\"]\n[ensembles.quartet]\nplanner = \"claude\"\nimplementer = \"claude\"\nreviewer = \"agy\"\ngate = \"claude\"\nclassifier = \"claude\"\n[ensembles.conflict]\nplanner = \"claude\"\n")
	writeFile(t, filepath.Join(wt, ".kapellmeister/ensembles/conflict.toml"), sectionedEnsemble)
	var diag bytes.Buffer
	var code int
	out, _ := captureStdout(t, func() error { code = execute([]string{"repertoire", "-worktree", wt, "-json"}, &diag); return nil })
	if code != 0 {
		t.Fatal(diag.String())
	}
	var got struct {
		Ensembles []repertoireEnsemble `json:"ensembles"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	conflicts := 0
	found := false
	for _, row := range got.Ensembles {
		if row.Name == "quartet" {
			found = true
			if row.Ready || row.Reason != "reviewer: none of agy is available" {
				t.Fatal(row)
			}
		}
		if row.Name == "conflict" {
			conflicts++
			if row.Ready || !strings.Contains(row.Reason, "defined both") {
				t.Fatal(row)
			}
		}
	}
	if !found || conflicts != 2 {
		t.Fatal(got)
	}
}

func TestRepertoireListsUserScoresAndEmptyEnsemblesOutsideGit(t *testing.T) {
	home, wt := t.TempDir(), t.TempDir()
	t.Setenv(loop.HomeEnv, home)
	writeFile(t, filepath.Join(home, "scores/user.toml"), "[[steps]]\nname = \"plan\"\nkind = \"produce\"\npart = \"planner\"\nwrites = \"reply\"\nnext = { done = \"approved\" }\n")
	var diag bytes.Buffer
	var code int
	out, _ := captureStdout(t, func() error { code = execute([]string{"repertoire", "-worktree", wt, "-json"}, &diag); return nil })
	if code != 0 || !strings.Contains(diag.String(), "note: repository scores not read:") {
		t.Fatal(code, diag.String())
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(out), &raw); err != nil || string(raw["ensembles"]) != "[]" {
		t.Fatal(out, err)
	}
	var scores []map[string]any
	if err := json.Unmarshal(raw["scores"], &scores); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, s := range scores {
		if s["name"] == "user" {
			found = true
			if s["source"] != "user" {
				t.Fatal(s)
			}
			if _, ok := s["description"]; ok {
				t.Fatal(s)
			}
		}
	}
	if !found {
		t.Fatal(scores)
	}
	text, _ := captureStdout(t, func() error { code = execute([]string{"repertoire", "-worktree", wt}, &diag); return nil })
	if code != 0 || !strings.Contains(text, "no ensembles") {
		t.Fatal(text)
	}
	for _, arg := range []string{"-unknown", "extra"} {
		if code := execute([]string{"repertoire", arg}, &diag); code != 2 {
			t.Fatal(arg, code)
		}
	}
}

func TestRepertoireRefusesARepositoryPartUsedByAUserScore(t *testing.T) {
	home, wt := t.TempDir(), t.TempDir()
	t.Setenv(loop.HomeEnv, home)
	writeFile(t, filepath.Join(home, "config.toml"), "[agents]\navailable = [\"claude\"]\n[run]\nrepository_scores = false\n")
	safetyGit(t, wt, "init", "-q")
	writeFile(t, filepath.Join(wt, ".kapellmeister/parts/scout.md"), "Inspect.")
	safetyGit(t, wt, "add", ".")
	safetyGit(t, wt, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-qm", "fixture")
	writeFile(t, filepath.Join(home, "scores/custom.toml"), "[[steps]]\nname = \"inspect\"\nkind = \"produce\"\npart = \"scout\"\nnext = { done = \"approved\" }\n")
	writeFile(t, filepath.Join(wt, ".kapellmeister/ensembles/sample.toml"), "score = \"custom\"\n[sections.work.chairs.scout]\npart = \"scout\"\nagents = [\"claude\"]\n")
	var diag bytes.Buffer
	var code int
	out, _ := captureStdout(t, func() error { code = execute([]string{"repertoire", "-worktree", wt, "-json"}, &diag); return nil })
	if code != 0 {
		t.Fatal(diag.String())
	}
	var got struct {
		Ensembles []repertoireEnsemble `json:"ensembles"`
		Scores    []repertoireScore    `json:"scores"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Ensembles) != 1 || got.Ensembles[0].Ready || !strings.Contains(got.Ensembles[0].Reason, "repository part") || !strings.Contains(got.Ensembles[0].Reason, "repository_scores is false") {
		t.Fatal(got.Ensembles)
	}
	found := false
	for _, s := range got.Scores {
		if s.Name == "custom" {
			found = true
			if s.Source != "user" || s.Error != got.Ensembles[0].Reason || len(s.Steps) != 0 {
				t.Fatal(s)
			}
		}
	}
	if !found {
		t.Fatal(got.Scores)
	}
}

func TestAConflictingBrokenEnsembleIsReportedAsAConflictInBothRows(t *testing.T) {
	home, wt := t.TempDir(), t.TempDir()
	t.Setenv(loop.HomeEnv, home)
	writeFile(t, filepath.Join(home, "config.toml"), "[agents]\navailable = [\"claude\"]\n[ensembles.conflict]\nplanner = \"claude\"\n")
	writeFile(t, filepath.Join(wt, ".kapellmeister/ensembles/conflict.toml"), "bad = true\n")
	var diag bytes.Buffer
	var code int
	out, _ := captureStdout(t, func() error { code = execute([]string{"repertoire", "-worktree", wt, "-json"}, &diag); return nil })
	if code != 0 {
		t.Fatal(diag.String())
	}
	var got struct {
		Ensembles []repertoireEnsemble `json:"ensembles"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Ensembles) != 2 {
		t.Fatal(got)
	}
	for _, r := range got.Ensembles {
		if r.Ready || !strings.Contains(r.Reason, "defined both") {
			t.Fatal(r)
		}
	}
}
