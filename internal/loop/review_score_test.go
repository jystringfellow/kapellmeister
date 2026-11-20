package loop

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"
)

func reviewScoreForTest(t *testing.T) Score {
	t.Helper()
	loaded, err := LoadScore("review", ScoreSource{Home: t.TempDir(), AllowRepository: true})
	if err != nil {
		t.Fatal(err)
	}
	return loaded.Score
}
func reviewScoreEngine(t *testing.T) (*Engine, *fakeStepper) {
	t.Helper()
	e, f, _ := newEngine(t, passing)
	e.Score = reviewScoreForTest(t)
	e.Config.Chairs = Chairs{Reviewers: []Chair{scoreChair("first"), scoreChair("second")}, Gate: scoreChair("gate"), Classifier: scoreChair("classifier")}
	for _, name := range []string{"review", "gate"} {
		f.actions[name] = func(s Step) { f.write(s.ReplyPath, "APPROVE\n") }
	}
	return e, f
}
func TestTheReviewScoreHasOnlyTwoStepsAndReviewerGateAndClassifierParts(t *testing.T) {
	s := reviewScoreForTest(t)
	if s.Name != "review" || s.Source != "built-in" || s.Rounds != 0 || s.LessonsBy != "" || len(s.Steps) != 2 {
		t.Fatalf("score = %+v", s)
	}
	parts := s.Roles()
	if !reflect.DeepEqual(parts, []string{"reviewer", "gate", "classifier"}) {
		t.Fatalf("parts = %v", parts)
	}
	if s.Steps[0].Kind != KindReview || s.Steps[1].Kind != KindDecide || s.Steps[0].ApproveWhen != "any" {
		t.Fatalf("steps = %+v", s.Steps)
	}
}
func TestAReviewScoreRunEndsApprovedWhenTheGateApproves(t *testing.T) {
	e, f := reviewScoreEngine(t)
	verifies := 0
	e.Verify = func(ctx context.Context, dir string) (VerifyResult, error) { verifies++; return passing(ctx, dir) }
	res := run(t, e)
	if res.Outcome != Approved || res.Rounds != 1 || verifies != 1 {
		t.Fatalf("result = %+v, verifies = %d", res, verifies)
	}
	equal(t, f.names(), []string{"review[1]", "review[1]", "gate[1]"})
	for _, name := range []string{"review-1-1.md", "review-1-2.md", "gate-1.md"} {
		if _, err := os.Stat(filepath.Join(res.RunDir, name)); err != nil {
			t.Fatal(err)
		}
	}
	for _, step := range f.steps {
		if !strings.Contains(step.Prompt, "it exited 0. Its output is in "+e.rel("verify-0.txt")) {
			t.Fatalf("prompt = %s", step.Prompt)
		}
	}
}
func TestAReviewScoreRunNamesTheDecisionOrReviewsWhenChangesAreRequested(t *testing.T) {
	for _, gate := range []bool{false, true} {
		t.Run(fmt.Sprint(gate), func(t *testing.T) {
			e, f := reviewScoreEngine(t)
			name, path := "review", "review-1-1.md"
			if gate {
				name, path = "gate", "gate-1.md"
			}
			f.actions[name] = func(s Step) { f.write(s.ReplyPath, "CHANGES\n") }
			res := run(t, e)
			if res.Outcome != ChangesRequested || !strings.Contains(res.Reason, path) {
				t.Fatalf("result = %+v", res)
			}
			if !gate && (len(f.steps) != 2 || !strings.Contains(res.Reason, "review-1-2.md")) {
				t.Fatalf("steps = %v, reason = %s", f.names(), res.Reason)
			}
		})
	}
}
func TestAFailingVerifyEndsAReviewScoreChangesRequestedWithoutPlayingTheGate(t *testing.T) {
	e, f := reviewScoreEngine(t)
	e.Verify = func(context.Context, string) (VerifyResult, error) {
		return VerifyResult{ExitCode: 1, Output: "broken\n"}, nil
	}
	res := run(t, e)
	if res.Outcome != ChangesRequested || len(f.steps) != 2 || !strings.Contains(res.Reason, "verify") || !strings.Contains(res.Reason, "verify-0.txt") {
		t.Fatalf("result = %+v, steps = %v", res, f.names())
	}
	for _, step := range f.steps {
		if !strings.Contains(step.Prompt, "it exited 1.") {
			t.Fatal(step.Prompt)
		}
	}
}
func TestAnUnrunnableOrTimedOutVerifyStillFailsAReviewScore(t *testing.T) {
	for _, timed := range []bool{false, true} {
		t.Run(fmt.Sprint(timed), func(t *testing.T) {
			e, f := reviewScoreEngine(t)
			e.Verify = func(context.Context, string) (VerifyResult, error) {
				if timed {
					return VerifyResult{TimedOut: true, Limit: time.Second, ExitCode: 124}, nil
				}
				return VerifyResult{}, errors.New("command missing")
			}
			res := run(t, e)
			if res.Outcome != Failed || len(f.steps) != 0 {
				t.Fatalf("result = %+v, steps = %v", res, f.names())
			}
		})
	}
}
func TestTwoReviewScoreReviewersPlayInParallelAndWriteSeparateReplies(t *testing.T) {
	e, f := reviewScoreEngine(t)
	first, second := make(chan struct{}), make(chan struct{})
	f.actions["review"] = func(s Step) {
		if s.Chair.Profile == "first" {
			close(first)
			<-second
		} else {
			close(second)
			<-first
		}
		f.write(s.ReplyPath, s.Chair.Profile+"\nAPPROVE\n")
	}
	res := run(t, e)
	if res.Outcome != Approved {
		t.Fatalf("result = %+v", res)
	}
	for i, name := range []string{"first", "second"} {
		data, err := os.ReadFile(filepath.Join(res.RunDir, fmt.Sprintf("review-1-%d.md", i+1)))
		if err != nil || string(data) != name+"\nAPPROVE\n" {
			t.Fatalf("reply = %s, %v", data, err)
		}
	}
}
func TestReviewScoreApprovalUsesTheEnsemblesGateWhen(t *testing.T) {
	for _, when := range []string{"any", "all", "always"} {
		t.Run(when, func(t *testing.T) {
			e, f := reviewScoreEngine(t)
			e.Config.GateWhen = when
			f.actions["review"] = func(s Step) {
				v := "CHANGES"
				if s.Chair.Profile == "first" && when != "always" {
					v = "APPROVE"
				}
				f.write(s.ReplyPath, v+"\n")
			}
			res := run(t, e)
			want := Approved
			if when == "all" {
				want = ChangesRequested
			}
			if res.Outcome != want {
				t.Fatalf("result = %+v", res)
			}
		})
	}
}
func TestStrictRunsAfterApprovalOfAReviewScoreOnly(t *testing.T) {
	for _, approve := range []bool{false, true} {
		t.Run(fmt.Sprint(approve), func(t *testing.T) {
			e, f := reviewScoreEngine(t)
			mark := filepath.Join(t.TempDir(), "strict")
			e.Config.StrictCommand = "echo ran >>" + mark
			if !approve {
				f.actions["gate"] = func(s Step) { f.write(s.ReplyPath, "CHANGES\n") }
			}
			res := run(t, e)
			calls := strictRuns(t, mark)
			if approve && (res.Outcome != Approved || calls != 1) || !approve && (res.Outcome != ChangesRequested || calls != 0) {
				t.Fatalf("result = %+v, strict = %d", res, calls)
			}
		})
	}
}
func TestReviewScoreChairsCannotChangeTheExistingTrackedAndUntrackedChange(t *testing.T) {
	for _, part := range []string{"review", "gate"} {
		t.Run(part, func(t *testing.T) {
			e, f := reviewScoreEngine(t)
			scoreFileWrite(t, f.dir, "tracked", "base\n")
			scoreFileGit(t, f.dir, "add", "tracked")
			scoreFileGit(t, f.dir, "commit", "-qm", "fixture")
			f.write("tracked", "existing\n")
			f.write("untracked", "existing untracked\n")
			before, _ := e.gitOutput("diff", "HEAD")
			status, _ := e.gitOutput("status", "--porcelain=v1", "--untracked-files=all")
			f.actions[part] = func(s Step) {
				f.write("tracked", "damage\n")
				f.write("untracked", "damage\n")
				f.write("extra", "damage\n")
				f.write(s.ReplyPath, "APPROVE\n")
			}
			run(t, e)
			after, _ := e.gitOutput("diff", "HEAD")
			gotStatus, _ := e.gitOutput("status", "--porcelain=v1", "--untracked-files=all")
			data, _ := os.ReadFile(filepath.Join(f.dir, "untracked"))
			if before != after || status != gotStatus || string(data) != "existing untracked\n" {
				t.Fatalf("change altered: %s, %s, %s", after, gotStatus, data)
			}
		})
	}
}
func TestAScoreFileNamedReviewIsRefusedFromTheUserDirectoryAndTheBaseCommit(t *testing.T) {
	for _, repo := range []bool{false, true} {
		t.Run(fmt.Sprint(repo), func(t *testing.T) {
			src := ScoreSource{Home: t.TempDir(), Worktree: newRepo(t), AllowRepository: true}
			root, rel := src.Home, "scores/review.toml"
			if repo {
				root, rel = src.Worktree, ".kapellmeister/scores/review.toml"
			}
			scoreFileWrite(t, root, rel, "bad")
			if repo {
				scoreFileGit(t, root, "add", ".")
				scoreFileGit(t, root, "commit", "-qm", "fixture")
			}
			for _, name := range []string{"review", "another"} {
				_, err := LoadScore(name, src)
				if err == nil || !strings.Contains(err.Error(), "built-in name is reserved") || !strings.Contains(err.Error(), "review.toml") {
					t.Fatalf("error = %v", err)
				}
			}
		})
	}
}
func TestReviewScorePartTextContainsNoAbsentHandoffs(t *testing.T) {
	s := reviewScoreForTest(t)
	absent := regexp.MustCompile(`\b(plan|implementer|summary|tester)\b`)
	for _, name := range []string{"reviewer", "gate"} {
		p, _ := s.Part(name)
		for _, text := range p.Prompt {
			if absent.MatchString(text) {
				t.Fatal(text)
			}
		}
	}
}

