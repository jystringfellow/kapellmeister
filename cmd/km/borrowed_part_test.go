package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jystringfellow/kapellmeister/internal/loop"
)

func TestRepertoireShowsBorrowedPartPaths(t *testing.T) {
	rows, _ := repertoireScores([]loop.ScoreEntry{{Name: "small", Score: loop.Score{Parts: []loop.Part{{Name: "writer", From: "agents/writer.md"}}}}})
	var out bytes.Buffer
	// The JSON representation must carry the path as well as the score name.
	data, err := json.Marshal(rows)
	if err != nil {
		t.Fatal(err)
	}
	out.Write(data)
	if !strings.Contains(out.String(), "agents/writer.md") {
		t.Fatalf("rows = %s", out.String())
	}
}

func TestBorrowedSourceLineIdentifiesOnlyMatchingChairs(t *testing.T) {
	f := loop.ScoreFile{Kind: "part", Name: "writer", Source: "repository", From: ".claude/agents/writer.md", SHA256: strings.Repeat("a", 64), FrontMatter: "front matter ignored, including tools, disallowedTools and model"}
	for _, tc := range []struct {
		name   string
		chairs []loop.Chair
		want   string
	}{
		{"all", []loop.Chair{{NativeAgent: "writer", Address: "a"}, {NativeAgent: "writer", Address: "b"}}, "part text supplied by agent writer\n"},
		{"some", []loop.Chair{{NativeAgent: "writer", Address: "a"}, {NativeAgent: "writer", Address: "b"}, {Address: "c"}}, "part text supplied by agent writer for a, b\n"},
		{"unaddressed", []loop.Chair{{NativeAgent: "writer"}, {Address: "c"}}, "part text supplied by agent writer\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			printScoreFiles(&out, []loop.ScoreFile{f}, loop.Chairs{Parts: map[string][]loop.Chair{"writer": tc.chairs}})
			if !strings.HasSuffix(out.String(), tc.want) || !strings.Contains(out.String(), "from .claude/agents/writer.md  aaaaaaaaaaaa  "+f.FrontMatter) {
				t.Fatal(out.String())
			}
		})
	}
}

func TestScoreShowReportsFlagErrorsWithUsageStatus(t *testing.T) {
	var diag bytes.Buffer
	if code := execute([]string{"score", "show", "-bogus"}, &diag); code != 2 || !strings.Contains(diag.String(), "flag provided but not defined") {
		t.Fatalf("code %d, diagnostic %q", code, diag.String())
	}
}

func TestBorrowedPartAppearsInScoreCommandsAndRunRecords(t *testing.T) {
	_, chairs, members, wt := smallRepositoryScore(t)
	const path = ".claude/agents/writer.md"
	const body = "---\ntools: Read\nmodel: example\n---\nBorrowed body\n"
	score := strings.Replace(repositorySmallScore, "rounds = 2", "rounds = 2\n[parts.drafter]\nfrom = \""+path+"\"", 1)
	scorePath := filepath.Join(loop.Home(), "scores/small.toml")
	writeFile(t, scorePath, score)
	writeFile(t, filepath.Join(wt, path), body)
	for _, args := range [][]string{{"add", "-f", path}, {"-c", "commit.gpgsign=false", "commit", "-qm", "borrowed"}} {
		if out, err := exec.Command("git", append([]string{"-C", wt}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git: %v: %s", err, out)
		}
	}
	loaded, err := loop.LoadScore("small", loop.ScoreSource{Home: loop.Home(), Worktree: wt, AllowRepository: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, jsonMode := range []bool{false, true} {
		args := []string{"-worktree", wt}
		if jsonMode {
			args = append(args, "-json")
		}
		args = append(args, scorePath)
		var out, diag bytes.Buffer
		if err := checkCommand("score", args, nil, &out, &diag, ""); err != nil || !strings.Contains(out.String(), path) {
			t.Fatalf("check %v: %s %s", err, out.String(), diag.String())
		}
	}
	var show, diag bytes.Buffer
	if err := scoreCommand([]string{"show", "-worktree", wt, "small"}, &show, &diag); err != nil || !strings.Contains(show.String(), "from = \""+path+"\"") {
		t.Fatalf("show %v: %s", err, show.String())
	}
	var rep bytes.Buffer
	if err := repertoireCommand([]string{"-worktree", wt}, &rep, &diag); err != nil || !strings.Contains(rep.String(), "borrowed  small  drafter  from "+path) {
		t.Fatalf("repertoire %v: %s", err, rep.String())
	}
	f := &fileScoreStepper{inspect: func(step loop.Step) {
		if step.Name == "draft" {
			writeFile(t, filepath.Join(wt, path), "changed")
		}
	}}
	dir := filepath.Join(loop.StateDir(), "runs", "borrowed-record")
	var progress bytes.Buffer
	sum, err := runEnsemble(context.Background(), ensembleRun{Score: loaded.Score, ScoreFiles: loaded.Files, Members: members, RunID: "borrowed-record", StateDir: dir, Stepper: f, Progress: &progress, Config: loop.Config{RunID: "borrowed-record", Task: "probe", Worktree: wt, Chairs: chairs, MaxRounds: 2, ReplyGrace: time.Millisecond}})
	if err != nil {
		t.Fatal(err)
	}
	result := recordedResult(t, dir)
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(body)))
	found := false
	for _, item := range result["score_files"].([]any) {
		file := item.(map[string]any)
		if file["from"] == path && file["sha256"] == hash {
			found = true
		}
	}
	if !found || !slices.Contains(sum.ScoreFilesChanged, path) || !strings.Contains(fmt.Sprint(result["score_files_changed"]), path) {
		t.Fatalf("result %+v summary %+v", result, sum)
	}
	var summary bytes.Buffer
	finishRun(&summary, sum)
	if !strings.Contains(summary.String(), "changed   "+path) {
		t.Fatal(summary.String())
	}
}

func TestScoreShowLoadsUserScoresOutsideGitRepositories(t *testing.T) {
	home := t.TempDir()
	t.Setenv("KAPELLMEISTER_HOME", home)
	wt := t.TempDir()
	writeFile(t, filepath.Join(home, "scores/small.toml"), repositorySmallScore)
	writeFile(t, filepath.Join(home, "parts/drafter.md"), "body")
	writeFile(t, filepath.Join(home, "parts/judge.md"), "body")
	var out, diag bytes.Buffer
	err := scoreCommand([]string{"show", "-worktree", wt, "small"}, &out, &diag)
	if err != nil || !strings.Contains(out.String(), "[parts.drafter]") {
		t.Fatalf("%v: %s", err, out.String())
	}
}

func TestBorrowedSourceLinesShowFrontMatterNotesOnlyWhenPresent(t *testing.T) {
	for _, note := range []string{"", "front matter ignored", "front matter ignored, including tools", "front matter ignored, including disallowedTools", "front matter ignored, including model", "front matter ignored, including tools and model"} {
		var out bytes.Buffer
		printScoreFiles(&out, []loop.ScoreFile{{Kind: "part", Name: "writer", Source: "repository", From: "agent.md", SHA256: strings.Repeat("b", 64), FrontMatter: note}}, loop.Chairs{})
		want := "part  writer  repository  from agent.md  bbbbbbbbbbbb"
		if note != "" {
			want += "  " + note
		}
		want += "\n"
		if out.String() != want {
			t.Fatalf("got %q want %q", out.String(), want)
		}
	}
}
