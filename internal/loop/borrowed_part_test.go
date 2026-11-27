package loop

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestBorrowedPartPathsAreValidated(t *testing.T) {
	for _, path := range []string{"", "/a.md", "a/../b.md", "a/./b.md", "a//b.md", `a\b.md`, ".git/a.md", ".GIT/a.md", "a.txt", "a\x01.md"} {
		text := smallScoreFile + "\n[parts.writer]\nfrom = " + strconvQuote(path) + "\n"
		_, err := decodeScore(text, "score.toml", "small")
		if err == nil || !strings.Contains(err.Error(), "score.toml") || !strings.Contains(err.Error(), "part writer") || strings.Contains(err.Error(), "unknown key") || !strings.Contains(err.Error(), "from") {
			t.Errorf("path %q: %v", path, err)
		}
	}
}

func strconvQuote(s string) string { return strings.ReplaceAll(strconv.Quote(s), `\x01`, `\u0001`) }

func TestBorrowedPartsReadCommittedTextWithoutFrontMatter(t *testing.T) {
	s := scoreFileSource(t)
	s.Worktree = t.TempDir()
	scoreFileGit(t, s.Worktree, "init", "-q")
	scoreFileWrite(t, s.Home, "scores/small.toml", strings.Replace(smallScoreFile, "memory = \"fresh\"", "memory = \"fresh\"\nfrom = \".claude/agents/writer.md\"", 1))
	scoreFileWrite(t, s.Worktree, ".claude/agents/writer.md", "---\ntools: Read\nmodel: x\n---\nLiteral {unknown}\n## step: other\n")
	scoreFileGit(t, s.Worktree, "add", ".")
	scoreFileGit(t, s.Worktree, "commit", "-qm", "fixture")
	scoreFileWrite(t, s.Worktree, ".claude/agents/writer.md", "changed")
	got, err := LoadScore("small", s)
	if err != nil {
		t.Fatal(err)
	}
	part, _ := got.Score.Part("drafter")
	if part.Prompt["draft"] != "Literal {unknown}\n## step: other" {
		t.Fatalf("prompt = %q", part.Prompt["draft"])
	}
	if part.FromSHA256 != got.Files[1].SHA256 || len(got.Files) != 2 || got.Files[1].From != ".claude/agents/writer.md" || got.Files[1].SHA256 != fmt.Sprintf("%x", sha256.Sum256([]byte("---\ntools: Read\nmodel: x\n---\nLiteral {unknown}\n## step: other\n"))) {
		t.Fatalf("files = %+v", got.Files)
	}
}

func TestBorrowedPartPromptKeepsLiteralTextAndAppendsInstructions(t *testing.T) {
	e := &Engine{Score: Score{Name: "custom", Steps: []ScoreStep{{Name: "draft", Part: "writer"}}, Parts: []Part{{Name: "writer", From: "agent.md", Prompt: map[string]string{"draft": "Literal {reply} {unknown}\n## step: other"}}}}}
	got := e.brief(ScoreStep{Name: "draft", Part: "writer", Kind: KindProduce, Writes: "reply"}, handoff{}, "reply.md", Chair{})
	for _, want := range []string{"Literal {reply} {unknown}\n## step: other", "Write your reply to reply.md.", "Do not modify any file other than reply.md."} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
}

func TestBorrowedFrontMatterNamesOnlyUnindentedSettings(t *testing.T) {
	for _, tc := range []struct {
		input, body, note string
		bad               bool
	}{
		{"body", "body", "", false},
		{"---\nname: writer\n---\nbody", "body", "front matter ignored", false},
		{"---\n tools: ignored\n---\nbody", "body", "front matter ignored", false},
		{"---\ntools: yes\n---\nbody", "body", "front matter ignored, including tools", false},
		{"---\ntools: yes\nmodel: yes\n---\nbody", "body", "front matter ignored, including tools and model", false},
		{"---\n tools: ignored\nmodel: x\ntools: y\ndisallowedTools: z\n---\nbody", "body", "front matter ignored, including tools, disallowedTools and model", false},
		{"---\n\"tools\": x\n---\nbody", "body", "front matter ignored, including tools", false},
		{"---\n  \"tools\": x\n\tmodel : x\n\"tools': x\n\"\"model\"\": x\n---\nbody", "body", "front matter ignored", false},
		{"---\nmodel: x", "", "", true},
		{"---\n---\n \n", "", "", true},
	} {
		body, note, err := stripBorrowedFrontMatter(tc.input)
		if (err != nil) != tc.bad || body != tc.body || note != tc.note {
			t.Errorf("%q: %q %q %v", tc.input, body, note, err)
		}
	}
}

