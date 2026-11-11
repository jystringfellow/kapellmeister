package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
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

const recordedEnsemble = `
[sections.leads.chairs.planner]
part = "planner"
agents = ["p:planner:m"]

[sections.leads.chairs.implementer]
part = "implementer"
agents = ["p:implementer:m"]

[sections.review.chairs.security]
part = "reviewer"
agents = ["p:reviewer:m"]

[sections.review.chairs.style]
part = "reviewer"
agents = ["p:reviewer:m"]

[sections.leads.chairs.gate]
part = "gate"
agents = ["p:gate:m"]

[sections.leads.chairs.classifier]
part = "classifier"
agents = ["p:classifier:m"]
`

func onlyRunRecord(t *testing.T) string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(loop.StateDir(), "runs"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("run records = %v, %v", entries, err)
	}
	return filepath.Join(loop.StateDir(), "runs", entries[0].Name())
}

func recordedResult(t *testing.T, dir string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "result.json"))
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func recordedSteps(t *testing.T, dir string) []loop.Record {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "telemetry.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var steps []loop.Record
	lines := bufio.NewScanner(bytes.NewReader(data))
	lines.Buffer(nil, len(data)+1)
	for lines.Scan() {
		var rec loop.Record
		if err := json.Unmarshal(lines.Bytes(), &rec); err != nil {
			t.Fatal(err)
		}
		if rec.Chair != nil {
			steps = append(steps, rec)
		}
	}
	if len(steps) == 0 {
		t.Fatal("no step records")
	}
	return steps
}

func setOrigin(t *testing.T, worktree, remote string) {
	t.Helper()
	if out, err := exec.Command("git", "-C", worktree, "remote", "add", "origin", remote).CombinedOutput(); err != nil {
		t.Fatalf("git remote add: %v: %s", err, out)
	}
}

type replyingStepper struct{}

func (replyingStepper) Run(_ context.Context, step loop.Step) (loop.StepResult, error) {
	text := "reply\n"
	if strings.HasSuffix(step.ReplyPath, ".verdict") {
		text = "APPROVE"
	}
	if err := os.WriteFile(filepath.Join(step.Dir, step.ReplyPath), []byte(text), 0o600); err != nil {
		return loop.StepResult{}, err
	}
	return loop.StepResult{TerminalID: "terminal", Status: "completed"}, nil
}

func (replyingStepper) Release(context.Context, string) error { return nil }

func recordRun(t *testing.T, worktree, ensembleName string, chairs loop.Chairs) string {
	t.Helper()
	dir := filepath.Join(loop.StateDir(), "runs", "recorded")
	sum, err := runEnsemble(context.Background(), ensembleRun{
		RunID: "recorded", StateDir: dir, Ensemble: ensembleName, Stepper: replyingStepper{}, Progress: io.Discard,
		Config: loop.Config{RunID: "recorded", Task: "probe", Worktree: worktree, Chairs: chairs, MaxRounds: 1, ReplyGrace: time.Millisecond},
	})
	if err != nil || sum.Outcome != loop.Approved {
		t.Fatalf("run = %+v, %v", sum, err)
	}
	return dir
}

func plainChairs() loop.Chairs {
	chair := loop.Chair{Provider: "p", Profile: "chair", Model: "m"}
	return loop.Chairs{Planner: chair, Implementer: chair, Reviewers: []loop.Chair{chair}, Gate: chair, Classifier: chair}
}

func chairsFromEnsembleFile(t *testing.T, worktree string) loop.Chairs {
	t.Helper()
	writeFile(t, filepath.Join(loop.Home(), ensemble.UserDir, "thorough.toml"), recordedEnsemble)
	s, err := loop.LoadSettings(loop.Home(), worktree)
	if err != nil {
		t.Fatal(err)
	}
	_, chairs, members, err := lineUp(s, worktree, "thorough", noRoleFlags(t))
	if err != nil {
		t.Fatal(err)
	}
	chairs, err = addressedChairs(worktree, "thorough", chairs, members)
	if err != nil {
		t.Fatal(err)
	}
	return chairs
}