func TestAReviewScoreWithAMissingVerifyCommandFailsBeforeReview(t *testing.T) {
	e, f := reviewScoreEngine(t)
	e.Config.VerifyCommand = "./missing-verify-command"
	e.Verify = CommandVerifier(e.Config.VerifyCommand)
	res := run(t, e)
	if res.Outcome != Failed || len(f.steps) != 0 || !strings.Contains(res.Reason, "verify") {
		t.Fatalf("result = %+v, steps = %v", res, f.names())
	}
}
func TestTheReviewScoreCanBeCopiedUsingNewPartNames(t *testing.T) {
	score, ok := BuiltinScoreText("review")
	if !ok {
		t.Fatal("missing embedded score")
	}
	score = strings.NewReplacer("reviewer", "inspector", "gate", "judge").Replace(score)
	loaded, err := decodeScore(score, "copy.toml", "inspection")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []struct{ old, new string }{{"reviewer", "inspector"}, {"gate", "judge"}} {
		if _, err := decodePart(BuiltinPartText("review", p.old), p.new+".md", loaded.StepsOf(p.new)); err != nil {
			t.Fatal(err)
		}
	}
}
func TestEditsWorktreeIsTrueOnlyWhenAProduceStepMayChangeTheWorktree(t *testing.T) {
	for _, c := range []struct {
		kind   StepKind
		writes string
		want   bool
	}{{KindProduce, "", true}, {KindProduce, "reply", false}, {KindReview, "", false}, {KindDecide, "", false}, {KindTest, "", false}} {
		s := Score{Steps: []ScoreStep{{Kind: c.kind, Writes: c.writes}}}
		if s.EditsWorktree() != c.want {
			t.Fatalf("%+v edits = %v", c, s.EditsWorktree())
		}
	}
	if !DefaultScore().EditsWorktree() || reviewScoreForTest(t).EditsWorktree() {
		t.Fatal("built-in worktree permissions")
	}
}
func TestTheVerificationPlaceholderFillsTheConductorsShortResult(t *testing.T) {
	e, _, _ := newEngine(t, passing)
	spec := ScoreStep{Name: "inspect", Part: "inspector", Kind: KindReview}
	text, err := decodePart("Evidence: {verification}; output: {verify}.", "inspector.md", []ScoreStep{spec})
	if err != nil {
		t.Fatal(err)
	}
	e.Score = Score{Parts: []Part{{Name: "inspector", Prompt: text}}, Steps: []ScoreStep{spec}}
	prompt := e.brief(spec, handoff{verify: VerifyResult{ExitCode: 2}, verifyPath: "output.txt"}, "reply.md", Chair{})
	if !strings.Contains(prompt, "Evidence: kapellmeister ran `check`; it exited 2. Its output is in output.txt.; output: output.txt.") {
		t.Fatal(prompt)
	}
}