func TestBuiltInPartsCannotBorrowFiles(t *testing.T) {
	for _, name := range []string{"planner", "implementer", "reviewer", "gate", "tester", "classifier"} {
		_, err := decodeScore(smallScoreFile+"\n[parts."+name+"]\nfrom = \"agent.md\"\n", "score.toml", "small")
		if err == nil || !strings.Contains(err.Error(), "part "+name) || !strings.Contains(err.Error(), "from") {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestBorrowedPartsRefuseUnavailableAndNonRegularEntries(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*testing.T, ScoreSource)
		allow bool
		want  string
	}{
		{"missing", func(t *testing.T, s ScoreSource) {}, true, "does not exist"},
		{"disabled", func(t *testing.T, s ScoreSource) {}, false, "repository_scores is false"},
		{"link", func(t *testing.T, s ScoreSource) {
			if err := os.Symlink("target", filepath.Join(s.Worktree, "agent.md")); err != nil {
				t.Fatal(err)
			}
		}, true, "symbolic link"},
		{"directory", func(t *testing.T, s ScoreSource) { scoreFileWrite(t, s.Worktree, "agent.md/child", "text") }, true, "not a regular file"},
		{"large", func(t *testing.T, s ScoreSource) {
			scoreFileWrite(t, s.Worktree, "agent.md", strings.Repeat("a", partSizeLimit+1))
		}, true, "over the"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := scoreFileSource(t)
			s.Worktree = t.TempDir()
			s.AllowRepository = tc.allow
			scoreFileGit(t, s.Worktree, "init", "-q")
			scoreFileWrite(t, s.Home, "scores/small.toml", strings.Replace(smallScoreFile, "memory = \"fresh\"", "memory = \"fresh\"\nfrom = \"agent.md\"", 1))
			scoreFileWrite(t, s.Worktree, "other", "fixture")
			tc.setup(t, s)
			scoreFileGit(t, s.Worktree, "add", ".")
			scoreFileGit(t, s.Worktree, "commit", "-qm", "fixture")
			_, err := LoadScore("small", s)
			if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "part drafter") || !strings.Contains(err.Error(), "small.toml") {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestNativeAgentSuppliesOnlyItsMatchingBorrowedPart(t *testing.T) {
	body := "unique borrowed body"
	e := &Engine{Score: Score{Name: "custom", Steps: []ScoreStep{{Name: "draft", Part: "writer"}}, Parts: []Part{{Name: "writer", From: ".claude/agents/writer.md", FromSHA256: "hash", Prompt: map[string]string{"draft": body}}}}}
	spec := ScoreStep{Name: "draft", Part: "writer", Kind: KindReview}
	for _, tc := range []struct {
		agent string
		want  bool
	}{{"writer", false}, {"other", true}, {"", true}} {
		got := e.brief(spec, handoff{}, "reply.md", Chair{NativeAgent: tc.agent, Manifest: &NativeManifest{Entries: []NativeManifestEntry{{Path: []byte(".claude/agents/writer.md"), SHA256: "hash"}}}})
		if strings.Contains(got, body) != tc.want || !strings.Contains(got, "Write your reply to reply.md.") {
			t.Errorf("agent %q: %s", tc.agent, got)
		}
	}
}

func TestBorrowedPartsAppendTheFixedBlockForEveryStepKind(t *testing.T) {
	e := &Engine{Config: Config{ScratchDir: "scratch"}, Score: Score{Name: "custom", Steps: []ScoreStep{{Name: "work", Part: "writer"}}, Parts: []Part{{Name: "writer", From: "agent.md", Prompt: map[string]string{"work": "body"}}}}}
	for _, tc := range []struct {
		kind              StepKind
		writes            string
		restrict, verdict bool
	}{
		{KindProduce, "", false, false}, {KindProduce, "reply", true, false}, {KindReview, "", true, true}, {KindDecide, "", true, true}, {KindTest, "", true, true},
	} {
		got := e.brief(ScoreStep{Name: "work", Part: "writer", Kind: tc.kind, Writes: tc.writes}, handoff{files: []string{"input.md"}}, "reply.md", Chair{})
		for _, want := range []string{"Read input.md.", "kapellmeister did not verify this change", "Write your reply to reply.md."} {
			if !strings.Contains(got, want) {
				t.Errorf("%s missing %q: %s", tc.kind, want, got)
			}
		}
		if strings.Contains(got, "Do not modify any file other than reply.md") != tc.restrict || strings.Contains(got, "Put APPROVE or CHANGES alone") != tc.verdict {
			t.Errorf("%s: %s", tc.kind, got)
		}
	}
}

func TestBorrowedPartPromptDoesNotChangeWhenTheWorktreeFileChanges(t *testing.T) {
	s := scoreFileSource(t)
	s.Worktree = t.TempDir()
	scoreFileGit(t, s.Worktree, "init", "-q")
	scoreFileWrite(t, s.Home, "scores/small.toml", strings.Replace(smallScoreFile, "memory = \"fresh\"", "memory = \"fresh\"\nfrom = \"agent.md\"", 1))
	scoreFileWrite(t, s.Worktree, "agent.md", "committed")
	scoreFileGit(t, s.Worktree, "add", ".")
	scoreFileGit(t, s.Worktree, "commit", "-qm", "fixture")
	before, err := LoadScore("small", s)
	if err != nil {
		t.Fatal(err)
	}
	scoreFileWrite(t, s.Worktree, "agent.md", "edited")
	after, err := LoadScore("small", s)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := before.Score.Part("drafter")
	b, _ := after.Score.Part("drafter")
	if a.Prompt["draft"] != "committed" || b.Prompt["draft"] != a.Prompt["draft"] || after.Files[1].SHA256 != before.Files[1].SHA256 {
		t.Fatalf("before %+v after %+v", before, after)
	}
}

func TestUnclosedBorrowedFrontMatterNamesTheScoreAndPart(t *testing.T) {
	s := scoreFileSource(t)
	s.Worktree = t.TempDir()
	scoreFileGit(t, s.Worktree, "init", "-q")
	path := scoreFileWrite(t, s.Home, "scores/small.toml", strings.Replace(smallScoreFile, "memory = \"fresh\"", "memory = \"fresh\"\nfrom = \"agent.md\"", 1))
	scoreFileWrite(t, s.Worktree, "agent.md", "---\nmodel: x\n")
	scoreFileGit(t, s.Worktree, "add", ".")
	scoreFileGit(t, s.Worktree, "commit", "-qm", "fixture")
	_, err := LoadScore("small", s)
	if err == nil || !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), "part drafter") || !strings.Contains(err.Error(), "unclosed") {
		t.Fatal(err)
	}
}

func TestRepositoryScoresDisabledRefusesUserAndRepositoryScoresBorrowingFiles(t *testing.T) {
	for _, repo := range []bool{false, true} {
		t.Run(fmt.Sprint(repo), func(t *testing.T) {
			s := scoreFileSource(t)
			s.Worktree = t.TempDir()
			s.AllowRepository = false
			scoreFileGit(t, s.Worktree, "init", "-q")
			score := strings.Replace(smallScoreFile, "memory = \"fresh\"", "memory = \"fresh\"\nfrom = \"agent.md\"", 1)
			if repo {
				os.Remove(filepath.Join(s.Home, "scores/small.toml"))
				scoreFileWrite(t, s.Worktree, ".kapellmeister/scores/small.toml", score)
			} else {
				scoreFileWrite(t, s.Home, "scores/small.toml", score)
			}
			scoreFileWrite(t, s.Worktree, "agent.md", "body")
			scoreFileGit(t, s.Worktree, "add", ".")
			scoreFileGit(t, s.Worktree, "commit", "-qm", "fixture")
			_, err := LoadScore("small", s)
			if err == nil || !strings.Contains(err.Error(), "repository_scores is false") || !strings.Contains(err.Error(), "small") {
				t.Fatal(err)
			}
		})
	}
}

func TestReservedBorrowedPartNamesAreRefusedWhenLoadingScores(t *testing.T) {
	s := scoreFileSource(t)
	path := scoreFileWrite(t, s.Home, "scores/small.toml", smallScoreFile+"\n[parts.classifier]\nfrom = \"agent.md\"\n")
	_, err := LoadScore("small", s)
	if err == nil || !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), "part classifier") {
		t.Fatal(err)
	}
}

