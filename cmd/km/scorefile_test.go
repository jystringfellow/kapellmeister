package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jystringfellow/kapellmeister/internal/ensemble"
	"github.com/jystringfellow/kapellmeister/internal/loop"
)

const repositorySmallScore = `rounds = 2
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
next = { approve = "rule", changes = "draft" }
[[steps]]
name = "rule"
kind = "decide"
part = "judge"
reads = ["draft", "inspect"]
next = { approve = "approved", changes = "changes_requested" }
`
const smallScoreEnsemble = `score = "small"
[sections.strings.chairs.first]
part = "drafter"
agents = ["codex"]
[sections.strings.chairs.second]
part = "reviewer"
agents = ["codex"]
[sections.strings.chairs.third]
part = "judge"
agents = ["codex"]
[sections.strings.chairs.fourth]
part = "classifier"
agents = ["codex"]
`

type fileScoreStepper struct {
	steps   []loop.Step
	changes bool
	inspect func(loop.Step)
}

func (f *fileScoreStepper) Run(_ context.Context, step loop.Step) (loop.StepResult, error) {
	f.steps = append(f.steps, step)
	if f.inspect != nil {
		f.inspect(step)
	}
	text := "APPROVE\n"
	if f.changes && (step.Name == "rule" || step.Name == "classify-rule") {
		text = "CHANGES\n"
	}
	if step.Name == "draft" {
		if err := os.WriteFile(filepath.Join(step.Dir, ".kapellmeister/parts/drafter.md"), []byte("changed during run"), 0644); err != nil {
			return loop.StepResult{}, err
		}
	}
	err := os.WriteFile(filepath.Join(step.Dir, step.ReplyPath), []byte(text), 0600)
	return loop.StepResult{Status: "completed", TerminalID: "terminal"}, err
}
func (*fileScoreStepper) Release(context.Context, string) error { return nil }
func smallRepositoryScore(t *testing.T) (loop.Settings, loop.Chairs, []ensemble.Member, string) {
	t.Helper()
	standing := playStanding(t)
	wt := standing.Worktree
	writeFile(t, loop.UserConfigPath(loop.Home()), builtInAgentConfig)
	writeFile(t, filepath.Join(wt, ".kapellmeister/scores/small.toml"), repositorySmallScore)
	writeFile(t, filepath.Join(wt, ".kapellmeister/parts/drafter.md"), "Committed drafter {reply}")
	writeFile(t, filepath.Join(wt, ".kapellmeister/parts/judge.md"), "Committed judge")
	writeFile(t, filepath.Join(wt, ".kapellmeister/ensembles/small.toml"), smallScoreEnsemble)
	for _, args := range [][]string{{"add", "-f", ".kapellmeister/scores", ".kapellmeister/parts"}, {"-c", "commit.gpgsign=false", "commit", "-qm", "fixture"}} {
		if out, err := exec.Command("git", append([]string{"-C", wt}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	writeFile(t, filepath.Join(wt, ".kapellmeister/scores/small.toml"), "uncommitted score")
	writeFile(t, filepath.Join(wt, ".kapellmeister/parts/drafter.md"), "uncommitted part")
	s, err := loop.LoadSettings(loop.Home(), wt)
	if err != nil {
		t.Fatal(err)
	}
	s, chairs, members, err := lineUp(s, wt, "small", noRoleFlags(t))
	if err != nil {
		t.Fatal(err)
	}
	return s, chairs, members, wt
}
func playRepositoryScore(t *testing.T, changes bool) (ensembleSummary, string) {
	t.Helper()
	s, chairs, members, wt := smallRepositoryScore(t)
	if s.Score.Score.Name != "small" || s.GateWhen != "" {
		t.Fatalf("settings = %+v", s)
	}
	var opening bytes.Buffer
	printScoreFiles(&opening, s.Score.Files)
	for _, file := range s.Score.Files {
		if !strings.Contains(opening.String(), file.Path+"  "+file.SHA256[:12]) {
			t.Fatalf("opening = %s", opening.String())
		}
	}
	dir := filepath.Join(loop.StateDir(), "runs", "file-score")
	f := &fileScoreStepper{changes: changes}
	var live []statusRun
	f.inspect = func(step loop.Step) {
		if step.Name == "draft" {
			live = statusRows(t, filepath.Dir(dir))
		}
	}
	sum, err := runEnsemble(context.Background(), ensembleRun{Score: s.Score.Score, ScoreFiles: s.Score.Files, Members: members, Agents: s.Defined, RunID: "file-score", StateDir: dir, Ensemble: "small", Stepper: f, Progress: &opening,
		Config: loop.Config{RunID: "file-score", Task: "Task marker", Worktree: wt, Chairs: chairs, MaxRounds: s.Rounds, ReplyGrace: time.Millisecond}})
	if err != nil {
		t.Fatal(err)
	}
	if len(live) != 1 || live[0].Score == nil || live[0].Score.Name != "small" || live[0].Score.Source != "repository" || len(live[0].Score.Steps) != 5 {
		t.Fatalf("status = %+v", live)
	}
	var names []string
	for _, step := range f.steps {
		if !strings.HasPrefix(step.Name, "classify-") {
			names = append(names, step.Name)
		}
	}
	if !reflect.DeepEqual(names, []string{"draft", "inspect", "rule"}) {
		t.Fatalf("steps = %v", names)
	}
	prompt := f.steps[0].Prompt
	if !strings.Contains(prompt, "Committed drafter") || strings.Contains(prompt, "uncommitted part") || strings.Index(prompt, "Task marker") >= strings.Index(prompt, "Committed drafter") || strings.Index(prompt, "Committed drafter") >= strings.Index(prompt, "Write your reply") {
		t.Fatalf("prompt = %s", prompt)
	}
	result := recordedResult(t, dir)
	if result["score"] != "small" || result["score_source"] != "repository" || len(result["score_files"].([]any)) != 3 {
		t.Fatalf("result = %v", result)
	}
	for i, file := range result["score_files"].([]any) {
		data := file.(map[string]any)
		if data["sha256"] != s.Score.Files[i].SHA256 || len(data["sha256"].(string)) != 64 {
			t.Fatalf("file = %v", data)
		}
	}
	if !reflect.DeepEqual(sum.ScoreFilesChanged, []string{".kapellmeister/parts/drafter.md"}) {
		t.Fatalf("changed = %v", sum.ScoreFilesChanged)
	}
	state, err := os.ReadFile(filepath.Join(dir, "current.json"))
	if err != nil || !strings.Contains(string(state), `"name":"small"`) || !strings.Contains(string(state), `"source":"repository"`) || !strings.Contains(string(state), `"name":"inspect"`) {
		t.Fatalf("state = %s, %v", state, err)
	}
	var summary bytes.Buffer
	finishRun(&summary, sum)
	if !strings.Contains(summary.String(), "changed   .kapellmeister/parts/drafter.md") {
		t.Fatalf("summary = %s", summary.String())
	}
	return sum, dir
}
func TestARunPlaysASmallRepositoryScoreWithAPartFromAFileToApproved(t *testing.T) {
	sum, _ := playRepositoryScore(t, false)
	if sum.Outcome != loop.Approved {
		t.Fatalf("outcome = %s", sum.Outcome)
	}
}
func TestARunEndsChangesRequestedWithExitStatusSevenAndWaitAndReportTreatItAsFinished(t *testing.T) {
	sum, dir := playRepositoryScore(t, true)
	var out bytes.Buffer
	err := finishRun(&out, sum)
	var status *exitStatusError
	if sum.Outcome != loop.ChangesRequested || !errors.As(err, &status) || status.code != 7 {
		t.Fatalf("outcome = %s, error = %v", sum.Outcome, err)
	}
	for _, jsonFlag := range []bool{false, true} {
		args := []string{"file-score", filepath.Dir(dir)}
		if jsonFlag {
			args = append(args, "-json")
		}
		text, diag, code := waitSubprocess(t, args...)
		if code != 7 || diag != "" || !strings.Contains(text, "changes_requested") {
			t.Fatalf("wait = %d, %s, %s", code, text, diag)
		}
	}
	text, diag := captureReport(t, dir)
	if diag != "" || !strings.Contains(text, "changes_requested") || strings.Contains(text, "unfinished") {
		t.Fatalf("report = %s %s", text, diag)
	}
}
func TestAnEnsembleWithNoScoreKeyPlaysTheDefaultScoreUnchanged(t *testing.T) {
	s, wt := ensembleWorktree(t, "")
	s, chairs, _, err := lineUp(s, wt, "sectioned", noRoleFlags(t))
	if err != nil || !reflect.DeepEqual(s.Score.Score, loop.DefaultScore()) || len(s.Score.Files) != 0 || len(chairs.Reviewers) != 2 {
		t.Fatalf("settings = %+v, %v", s, err)
	}
	dir := defaultScoreRun(t)
	for key := range recordedResult(t, dir) {
		if strings.HasPrefix(key, "score") {
			t.Fatalf("unexpected key %s", key)
		}
	}
}
func TestAScoreFlagCannotOverrideAnEnsembleFile(t *testing.T) {
	s, _, _, wt := smallRepositoryScore(t)
	s.ScoreName = "other"
	_, _, _, err := lineUp(s, wt, "small", noRoleFlags(t))
	if err == nil || !strings.Contains(err.Error(), "-score cannot override ensemble small") {
		t.Fatalf("error = %v", err)
	}
}
func TestATierPinOnAChairOfACustomPartIsRefused(t *testing.T) {
	err := checkPin(loop.DefaultPolicy(), "drafter", "strings.first", "codex@small", loop.Chair{Provider: "codex", Tier: "small"})
	if err == nil || err.Error() != "part drafter has no tier policy; name the agent without @tier" {
		t.Fatalf("error = %v", err)
	}
}

const builtInFileScore = `[[steps]]
name = "plan"
kind = "produce"
part = "planner"
writes = "reply"
next = {done = "implement"}
[[steps]]
name = "test-plan"
kind = "produce"
part = "tester"
writes = "reply"
background = true
reads = ["plan"]
[[steps]]
name = "implement"
kind = "produce"
part = "implementer"
reads = ["plan"]
next = {done = "review"}
[[steps]]
name = "review"
kind = "review"
part = "reviewer"
reads = ["plan", "implement"]
next = {approve = "gate", changes = "changes_requested"}
[[steps]]
name = "gate"
kind = "decide"
part = "gate"
reads = ["plan", "implement", "review"]
next = {approve = "test", changes = "changes_requested"}
[[steps]]
name = "test"
kind = "test"
part = "tester"
reads = ["plan", "test-plan", "implement"]
next = {approve = "approved", changes = "changes_requested", absent = "approved"}
`

func TestRunScoreFlagPlaysTheNamedScoreForARunSeatedFromChairs(t *testing.T) {
	for _, changes := range []bool{false, true} {
		t.Run(fmt.Sprint(changes), func(t *testing.T) {
			f := &runCAO{fake: playCAO{changes: changes}}
			_, args := runSessionArgs(t, f)
			writeFile(t, filepath.Join(loop.Home(), "scores/ending.toml"), builtInFileScore)
			args = append(args, "-score", "ending")
			var code int
			out, err := captureStdout(t, func() error { code = execute(append([]string{"run"}, args...), io.Discard); return nil })
			want := 0
			outcome := loop.Approved
			if changes {
				want = 7
				outcome = loop.ChangesRequested
			}
			if err != nil || code != want || !strings.Contains(out, outcome) {
				t.Fatalf("code = %d, error = %v, output = %s", code, err, out)
			}
			result := recordedResult(t, onlyRunRecord(t))
			if result["score"] != "ending" || result["score_source"] != "user" {
				t.Fatalf("result = %v", result)
			}
		})
	}
}
func TestAnInvalidScoreAndTheRepositoryOptOutStopBeforeAnyChairIsSeated(t *testing.T) {
	for _, command := range []string{"run", "rehearse", "audition"} {
		for _, optOut := range []bool{false, true} {
			t.Run(command+fmt.Sprint(optOut), func(t *testing.T) {
				_, _, _, wt := smallRepositoryScore(t)
				calls := 0
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; http.Error(w, "unexpected call", 500) }))
				defer srv.Close()
				reason := "repository_scores is false"
				if optOut {
					writeFile(t, loop.UserConfigPath(loop.Home()), builtInAgentConfig+"\n[run]\nrepository_scores = false\n")
				} else {
					reason = "invalid score name"
					writeFile(t, filepath.Join(wt, ".kapellmeister/ensembles/small.toml"), strings.Replace(smallScoreEnsemble, `score = "small"`, `score = "missing"`, 1))
					reason = `unknown score "missing"`
				}
				args := []string{"-worktree", wt, "-ensemble", "small", "-task", "probe", "-cao", srv.URL}
				var err error
				switch command {
				case "run":
					err = runCommand(args)
				case "rehearse":
					err = rehearseCommand(args)
				case "audition":
					args = []string{"-worktree", wt, "-ensemble", "small", "-cao", srv.URL}
					err = auditionCommand(args)
				}
				if calls != 0 || err == nil || !strings.Contains(err.Error(), reason) {
					t.Fatalf("calls = %d, error = %v", calls, err)
				}
			})
		}
	}
}
func TestPlayPlaysTheScoreItsStandingEnsembleNames(t *testing.T) {
	s, _, members, wt := smallRepositoryScore(t)
	standing := ensemble.Standing{Ensemble: "small", Session: "file-session", Worktree: wt, Task: "probe"}
	for _, m := range members {
		standing.Chairs = append(standing.Chairs, ensemble.StandingChair{Name: m.Name, Section: m.Section, Part: m.Part, Address: m.Address("small"), Binding: m.Chair, TerminalID: m.Name, Briefed: true})
	}
	savePlayStanding(t, standing)
	fake := &playCAO{}
	srv := fake.serve(t)
	out, err := capturePlay(t, []string{standing.Session, "-cao", srv.URL})
	if err != nil || !strings.Contains(out, "approved") {
		t.Fatalf("play = %s, %v", out, err)
	}
	if len(fake.replies) != 5 || fake.replies[0] != "draft-1.md" || s.Score.Score.Name != "small" {
		t.Fatalf("replies = %v", fake.replies)
	}
}