var recordedAddresses = map[string][]string{
	"plan": {"leads.planner@thorough"}, "implement": {"leads.implementer@thorough"},
	"review": {"review.security@thorough", "review.style@thorough"}, "gate": {"leads.gate@thorough"}, "lessons": {"leads.gate@thorough"},
	"classify-review": {"leads.classifier@thorough"}, "classify-gate": {"leads.classifier@thorough"},
}

func requireRecordedAddresses(t *testing.T, dir string, want map[string][]string, chairs int) {
	t.Helper()
	seen := map[string]bool{}
	for _, rec := range recordedSteps(t, dir) {
		if !slices.Contains(want[rec.Step], rec.Chair.Address) {
			t.Errorf("step %s address = %q, want one of %v", rec.Step, rec.Chair.Address, want[rec.Step])
		}
		seen[rec.Chair.Address] = true
	}
	if len(seen) != chairs {
		t.Errorf("addresses recorded = %v, want %d chairs", seen, chairs)
	}
}

func standingAddresses(s ensemble.Standing) map[string][]string {
	steps := map[string][]string{"planner": {"plan"}, "implementer": {"implement"}, "reviewer": {"review"}, "gate": {"gate", "lessons"}, "classifier": {"classify-review", "classify-gate"}}
	want := map[string][]string{}
	for _, c := range s.Chairs {
		for _, step := range steps[c.Part] {
			want[step] = append(want[step], c.Address)
		}
	}
	return want
}

func TestAResultNamesTheEnsembleItsRunUsed(t *testing.T) {
	s := playStanding(t)
	result := recordedResult(t, recordRun(t, s.Worktree, "thorough", plainChairs()))
	if result["ensemble"] != "thorough" {
		t.Errorf("ensemble = %v, want thorough", result["ensemble"])
	}
}

func TestAResultOfARunFromPlainChairsNamesNoEnsemble(t *testing.T) {
	s := playStanding(t)
	if name, ok := recordedResult(t, recordRun(t, s.Worktree, "", plainChairs()))["ensemble"]; ok {
		t.Errorf("ensemble = %v, want it omitted", name)
	}
}

func TestARepositoryIsItsOriginsHostAndPathWithoutSchemeCredentialsOrGitSuffix(t *testing.T) {
	for remote, want := range map[string]string{
		"https://github.com/owner/name.git":                "github.com/owner/name",
		"https://github.com/owner/name":                    "github.com/owner/name",
		"https://user:secret@github.com/owner/name.git":    "github.com/owner/name",
		"https://token@GitHub.com/owner/name/":             "github.com/owner/name",
		"git@github.com:owner/name.git":                    "github.com/owner/name",
		"user:secret@github.com:owner/name.git":            "github.com/owner/name",
		"github.com:owner/name":                            "github.com/owner/name",
		"ssh://git@github.com/owner/name.git":              "github.com/owner/name",
		"ssh://git:secret@github.com:2222/owner/name.git":  "github.com/owner/name",
		"https://org@dev.azure.com/org/project/_git/name":  "dev.azure.com/org/project/_git/name",
		"https://user:sec%ZZret@github.com/owner/name.git": "",
		"first@second@github.com:owner/name.git":           "",
		"/srv/git/name.git":                                "",
		"file:///srv/git/name.git":                         "",
		"":                                                 "",
	} {
		if got := repositoryOf(remote); got != want {
			t.Errorf("repositoryOf(%q) = %q, want %q", remote, got, want)
		}
	}
}

func TestAResultIdentifiesTheRepositoryAndNoRunRecordHoldsItsCredentials(t *testing.T) {
	s := playStanding(t)
	setOrigin(t, s.Worktree, "https://user:s3cret@example.test/owner/name.git")
	dir := recordRun(t, s.Worktree, "", plainChairs())
	result := recordedResult(t, dir)
	if result["repository"] != "example.test/owner/name" {
		t.Errorf("repository = %v, want example.test/owner/name", result["repository"])
	}
	if base, _ := result["base"].(string); len(base) != 40 {
		t.Errorf("base = %v, want the starting commit", result["base"])
	}
	for _, name := range []string{"result.json", "config.json", "telemetry.jsonl"} {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), "s3cret") || strings.Contains(string(data), "user:") {
			t.Errorf("%s holds the origin's credentials", name)
		}
	}
}