func TestBorrowedPartRecordsTheAgentThatSuppliesItsText(t *testing.T) {
	for _, tc := range []struct{ agent, from, want string }{{"writer", ".claude/agents/writer.md", "part text supplied by agent writer"}, {"writer", "other.md", ""}, {"", ".claude/agents/writer.md", ""}} {
		e, _, records := newEngine(t, passing)
		e.Score = Score{Name: "custom", Steps: []ScoreStep{{Name: "draft", Part: "writer"}}, Parts: []Part{{Name: "writer", From: tc.from, FromSHA256: "hash"}}}
		_, err := e.step(context.Background(), Step{Name: "draft", Chair: Chair{Provider: "p", Profile: "x", NativeAgent: tc.agent, Manifest: &NativeManifest{Entries: []NativeManifestEntry{{Path: []byte(".claude/agents/writer.md"), SHA256: "hash"}}}}, ReplyPath: "reply.md"})
		if err != nil {
			t.Fatal(err)
		}
		var rec Record
		if err := json.Unmarshal(records.Bytes(), &rec); err != nil {
			t.Fatal(err)
		}
		if rec.PartText != tc.want {
			t.Fatalf("record %+v", rec)
		}
	}
}

func TestBorrowedTestStepReportsVerificationAndScratchRules(t *testing.T) {
	e := &Engine{Config: Config{VerifyCommand: "check", ScratchDir: "scratch"}, Score: Score{Name: "custom", Steps: []ScoreStep{{Name: "test", Part: "writer"}}, Parts: []Part{{Name: "writer", From: "agent.md", Prompt: map[string]string{"test": "body"}}}}}
	spec := ScoreStep{Name: "test", Part: "writer", Kind: KindTest}
	pending := e.brief(spec, handoff{files: []string{"input"}}, "reply", Chair{})
	if strings.Contains(pending, "kapellmeister ran") {
		t.Fatal(pending)
	}
	done := e.brief(spec, handoff{files: []string{"input"}, verify: VerifyResult{ExitCode: 3}, verifyPath: "verify.log"}, "reply", Chair{})
	for _, want := range []string{"Read input.", "kapellmeister ran `check`; it exited 3. Its output is in verify.log.", "Everything you create stays under scratch.", "Do not modify any file other than reply, apart from what you create under scratch."} {
		if !strings.Contains(done, want) {
			t.Errorf("missing %q: %s", want, done)
		}
	}
}