func TestTheReviewScorePromptsArePinnedAsText(t *testing.T) {
	e, _, _ := newEngine(t, passing)
	e.Score = reviewScoreForTest(t)
	e.Config.Task = "Handle the existing change."
	common := ` in a kapellmeister engineering loop, working in this git worktree.
First read and follow every agent instruction file at the repository root (AGENTS.md, CLAUDE.md, GEMINI.md, .github/copilot-instructions.md, and any others present), including those your tool does not load by itself.
Never commit, push, stash, create branches, or switch branches.
<task>
Handle the existing change.
</task>
`
	expected := map[string]string{
		"review": "You are the reviewer" + common + `Review the uncommitted change in this worktree. Inspect it with ` + "`git status` and `git diff HEAD`" + `, including untracked files.
The task above says what the change is meant to do.
kapellmeister ran ` + "`check`" + `; it exited 0. Its output is in verify-0.txt.
Do not re-run the full test suite; the verification result above is authoritative.
Judge whether the change does what the task asks, follows the repository's rules, is correct, and is safe to merge. Look for bugs, missing tests, and scope creep.
Write your review to review-1.md: state plainly whether you approve the change as it stands, then list every required change with file and line.
A request only for more measurements is not a required change and is no reason for CHANGES.
Do not modify any file other than review-1.md. When done, reply with one short sentence.
Write your reply to review-1.md.
Put APPROVE or CHANGES alone on the last line of the reply file.
`,
		"gate": "You are the gate" + common + `Decide whether the uncommitted change in this worktree is ready to merge.
Read the reviews handed to you below; the task above says what the change is meant to do.
kapellmeister ran ` + "`check`" + `; it exited 0. Its output is in verify-0.txt.
Do not re-run the full test suite; the verification result above is authoritative.
Check the diff yourself with ` + "`git status` and `git diff HEAD`" + `, including untracked files. Sort every point the reviews raised into **blocking** (the change must not merge until it is fixed) or **nit** (it can merge as is), each with a reason, and add any blocking point you find yourself.
A request only for more measurements is never blocking. Approve when the task is done, verification passed if configured, and nothing blocks. List the nits in your decision; they do not stop approval.
Write to gate-1.md: your decision first, then the blocking points and the nits, each with its reason.
Do not modify any file other than gate-1.md. When done, reply with one short sentence.
Read review-1.md.
Write your reply to gate-1.md.
Put APPROVE or CHANGES alone on the last line of the reply file.
`,
	}
	for _, spec := range e.Score.Steps {
		h := handoff{verifyPath: "verify-0.txt"}
		if spec.Kind == KindDecide {
			h.files = []string{"review-1.md"}
		}
		got := e.brief(spec, h, spec.Name+"-1.md", Chair{})
		if got != expected[spec.Name] {
			t.Errorf("%s prompt:\n%s\nwant:\n%s", spec.Name, got, expected[spec.Name])
		}
		if regexp.MustCompile(`\b(plan|implementer|summary|tester)\b`).MatchString(got) {
			t.Fatal(got)
		}
	}
}
