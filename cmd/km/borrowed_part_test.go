package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jystringfellow/kapellmeister/internal/ensemble"
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
			for i := range tc.chairs {
				if tc.chairs[i].NativeAgent == "writer" {
					tc.chairs[i].Manifest = &loop.NativeManifest{Entries: []loop.NativeManifestEntry{{Path: []byte(f.From), SHA256: f.SHA256}}}
				}
			}
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

func TestPlayKeepsBorrowedTextThatDiffersFromTheChairsPinnedDefinition(t *testing.T) {
	for _, moved := range []bool{false, true} {
		t.Run(fmt.Sprint(moved), func(t *testing.T) {
			_, _, members, wt := smallRepositoryScore(t)
			const path = ".claude/agents/writer.md"
			pinned := "---\nname: writer\n---\nPinned body\n"
			writeFile(t, filepath.Join(loop.Home(), "scores/small.toml"), strings.Replace(repositorySmallScore, "rounds = 2", "rounds = 2\n[parts.drafter]\nfrom = \""+path+"\"", 1))
			writeFile(t, filepath.Join(loop.Home(), "parts/judge.md"), "judge")
			commit := func(body string) {
				writeFile(t, filepath.Join(wt, path), body)
				for _, args := range [][]string{{"add", "-f", path}, {"-c", "commit.gpgsign=false", "commit", "-qm", "agent"}} {
					if out, err := exec.Command("git", append([]string{"-C", wt}, args...)...).CombinedOutput(); err != nil {
						t.Fatalf("%s: %v", out, err)
					}
				}
			}
			commit(pinned)
			standing := ensemble.Standing{Ensemble: "small", Session: "file-session", Worktree: wt, Task: "probe"}
			for _, m := range members {
				c := m.Chair
				if m.Part == "drafter" {
					c.Provider = "claude_code"
					c.NativeAgent = "writer"
					var err error
					c, err = loop.ResolveNativeDefinition(wt, c, true)
					if err != nil {
						t.Fatal(err)
					}
				}
				standing.Chairs = append(standing.Chairs, ensemble.StandingChair{Name: m.Name, Section: m.Section, Part: m.Part, Address: m.Address("small"), Binding: c, TerminalID: m.Name, Briefed: true})
			}
			savePlayStanding(t, standing)
			body := pinned
			if moved {
				body = "---\nname: writer\n---\nNew body\n"
				commit(body)
			}
			fake := &playCAO{}
			srv := fake.serve(t)
			out, err := capturePlay(t, []string{standing.Session, "-cao", srv.URL})
			if err != nil {
				t.Fatalf("%v: %s", err, out)
			}
			if len(fake.requests) == 0 {
				t.Fatal("no requests")
			}
			prompt := fake.requests[0].Prompt
			entries, err := os.ReadDir(filepath.Join(loop.StateDir(), "runs"))
			if err != nil || len(entries) != 1 {
				t.Fatalf("%v %v", entries, err)
			}
			dir := filepath.Join(loop.StateDir(), "runs", entries[0].Name())
			telemetry, err := os.ReadFile(filepath.Join(dir, "telemetry.jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			result, err := os.ReadFile(filepath.Join(dir, "result.json"))
			if err != nil {
				t.Fatal(err)
			}
			if moved {
				sum := fmt.Sprintf("%x", sha256.Sum256([]byte(body)))
				for _, v := range []string{"New body", "Instructions from " + path, "End of " + path} {
					if !strings.Contains(prompt, v) {
						t.Fatal(prompt)
					}
				}
				if !strings.Contains(string(telemetry), "part text kept in the prompt") || !strings.Contains(out, "part text kept in the prompt") || !strings.Contains(string(result), sum) {
					t.Fatalf("%s\n%s\n%s", out, telemetry, result)
				}
			} else if strings.Contains(prompt, "Pinned body") || strings.Contains(prompt, "Instructions from "+path) || strings.Contains(prompt, "End of "+path) || !strings.Contains(string(telemetry), "part text supplied by agent writer") {
				t.Fatalf("%s\n%s", prompt, telemetry)
			}
		})
	}
}

func TestBorrowedSourceLineNamesChairsWithDifferentPinnedDefinitions(t *testing.T) {
	f := loop.ScoreFile{Kind: "part", Name: "writer", Source: "repository", From: ".claude/agents/writer.md", SHA256: strings.Repeat("a", 64)}
	matching := loop.Chair{NativeAgent: "writer", Address: "a", Manifest: &loop.NativeManifest{Entries: []loop.NativeManifestEntry{{Path: []byte(f.From), SHA256: f.SHA256}}}}
	different := loop.Chair{NativeAgent: "writer", Address: "b", Manifest: &loop.NativeManifest{}}
	for _, tc := range []struct {
		chairs []loop.Chair
		want   string
	}{{[]loop.Chair{different}, "part text kept in the prompt: not the definition pinned for agent writer"}, {[]loop.Chair{matching, different}, "part text supplied by agent writer for a  part text kept in the prompt for b: not the definition pinned for agent writer"}} {
		var out bytes.Buffer
		printScoreFiles(&out, []loop.ScoreFile{f}, loop.Chairs{Parts: map[string][]loop.Chair{"writer": tc.chairs}})
		if !strings.Contains(out.String(), tc.want) {
			t.Fatal(out.String())
		}
	}
}

func TestScoreCheckNamesTheScorePartPathLineAndFixForACRLFBorrowedFile(t *testing.T) {
	_, _, _, wt := smallRepositoryScore(t)
	scorePath := filepath.Join(loop.Home(), "scores/small.toml")
	writeFile(t, scorePath, strings.Replace(repositorySmallScore, "rounds = 2", "rounds = 2\n[parts.drafter]\nfrom = \"agent.md\"", 1))
	writeFile(t, filepath.Join(wt, "agent.md"), "one\ntwo\r\n")
	for _, args := range [][]string{{"config", "core.autocrlf", "false"}, {"add", "-f", "agent.md"}, {"commit", "-qm", "borrowed"}} {
		if out, err := exec.Command("git", append([]string{"-C", wt}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git: %s: %v", out, err)
		}
	}
	var diag bytes.Buffer
	code := 0
	out, err := captureStdout(t, func() error {
		code = execute([]string{"score", "check", "-worktree", wt, scorePath}, &diag)
		return nil
	})
	if err != nil || code != 1 {
		t.Fatalf("code %d: %v %s %s", code, err, out, diag.String())
	}
	for _, want := range []string{scorePath, "part drafter", "agent.md", "line 2", "has CRLF line endings; convert the file to LF"} {
		if !strings.Contains(out+diag.String(), want) {
			t.Errorf("missing %q: %s %s", want, out, diag.String())
		}
	}
}