func TestAResultOmitsTheRepositoryWhenTheWorktreeHasNoOrigin(t *testing.T) {
	s := playStanding(t)
	if repository, ok := recordedResult(t, recordRun(t, s.Worktree, "", plainChairs()))["repository"]; ok {
		t.Errorf("repository = %v, want it omitted", repository)
	}
}

func TestEachStepRecordOfARunFromAnEnsembleFileCarriesItsChairsAddress(t *testing.T) {
	s := playStanding(t)
	dir := recordRun(t, s.Worktree, "thorough", chairsFromEnsembleFile(t, s.Worktree))
	requireRecordedAddresses(t, dir, recordedAddresses, 6)
}

func TestEachStepRecordOfAStandingEnsembleCarriesItsChairsAddress(t *testing.T) {
	s := playStanding(t)
	chairs, err := s.RebuildChairs()
	if err != nil {
		t.Fatal(err)
	}
	requireRecordedAddresses(t, recordRun(t, s.Worktree, s.Ensemble, chairs), standingAddresses(s), len(s.Chairs))
}

func TestAStepRecordOfARunWithoutAnEnsembleFileCarriesNoAddress(t *testing.T) {
	s := playStanding(t)
	settings, err := loop.LoadSettings(loop.Home(), s.Worktree)
	if err != nil {
		t.Fatal(err)
	}
	_, chairs, members, err := lineUp(settings, s.Worktree, "", noRoleFlags(t, "-planner", "p:a:m", "-implementer", "p:a:m", "-reviewer", "p:a:m", "-gate", "p:a:m", "-classifier", "p:a:m"))
	if err != nil {
		t.Fatal(err)
	}
	if chairs, err = addressedChairs(s.Worktree, "", chairs, members); err != nil {
		t.Fatal(err)
	}
	for _, rec := range recordedSteps(t, recordRun(t, s.Worktree, "", chairs)) {
		if rec.Chair.Address != "" {
			t.Errorf("step %s address = %q, want none", rec.Step, rec.Chair.Address)
		}
	}
}

func TestRunWithAnEnsembleFileRecordsItsNameAndEachChairsAddress(t *testing.T) {
	f := &runCAO{}
	_, args := runSessionArgs(t, f)
	writeFile(t, filepath.Join(loop.Home(), ensemble.UserDir, "thorough.toml"), recordedEnsemble)
	if err := runCommand(append(args[:6:6], "-ensemble", "thorough")); err != nil {
		t.Fatal(err)
	}
	dir := onlyRunRecord(t)
	if name := recordedResult(t, dir)["ensemble"]; name != "thorough" {
		t.Errorf("ensemble = %v, want thorough", name)
	}
	requireRecordedAddresses(t, dir, recordedAddresses, 6)
}

func TestPlayRecordsTheStandingEnsemblesNameAndEachChairsAddress(t *testing.T) {
	s := playStanding(t)
	srv := (&playCAO{}).serve(t)
	if out, err := capturePlay(t, []string{s.Session, "-cao", srv.URL}); err != nil {
		t.Fatalf("play: %v\n%s", err, out)
	}
	dir := onlyRunRecord(t)
	if name := recordedResult(t, dir)["ensemble"]; name != s.Ensemble {
		t.Errorf("ensemble = %v, want %s", name, s.Ensemble)
	}
	requireRecordedAddresses(t, dir, standingAddresses(s), len(s.Chairs))
}

func TestARunFromAnEnsembleFilePreflightsChairsThatShareABindingOnce(t *testing.T) {
	s, wt := ensembleWorktree(t, "")
	writeFile(t, filepath.Join(loop.Home(), ensemble.UserDir, "thorough.toml"), recordedEnsemble)
	_, chairs, _, err := lineUp(s, wt, "thorough", noRoleFlags(t))
	if err != nil {
		t.Fatal(err)
	}
	if unique := loop.UniqueChairs(stepChairs(chairs)["review"]...); len(unique) != 1 {
		t.Errorf("preflight reviewers = %+v, want the shared binding once", unique)
	}
}

func readCurrent(t *testing.T, dir string) runState {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "current.json"))
	if err != nil {
		t.Fatal(err)
	}
	var state runState
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatal(err)
	}
	return state
}