func TestALoadedScoreIsCarriedIntoTheDetachedRunWithoutReadingItsFilesAgain(t *testing.T) {
	s, _, _, wt := smallRepositoryScore(t)
	data, err := json.Marshal(detachedLaunchRecord{Score: &s.Score})
	if err != nil {
		t.Fatal(err)
	}
	var launch detachedLaunchRecord
	if err := json.Unmarshal(data, &launch); err != nil {
		t.Fatal(err)
	}
	if launch.Score == nil {
		t.Fatal("score missing from handoff")
	}
	s.Score = *launch.Score
	s.RepositoryScores = false
	got, err := loadSelectedScore(s, wt, "small")
	if err != nil || !reflect.DeepEqual(got.Score, s.Score) {
		t.Fatalf("score was read again: %v", err)
	}
}

func TestAChairOfACustomPartIsCheckedOnceWithoutATierPolicy(t *testing.T) {
	chairs := loop.Chairs{Parts: map[string][]loop.Chair{"review": {{Provider: "codex", Profile: "km-codex"}}}}
	policy := loop.DefaultPolicy()
	got := defaultChairs(&policy, chairs)
	if len(got) != 1 || got[0].Model != "" || got[0].Tier != "" {
		t.Fatalf("chairs = %+v", got)
	}
	if variants := policy.EffortVariants(stepChairs(chairs)); len(variants) != 0 {
		t.Fatalf("variants = %+v", variants)
	}
}