func TestBorrowedPartsCombineWithMemoryAndIgnoreThePartFile(t *testing.T) {
	s := scoreFileSource(t)
	s.Worktree = t.TempDir()
	scoreFileGit(t, s.Worktree, "init", "-q")
	score := strings.Replace(smallScoreFile, "memory = \"fresh\"", "memory = \"resume\"\nresume_tokens = 50\nresume_steps = 2\nfrom = \"agent.md\"", 1)
	scoreFileWrite(t, s.Home, "scores/small.toml", score)
	scoreFileWrite(t, s.Home, "parts/drafter.md", "{invalid}")
	scoreFileWrite(t, s.Worktree, "agent.md", "borrowed")
	scoreFileGit(t, s.Worktree, "add", ".")
	scoreFileGit(t, s.Worktree, "commit", "-qm", "fixture")
	loaded, err := LoadScore("small", s)
	if err != nil {
		t.Fatal(err)
	}
	part, _ := loaded.Score.Part("drafter")
	if part.Prompt["draft"] != "borrowed" || part.Memory != MemoryResume || part.ResumeTokens != 50 || part.ResumeSteps != 2 {
		t.Fatalf("part %+v", part)
	}
}

func TestOptionalTestPartsMayBorrowFiles(t *testing.T) {
	score := `[parts.writer]
optional = true
from = "agent.md"
[[steps]]
name = "probe"
kind = "test"
part = "writer"
next = { approve = "approved", changes = "changes_requested", absent = "approved" }
`
	got, err := decodeScore(score, "score.toml", "small")
	if err != nil {
		t.Fatal(err)
	}
	part, _ := got.Part("writer")
	if !part.Optional || part.From != "agent.md" {
		t.Fatalf("part %+v", part)
	}
}

