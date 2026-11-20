package loop

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const smallScoreFile = `rounds = 2
[parts.drafter]
memory = "fresh"
[[steps]]
name = "draft"
kind = "produce"
part = "drafter"
escalate = true
next = { done = "inspect" }
[[steps]]
name = "inspect"
kind = "review"
part = "reviewer"
reads = ["draft", "draft"]
approve_when = "all"
next = { approve = "approved", changes = "draft" }
`

func scoreFileWrite(t *testing.T, root, path, text string) string {
	t.Helper()
	path = filepath.Join(root, path)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}
func scoreFileSource(t *testing.T) ScoreSource {
	t.Helper()
	s := ScoreSource{Home: t.TempDir(), AllowRepository: true}
	scoreFileWrite(t, s.Home, "scores/small.toml", smallScoreFile)
	scoreFileWrite(t, s.Home, "parts/drafter.md", "Draft {reply}.")
	return s
}
func scoreFileGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.invalid", "GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.invalid")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %s: %v", args, out, err)
	}
}
func TestAScoreFileLoadsIntoTheScoreTheEnginePlays(t *testing.T) {
	s := scoreFileSource(t)
	got, err := LoadScore("small", s)
	if err != nil {
		t.Fatal(err)
	}
	if got.Score.Name != "small" || got.Score.Source != "user" || got.Score.Rounds != 2 || len(got.Files) != 2 {
		t.Fatalf("loaded = %+v", got)
	}
	if got.Score.Steps[1].Brief != "review" || got.Score.Steps[1].ApproveWhen != "all" || !got.Score.Steps[0].Escalate {
		t.Fatalf("steps = %+v", got.Score.Steps)
	}
	if got.Score.Outline(false, false).Source != "user" {
		t.Fatal("outline lost source")
	}
}
func TestARepositoryScoreAndPartAreReadFromTheBaseCommitNotTheWorktree(t *testing.T) {
	s := ScoreSource{Home: t.TempDir(), Worktree: t.TempDir(), AllowRepository: true}
	scoreFileGit(t, s.Worktree, "init", "-q")
	scoreFileWrite(t, s.Worktree, ".kapellmeister/scores/small.toml", smallScoreFile)
	part := scoreFileWrite(t, s.Worktree, ".kapellmeister/parts/drafter.md", "Committed instructions")
	scoreFileGit(t, s.Worktree, "add", ".")
	scoreFileGit(t, s.Worktree, "commit", "-qm", "fixture")
	scoreFileWrite(t, s.Worktree, ".kapellmeister/scores/small.toml", "broken")
	os.Remove(part)
	got, err := LoadScore("small", s)
	if err != nil {
		t.Fatal(err)
	}
	p, _ := got.Score.Part("drafter")
	if p.Prompt["draft"] != "Committed instructions" || got.Files[0].SHA256 != fmt.Sprintf("%x", sha256.Sum256([]byte(smallScoreFile))) {
		t.Fatalf("loaded = %+v, part = %+v", got, p)
	}
	s.AllowRepository = false
	_, err = LoadScore("small", s)
	want := `repository score "small" (.kapellmeister/scores/small.toml) refused: [run] repository_scores is false`
	if err == nil || err.Error() != want {
		t.Fatalf("error = %v, want %s", err, want)
	}
	scoreFileWrite(t, s.Home, "scores/small.toml", smallScoreFile)
	_, err = LoadScore("small", s)
	want = `repository part "drafter" (.kapellmeister/parts/drafter.md) refused: [run] repository_scores is false`
	if err == nil || err.Error() != want {
		t.Fatalf("error = %v", err)
	}
	scoreFileWrite(t, s.Home, "parts/drafter.md", "User instructions")
	got, err = LoadScore("small", s)
	if err != nil {
		t.Fatal(err)
	}
	p, _ = got.Score.Part("drafter")
	if p.Prompt["draft"] != "User instructions" || got.Score.Source != "user" {
		t.Fatalf("loaded = %+v", got)
	}
}
func TestARunThatNamesNoScoreReadsNoScoreFile(t *testing.T) {
	s := scoreFileSource(t)
	scoreFileWrite(t, s.Home, "scores/default.toml", "broken")
	got, err := LoadScore("", s)
	if err != nil || !reflect.DeepEqual(got.Score, DefaultScore()) || len(got.Files) != 0 {
		t.Fatalf("loaded = %+v, %v", got, err)
	}
}
func TestAFileNamedAsABuiltInIsRefusedNamingTheFile(t *testing.T) {
	for _, name := range append([]string{"default"}, AllRoles()...) {
		t.Run(name, func(t *testing.T) {
			s := scoreFileSource(t)
			path := "parts/" + name + ".md"
			if name == "default" {
				path = "scores/default.toml"
			}
			full := scoreFileWrite(t, s.Home, path, "reserved")
			_, err := LoadScore("small", s)
			if err == nil || err.Error() != full+": built-in name is reserved" {
				t.Fatalf("error = %v", err)
			}
		})
	}
}
func TestAnInvalidScoreIsRefusedWithItsFileStepAndReason(t *testing.T) {
	cases := []struct{ name, old, replacement, reason string }{
		{"unknown top key", "rounds = 2", "unknown = true\nrounds = 2", `unknown key "unknown"`},
		{"unknown part key", `memory = "fresh"`, `unexpected = true`, `part drafter: unknown key "unexpected"`},
		{"unknown step key", `escalate = true`, `unexpected = true`, `step draft: unknown key "unexpected"`},
		{"unknown kind", `kind = "produce"`, `kind = "dance"`, `step draft: unknown kind "dance" (want produce, review, decide, test)`},
		{"unknown target", `done = "inspect"`, `done = "missing"`, `step draft: exit done has unknown target "missing"`},
		{"missing exit", `done = "inspect"`, ``, `step draft: exit done has no target`},
		{"foreign exit", `done = "inspect"`, `done = "inspect", absent = "approved"`, `step draft: unknown exit "absent" for produce`},
		{"unknown read", `reads = ["draft", "draft"]`, `reads = ["missing", "draft"]`, `step inspect: reads unknown step "missing"`},
		{"unknown writes", `escalate = true`, `writes = "tree"`, `step draft: unknown writes "tree" (want reply)`},
		{"empty writes", `escalate = true`, `writes = ""`, `step draft: unknown writes "" (want reply)`},
		{"backward without rounds", "rounds = 2\n", "", `step inspect: exit changes targets earlier step "draft" without rounds`},
		{"review resumes", "[parts.drafter]", "[parts.reviewer]\nmemory = \"resume\"\n[parts.drafter]", `part reviewer: review steps cannot resume`},
		{"unknown memory", `memory = "fresh"`, `memory = "forever"`, `part drafter: unknown memory "forever" (want fresh or resume)`},
		{"empty memory", `memory = "fresh"`, `memory = ""`, `part drafter: unknown memory "" (want fresh or resume)`},
		{"optional produce", `memory = "fresh"`, `optional = true`, `part drafter: optional requires test or background steps`},
		{"unknown approval", `approve_when = "all"`, `approve_when = "some"`, `step inspect: unknown approve_when "some" (want any, all or always)`},
		{"empty approval", `approve_when = "all"`, `approve_when = ""`, `step inspect: unknown approve_when "" (want any, all or always)`},
		{"approval on produce", `escalate = true`, `approve_when = "any"`, `step draft: approve_when is only for review`},
		{"background shape", `escalate = true`, `background = true`, `step draft: background requires produce writing only reply`},
		{"background first", `escalate = true`, "background = true\nwrites = \"reply\"", `step draft: background cannot be the first step`},
		{"no ending", `approve = "approved"`, `approve = "draft"`, `step draft: no path reaches an ending`},
		{"unreachable", `done = "inspect"`, `done = "approved"`, `step inspect: no path reaches this step`},
		{"unused part", "[parts.drafter]", "[parts.unused]\n[parts.drafter]", `part unused: no step plays this part`},
		{"short builtin reads", `reads = ["draft", "draft"]`, `reads = ["draft"]`, `step inspect: built-in part reviewer needs at least 2 reads`},
		{"foreign builtin kind", `part = "drafter"`, `part = "gate"`, `step draft: built-in part gate cannot play produce`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := scoreFileSource(t)
			path := scoreFileWrite(t, s.Home, "scores/small.toml", strings.Replace(smallScoreFile, c.old, c.replacement, 1))
			_, err := LoadScore("small", s)
			if err == nil || err.Error() != path+": "+c.reason {
				t.Fatalf("error = %v, want %s: %s", err, path, c.reason)
			}
		})
	}
}
func TestAPartFileSplitsStepInstructionsAndRefusesUnknownPlaceholders(t *testing.T) {
	for _, c := range []struct{ text, want, reason string }{
		{"Common\n## step: draft\nDraft {reply}", "Common\n\nDraft {reply}", ""},
		{"Use ${HOME} and { \"a\": 1 } and {check}", "Use ${HOME} and { \"a\": 1 } and {check}", ""},
		{"Use {unknown}", "", `step draft: unknown placeholder {unknown}`},
		{"## step: missing\nText", "", `step missing: part does not play this step`},
		{"## step: Unknown\nText", "", `step Unknown: part does not play this step`},
		{"---Plain Markdown", "---Plain Markdown", ""},
		{"## step: draft\nText\n## step: draft\nAgain", "", `step draft: duplicate heading`},
		{"## step: draft\n", "", `step draft: empty instructions`},
		{"---\ntitle: draft\n---", "", `front matter is not allowed`},
	} {
		t.Run(c.text, func(t *testing.T) {
			s := scoreFileSource(t)
			path := scoreFileWrite(t, s.Home, "parts/drafter.md", c.text)
			got, err := LoadScore("small", s)
			if c.reason != "" {
				if err == nil || err.Error() != path+": "+c.reason {
					t.Fatalf("error = %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			p, _ := got.Score.Part("drafter")
			if p.Prompt["draft"] != c.want {
				t.Fatalf("prompt = %q", p.Prompt["draft"])
			}
		})
	}
}

func TestEveryExitAndStepShapeIsCheckedBeforePlaying(t *testing.T) {
	const produce = `[[steps]]
name = "draft"
kind = "produce"
part = "drafter"
next = { done = "approved" }
`
	for _, c := range []struct{ name, text, reason string }{
		{"no steps", "rounds = 1", "score has no steps"},
		{"rounds", "rounds = 0\n" + produce, "rounds must be at least 1"},
		{"duplicate", produce + produce, "step draft: duplicate step name"},
		{"invalid name", strings.Replace(produce, `name = "draft"`, `name = "../draft"`, 1), `step #1: invalid name "../draft"`},
		{"reserved name", strings.Replace(produce, `name = "draft"`, `name = "verify"`, 1), "step verify: step name is reserved"},
		{"classifier step", strings.Replace(produce, `part = "drafter"`, `part = "classifier"`, 1), `step draft: invalid step part "classifier"`},
		{"review exit", strings.ReplaceAll(strings.Replace(produce, `kind = "produce"`, `kind = "review"`, 1), `done = "approved"`, `approve = "approved", changes = "changes_requested", done = "approved"`), `step draft: unknown exit "done" for review`},
		{"decide exit", strings.ReplaceAll(strings.Replace(produce, `kind = "produce"`, `kind = "decide"`, 1), `done = "approved"`, `approve = "approved", changes = "changes_requested", absent = "approved"`), `step draft: unknown exit "absent" for decide`},
		{"test absent", strings.ReplaceAll(strings.Replace(produce, `kind = "produce"`, `kind = "test"`, 1), `done = "approved"`, `approve = "approved", changes = "changes_requested"`), `step draft: exit absent has no target`},
		{"review missing", strings.ReplaceAll(strings.Replace(produce, `kind = "produce"`, `kind = "review"`, 1), `done = "approved"`, `approve = "approved"`), `step draft: exit changes has no target`},
		{"decide missing", strings.ReplaceAll(strings.Replace(produce, `kind = "produce"`, `kind = "decide"`, 1), `done = "approved"`, `changes = "changes_requested"`), `step draft: exit approve has no target`},
		{"test foreign", strings.ReplaceAll(strings.Replace(produce, `kind = "produce"`, `kind = "test"`, 1), `done = "approved"`, `approve = "approved", changes = "changes_requested", absent = "approved", done = "approved"`), `step draft: unknown exit "done" for test`},
		{"test writes", strings.ReplaceAll(strings.Replace(produce, `kind = "produce"`, `kind = "test"`+"\nwrites = \"reply\"", 1), `done = "approved"`, `approve = "approved", changes = "changes_requested", absent = "approved"`), "step draft: writes is only for produce"},
		{"review escalation", strings.ReplaceAll(strings.Replace(produce, `kind = "produce"`, `kind = "review"`+"\nescalate = true", 1), `done = "approved"`, `approve = "approved", changes = "changes_requested"`), "step draft: escalate is only for produce"},
		{"planner writes", strings.Replace(produce, `part = "drafter"`, `part = "planner"`, 1), "step draft: built-in part planner requires writes = reply"},
		{"background not produce", produce + `[[steps]]
name = "accompany"
kind = "review"
part = "drafter"
background = true
`, "step accompany: background requires produce writing only reply"},
		{"background exits", produce + `[[steps]]
name = "accompany"
kind = "produce"
part = "drafter"
background = true
writes = "reply"
next = {done = "approved"}
`, "step accompany: background has no next exits"},
		{"two backgrounds", produce + `[[steps]]
name = "accompany"
kind = "produce"
part = "drafter"
background = true
writes = "reply"
[[steps]]
name = "accompany-again"
kind = "produce"
part = "drafter"
background = true
writes = "reply"
`, "step accompany-again: more than one background step"},
		{"targeted background", strings.Replace(produce, `done = "approved"`, `done = "accompany"`, 1) + `[[steps]]
name = "accompany"
kind = "produce"
part = "drafter"
background = true
writes = "reply"
`, `step draft: exit done targets background step "accompany"`},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := decodeScore(c.text, "score.toml", "small")
			if err == nil || err.Error() != "score.toml: "+c.reason {
				t.Fatalf("error = %v, want score.toml: %s", err, c.reason)
			}
		})
	}
}

func TestABackgroundStepIsReachedAlongsideTheStepBeforeIt(t *testing.T) {
	text := `[[steps]]
name = "draft"
kind = "produce"
part = "drafter"
next = {done = "inspect"}
[[steps]]
name = "accompany"
kind = "produce"
part = "drafter"
background = true
writes = "reply"
[[steps]]
name = "inspect"
kind = "review"
part = "inspector"
reads = ["draft", "accompany"]
next = {approve = "approved", changes = "changes_requested"}
`
	if _, err := decodeScore(text, "score.toml", "small"); err != nil {
		t.Fatal(err)
	}
}
func TestAnUnknownPartIsRefusedNamingItsStepAndBothPlacesSearched(t *testing.T) {
	s := scoreFileSource(t)
	os.Remove(filepath.Join(s.Home, "parts/drafter.md"))
	_, err := LoadScore("small", s)
	want := fmt.Sprintf(`%s: step draft: unknown part "drafter" (looked in %s and .kapellmeister/parts/drafter.md at the base commit)`, filepath.Join(s.Home, "scores/small.toml"), filepath.Join(s.Home, "parts/drafter.md"))
	if err == nil || err.Error() != want {
		t.Fatalf("error = %v, want %s", err, want)
	}
}

func TestRepositoryReservedNamesAreCheckedAtTheBaseCommit(t *testing.T) {
	s := scoreFileSource(t)
	s.Worktree = t.TempDir()
	scoreFileGit(t, s.Worktree, "init", "-q")
	scoreFileWrite(t, s.Worktree, "initial", "fixture")
	scoreFileGit(t, s.Worktree, "add", ".")
	scoreFileGit(t, s.Worktree, "-c", "commit.gpgsign=false", "commit", "-qm", "fixture")
	path := ".kapellmeister/parts/reviewer.md"
	scoreFileWrite(t, s.Worktree, path, "Uncommitted instructions")
	if _, err := LoadScore("small", s); err != nil {
		t.Fatal(err)
	}
	scoreFileGit(t, s.Worktree, "add", ".")
	scoreFileGit(t, s.Worktree, "-c", "commit.gpgsign=false", "commit", "-qm", "fixture")
	_, err := LoadScore("small", s)
	if err == nil || err.Error() != path+" at the base commit: built-in name is reserved" {
		t.Fatalf("error = %v", err)
	}
}
func TestRepositoryPartsMustBeRegularReadableTextAtTheBaseCommit(t *testing.T) {
	for _, c := range []struct{ name, text, reason string }{
		{"symlink", "", "symbolic link"},
		{"invalid utf8", string([]byte{0xff}), "invalid UTF-8"},
		{"direction", "instructions\u202e", "invisible or direction-changing characters"},
		{"control", "instructions\x01", "control characters"},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := scoreFileSource(t)
			os.Remove(filepath.Join(s.Home, "parts/drafter.md"))
			s.Worktree = t.TempDir()
			scoreFileGit(t, s.Worktree, "init", "-q")
			path := ".kapellmeister/parts/drafter.md"
			full := scoreFileWrite(t, s.Worktree, path, c.text)
			if c.name == "symlink" {
				os.Remove(full)
				if err := os.Symlink("missing", full); err != nil {
					t.Fatal(err)
				}
			}
			scoreFileGit(t, s.Worktree, "add", ".")
			scoreFileGit(t, s.Worktree, "-c", "commit.gpgsign=false", "commit", "-qm", "fixture")
			_, err := LoadScore("small", s)
			if err == nil || err.Error() != path+" at the base commit: "+c.reason {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestAFileScoreHonoursItsReviewPolicyWithoutEscalatingStepsThatHaveNoPolicy(t *testing.T) {
	for _, override := range []string{"", "any"} {
		t.Run(override, func(t *testing.T) {
			loaded, err := LoadScore("small", scoreFileSource(t))
			if err != nil {
				t.Fatal(err)
			}
			e, f, records := newEngine(t, passing)
			e.Score = loaded.Score
			policy := DefaultPolicy()
			after := 1
			policy.Sizing.EscalateAfter = &after
			policy.Sizing.Mode = SizingOff
			e.Config.Policy = &policy
			e.Config.GateWhen = override
			e.Config.MaxRounds = 2
			e.Config.Chairs.Parts = map[string][]Chair{"drafter": {scoreChair("drafter")}}
			e.Config.Chairs.Reviewers = []Chair{scoreChair("first"), scoreChair("second")}
			f.actions["inspect"] = func(step Step) {
				word := "APPROVE"
				if step.Chair.Profile == "second" {
					word = "CHANGES"
				}
				f.write(step.ReplyPath, word+"\n")
			}
			result := run(t, e)
			want := RoundsExhausted
			if override == "any" {
				want = Approved
			}
			if result.Outcome != want || strings.Contains(records.String(), `"step":"escalate"`) {
				t.Fatalf("result = %+v, records = %s", result, records)
			}
		})
	}
}