func TestAFileScoresProfilesAndPreflightFollowItsStepsInsteadOfBuiltInRoleNames(t *testing.T) {
	chairs := loop.Chairs{Parts: map[string][]loop.Chair{"drafter": {{Provider: "codex", Profile: "km-codex", Address: "strings.first"}}}, Reviewers: []loop.Chair{{Provider: "codex", Profile: "km-codex", Address: "strings.second"}}}
	score := loop.Score{Source: "user", Steps: []loop.ScoreStep{{Name: "implement", Part: "drafter"}, {Name: "inspect", Part: "reviewer"}}}
	steps := stepChairs(chairs, score)
	if len(steps["implement"]) != 1 || len(steps["inspect"]) != 1 || len(steps["review"]) != 0 {
		t.Fatalf("steps = %+v", steps)
	}
	policy := loop.DefaultPolicy()
	got := defaultChairs(&policy, chairs, score)
	if len(got) != 2 || got[0].Address != "strings.first" || got[0].Model == "" || got[1].Address != "strings.second" || got[1].Model != "" {
		t.Fatalf("chairs = %+v", got)
	}
}

func TestConfigReportsWhetherRepositoryScoresAreAllowed(t *testing.T) {
	t.Setenv(loop.HomeEnv, t.TempDir())
	writeFile(t, loop.UserConfigPath(loop.Home()), builtInAgentConfig+"\n[run]\nrepository_scores = false\n")
	out, err := captureStdout(t, func() error { return configCommand([]string{"-cao", unreachableCAO}) })
	if err != nil || !strings.Contains(out, "repository scores off") {
		t.Fatalf("config = %s, %v", out, err)
	}
}