func TestBorrowedFrontMatterDelimitersMustBeExact(t *testing.T) {
	for _, c := range []struct{ name, text, want string }{
		{"opening with trailing space", "--- \ntools: Read\n---\nBody", "line 1"},
		{"opening with leading space", " ---\ntools: Read\n---\nBody", "line 1"},
		{"closing with trailing space before a later rule", "---\ntools: Read\n--- \nBody\n---\nMore", "line 3"},
		{"closing with a tab", "---\nmodel: x\n---\t\nBody", "line 3"},
		{"blank line before the opening", "\n---\ntools: Read\n---\nBody", "line 2"},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, _, err := stripBorrowedFrontMatter(c.text)
			if err == nil || !strings.Contains(err.Error(), c.want) || !strings.Contains(err.Error(), "---") {
				t.Fatalf("got %v, want a refusal naming %s", err, c.want)
			}
		})
	}
}

func TestABorrowedBodyMayContainRulesAfterExactFrontMatter(t *testing.T) {
	for _, text := range []string{"Body\n\n---\n\nMore", "---\ntools: Read\n---\nBody\n\n--- \n\nMore", "Body\n --- \nMore"} {
		body, _, err := stripBorrowedFrontMatter(text)
		if err != nil || !strings.Contains(body, "More") {
			t.Fatalf("%q: body %q, err %v", text, body, err)
		}
	}
}

func TestABorrowedPartOnAMessageChairAsksForTheFinalMessageAndNoFile(t *testing.T) {
	e := &Engine{Config: Config{ScratchDir: "scratch"}, Score: Score{Name: "custom", Steps: []ScoreStep{{Name: "work", Part: "writer"}}, Parts: []Part{{Name: "writer", From: "agent.md", Prompt: map[string]string{"work": "Write to {reply}."}}}}}
	for _, kind := range []StepKind{KindReview, KindDecide, KindTest} {
		got := e.brief(ScoreStep{Name: "work", Part: "writer", Kind: kind}, handoff{files: []string{"input.md"}}, "reply.md", Chair{Provider: "claude_code", Reply: ReplyMessage})
		for _, want := range []string{"Write to {reply}.", "Read input.md.", "Do not write or modify any file", "Put APPROVE or CHANGES alone on the last line before the closing km-end line."} {
			if !strings.Contains(got, want) {
				t.Errorf("%s missing %q: %s", kind, want, got)
			}
		}
		for _, unwanted := range []string{"reply.md", "Write your reply to", "final message (not a file)", "of the reply file"} {
			if strings.Contains(got, unwanted) {
				t.Errorf("%s has %q: %s", kind, unwanted, got)
			}
		}
	}
	got := e.brief(ScoreStep{Name: "work", Part: "writer", Kind: KindTest}, handoff{}, "reply.md", Chair{Provider: "claude_code", Reply: ReplyMessage})
	if !strings.Contains(got, "Do not write or modify any file, apart from what you create under scratch.") {
		t.Fatalf("test step: %s", got)
	}
}

func TestBorrowedTextIsDelimitedByLinesNamingItsFile(t *testing.T) {
	e := &Engine{Score: Score{Name: "custom", Steps: []ScoreStep{{Name: "work", Part: "writer"}}, Parts: []Part{{Name: "writer", From: "agent.md", Prompt: map[string]string{"work": "body"}}}}}
	got := e.brief(ScoreStep{Name: "work", Part: "writer", Kind: KindProduce}, handoff{}, "reply", Chair{})
	if !strings.Contains(got, "Instructions from agent.md follow, quoted as written:\nbody\nEnd of agent.md.\n") {
		t.Fatal(got)
	}
}

func TestQuotedAndSpacedFrontMatterKeysAreNamed(t *testing.T) {
	for _, line := range []string{`"tools": x`, `'tools': x`, `model : x`, `"disallowedTools" : x`} {
		_, note, err := stripBorrowedFrontMatter("---\n" + line + "\n---\nbody")
		if err != nil || note == "front matter ignored" {
			t.Fatalf("%q: %q %v", line, note, err)
		}
	}
}