type observingStepper struct{ observe func(loop.Step) }

func (s observingStepper) Run(ctx context.Context, step loop.Step) (loop.StepResult, error) {
	s.observe(step)
	return (replyingStepper{}).Run(ctx, step)
}
func (observingStepper) Release(context.Context, string) error { return nil }

func TestARunRecordsItsEnsembleRepositoryAndProcessBeforeAnyStepEnds(t *testing.T) {
	for _, command := range []string{"run", "play"} {
		t.Run(command, func(t *testing.T) {
			standing := playStanding(t)
			setOrigin(t, standing.Worktree, "https://user:secret@host/owner/app.git")
			dir := filepath.Join(loop.StateDir(), "runs", "live")
			observed := false
			stepper := observingStepper{observe: func(step loop.Step) {
				s := readCurrent(t, dir)
				if s.RunID != "live" || s.Ensemble != "thorough" || s.Repository != "host/owner/app" || s.Command != command || s.PID != os.Getpid() || s.ProcessStart == "" || s.Host == "" || s.Started.IsZero() {
					t.Errorf("state = %+v", s)
				}
				if step.Name == "plan" {
					observed = true
					data, err := os.ReadFile(filepath.Join(dir, "telemetry.jsonl"))
					lines := strings.Split(strings.TrimSpace(string(data)), "\n")
					if err != nil || len(lines) != 1 || !strings.Contains(lines[0], `"step":"start"`) {
						t.Errorf("first step already ended: %s %v", data, err)
					}
				}
				found := false
				for _, active := range s.Playing {
					if active.Step == step.Name && active.ChairName == step.ChairName && active.Chair == step.Chair.Address {
						found = true
					}
				}
				if !found {
					t.Errorf("step %+v absent from %+v", step, s)
				}
			}}
			sum, err := runEnsemble(context.Background(), ensembleRun{RunID: "live", Command: command, StateDir: dir, Ensemble: "thorough", Stepper: stepper, Progress: io.Discard, Config: loop.Config{RunID: "live", Task: "probe", Worktree: standing.Worktree, Chairs: plainChairs(), MaxRounds: 1}})
			if err != nil || sum.Outcome != loop.Approved || !observed {
				t.Fatalf("%+v %v observed %v", sum, err, observed)
			}
			s := readCurrent(t, dir)
			if len(s.Playing) != 0 || s.Round != 1 || s.Updated.Before(s.Started) {
				t.Fatalf("final state %+v", s)
			}
			rows := reportRows(t, dir)
			if len(rows) != 1 || rows[0].(map[string]any)["outcome"] != "approved" {
				t.Fatal(rows)
			}
		})
	}
}

func TestCurrentJSONIsReplacedAtomically(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(t.TempDir(), "sentinel")
	writeFile(t, target, "unchanged")
	if err := os.Symlink(target, filepath.Join(dir, "current.json")); err != nil {
		t.Fatal(err)
	}
	s := runState{RunID: "atomic", Started: time.Now(), Playing: []loop.ActiveStep{}}
	if err := writeRunState(dir, s); err != nil {
		t.Fatal(err)
	}
	if body, err := os.ReadFile(target); err != nil || string(body) != "unchanged" {
		t.Fatalf("target changed: %s %v", body, err)
	}
	old, err := os.Open(filepath.Join(dir, "current.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer old.Close()
	s.RunID = "replacement"
	if err := writeRunState(dir, s); err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(old)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "atomic") || readCurrent(t, dir).RunID != "replacement" {
		t.Fatalf("not replaced atomically: %s", body)
	}
}

func TestANewRunIDCarriesTheUTCTimeItWasMade(t *testing.T) {
	previous := time.Local
	time.Local = time.FixedZone("non-UTC", 9*60*60)
	t.Cleanup(func() { time.Local = previous })
	before := time.Now().UTC().Truncate(time.Second)
	id := newRunID()
	after := time.Now().UTC()
	if len(id) != 22 {
		t.Fatalf("run id = %q", id)
	}
	started, err := time.Parse("20060102-150405", id[:15])
	if err != nil || started.Before(before) || started.After(after) {
		t.Fatalf("run id = %q, start = %v, want between %v and %v, error = %v", id, started, before, after, err)
	}
}