func TestBorrowedFilesWithCRLFOrABOMAreRefusedNamingTheLineAndTheFix(t *testing.T) {
	for _, tc := range []struct{ body, want string }{{"a\r\nb", "line 1: has CRLF line endings; convert the file to LF"}, {"a\nb\nc\r\n", "line 3: has CRLF line endings; convert the file to LF"}, {"\ufeffa", "line 1: has a byte order mark; remove the BOM"}, {"\ufeffa\r\n", "line 1: has a byte order mark; remove the BOM"}, {"a\rb", "control characters"}} {
		s := scoreFileSource(t)
		s.Worktree = t.TempDir()
		scoreFileGit(t, s.Worktree, "init", "-q")
		scoreFileGit(t, s.Worktree, "config", "core.autocrlf", "false")
		scoreFileWrite(t, s.Home, "scores/small.toml", strings.Replace(smallScoreFile, "memory = \"fresh\"", "memory = \"fresh\"\nfrom = \"agent.md\"", 1))
		scoreFileWrite(t, s.Worktree, "agent.md", tc.body)
		scoreFileGit(t, s.Worktree, "add", ".")
		scoreFileGit(t, s.Worktree, "commit", "-qm", "fixture")
		_, err := LoadScore("small", s)
		if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "part drafter") || !strings.Contains(err.Error(), "agent.md") {
			t.Errorf("%q: %v", tc.body, err)
		}
	}
}

func TestBorrowedTextIsKeptWhenTheChairHasNoPinnedDefinition(t *testing.T) {
	e := &Engine{Score: Score{Name: "custom", Steps: []ScoreStep{{Name: "work", Part: "writer"}}, Parts: []Part{{Name: "writer", From: ".claude/agents/writer.md", Prompt: map[string]string{"work": "body"}}}}}
	got := e.brief(ScoreStep{Name: "work", Part: "writer", Kind: KindProduce}, handoff{}, "reply", Chair{NativeAgent: "writer"})
	if !strings.Contains(got, "body") {
		t.Fatal(got)
	}
}

func TestBorrowedTextIsDroppedOnlyWhenItEqualsThePinnedDefinition(t *testing.T) {
	path := ".claude/agents/writer.md"
	for _, tc := range []struct {
		name, sum string
		manifest  *NativeManifest
		drop      bool
	}{
		{"match", "hash", &NativeManifest{Entries: []NativeManifestEntry{{Path: []byte(path), SHA256: "hash"}}}, true},
		{"different", "other", &NativeManifest{Entries: []NativeManifestEntry{{Path: []byte(path), SHA256: "hash"}}}, false},
		{"missing", "hash", &NativeManifest{}, false}, {"nil", "hash", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := &Engine{Score: Score{Name: "custom", Steps: []ScoreStep{{Name: "work", Part: "writer"}}, Parts: []Part{{Name: "writer", From: path, FromSHA256: tc.sum, Prompt: map[string]string{"work": "unique body"}}}}}
			got := e.brief(ScoreStep{Name: "work", Part: "writer", Kind: KindReview}, handoff{files: []string{"input"}}, "reply", Chair{NativeAgent: "writer", Manifest: tc.manifest, Reply: ReplyMessage})
			if strings.Contains(got, "unique body") == tc.drop || strings.Contains(got, "Instructions from ") == tc.drop || strings.Contains(got, "End of ") == tc.drop {
				t.Fatal(got)
			}
			for _, want := range []string{"Read input.", "kapellmeister did not verify", "Do not write or modify any file.", "closing km-end line"} {
				if !strings.Contains(got, want) {
					t.Fatal(got)
				}
			}
		})
	}
}

func TestMessageChairMayProduceAReplyFromABorrowedPart(t *testing.T) {
	e := &Engine{Score: Score{Name: "custom", Steps: []ScoreStep{{Name: "work", Part: "writer"}}, Parts: []Part{{Name: "writer", From: "agent.md", Prompt: map[string]string{"work": "body"}}}}}
	got := e.brief(ScoreStep{Name: "work", Part: "writer", Kind: KindProduce, Writes: "reply"}, handoff{}, "reply.md", Chair{Reply: ReplyMessage})
	if !strings.Contains(got, "Do not write or modify any file.") || strings.Contains(got, "reply.md") || strings.Contains(got, "Write your reply to") || strings.Contains(got, "APPROVE") {
		t.Fatal(got)
	}
}

func TestBorrowedPathsResolveFromTheWorktreeDirectoryLikeRepositoryParts(t *testing.T) {
	s := scoreFileSource(t)
	root := t.TempDir()
	scoreFileGit(t, root, "init", "-q")
	scoreFileWrite(t, s.Home, "scores/small.toml", strings.Replace(smallScoreFile, "memory = \"fresh\"", "memory = \"fresh\"\nfrom = \"a.md\"", 1))
	scoreFileWrite(t, root, "a.md", "root")
	scoreFileWrite(t, root, "sub/a.md", "sub")
	scoreFileWrite(t, root, "sub/.kapellmeister/parts/drafter.md", "sub part")
	scoreFileGit(t, root, "add", ".")
	scoreFileGit(t, root, "commit", "-qm", "fixture")
	s.Worktree = filepath.Join(root, "sub")
	got, err := LoadScore("small", s)
	if err != nil {
		t.Fatal(err)
	}
	part, _ := got.Score.Part("drafter")
	if part.Prompt["draft"] != "sub" {
		t.Fatal(part)
	}
	scoreFileWrite(t, s.Home, "scores/small.toml", smallScoreFile)
	if err := os.Remove(filepath.Join(s.Home, "parts/drafter.md")); err != nil {
		t.Fatal(err)
	}
	repoPart, err := LoadScore("small", s)
	if err != nil {
		t.Fatal(err)
	}
	p, _ := repoPart.Score.Part("drafter")
	if p.Prompt["draft"] != "sub part" {
		t.Fatal(p)
	}
	scoreFileWrite(t, s.Home, "scores/small.toml", strings.Replace(smallScoreFile, "memory = \"fresh\"", "memory = \"fresh\"\nfrom = \"a.md\"", 1))
	scoreFileGit(t, root, "rm", "sub/a.md")
	scoreFileGit(t, root, "commit", "-qm", "remove")
	_, err = LoadScore("small", s)
	if err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatal(err)
	}
}

func TestBorrowedPathsRefuseSymlinkedDirectoriesAndGitlinks(t *testing.T) {
	for _, link := range []bool{true, false} {
		t.Run(fmt.Sprint(link), func(t *testing.T) {
			s := scoreFileSource(t)
			s.Worktree = t.TempDir()
			scoreFileGit(t, s.Worktree, "init", "-q")
			path := "agent.md"
			want := "not a regular file"
			if link {
				path = "dir/agent.md"
				want = "does not exist"
				scoreFileWrite(t, s.Worktree, "real/agent.md", "body")
				if err := os.Symlink("real", filepath.Join(s.Worktree, "dir")); err != nil {
					t.Fatal(err)
				}
			} else {
				nested := filepath.Join(s.Worktree, "agent.md")
				if err := os.MkdirAll(nested, 0755); err != nil {
					t.Fatal(err)
				}
				scoreFileGit(t, nested, "init", "-q")
				scoreFileWrite(t, nested, "body", "body")
				scoreFileGit(t, nested, "add", ".")
				scoreFileGit(t, nested, "-c", "commit.gpgsign=false", "commit", "-qm", "fixture")
			}
			scoreFileWrite(t, s.Home, "scores/small.toml", strings.Replace(smallScoreFile, "memory = \"fresh\"", "memory = \"fresh\"\nfrom = \""+path+"\"", 1))
			scoreFileGit(t, s.Worktree, "add", ".")
			scoreFileGit(t, s.Worktree, "commit", "-qm", "fixture")
			_, err := LoadScore("small", s)
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatal(err)
			}
		})
	}
}

func TestTheFixedBlockIsLastForEveryStepKind(t *testing.T) {
	e := &Engine{Config: Config{ScratchDir: "scratch"}, Score: Score{Name: "custom", Steps: []ScoreStep{{Name: "work", Part: "writer"}}, Parts: []Part{{Name: "writer", From: "agent.md", Prompt: map[string]string{"work": "body"}}}}}
	for _, tc := range []struct {
		kind   StepKind
		writes string
	}{{KindProduce, ""}, {KindProduce, "reply"}, {KindReview, ""}, {KindDecide, ""}, {KindTest, ""}} {
		spec := ScoreStep{Name: "work", Part: "writer", Kind: tc.kind, Writes: tc.writes}
		chair := Chair{Focus: "focus"}
		h := handoff{files: []string{"input"}, inputs: []stepReply{{failed: []string{"other"}}}}
		want := "End of agent.md.\n" + FocusBlock("focus") + inputNotes(h) + verifyLine("", VerifyResult{}, "")
		if tc.kind == KindTest {
			want += "Everything you create stays under scratch.\n"
		}
		want += replyLine(chair, "reply")
		if tc.kind != KindProduce || tc.writes == "reply" {
			want += modifyLine(chair, tc.kind, "reply", "scratch")
		}
		if tc.kind != KindProduce {
			want += verdictLine(chair)
		}
		got := e.brief(spec, h, "reply", chair)
		if !strings.HasSuffix(got, want) {
			t.Fatalf("%s: %s", tc.kind, got)
		}
	}
}
