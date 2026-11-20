package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jystringfellow/kapellmeister/internal/loop"
)

func reviewRoleArgs() []string {
	return []string{"-reviewer", "p:reviewer:m,p:other:m", "-gate", "p:gate:m", "-classifier", "p:classifier:m"}
}
func TestKmRunScoreReviewEndsApprovedOrChangesRequestedWithItsExitStatus(t *testing.T) {
	for _, changes := range []bool{false, true} {
		t.Run(fmt.Sprint(changes), func(t *testing.T) {
			f := &runCAO{fake: playCAO{changes: changes}}
			wt, args := runSessionArgs(t, f)
			args = append(args[:6:6], "-score", "review")
			args = append(args, reviewRoleArgs()...)
			writeFile(t, filepath.Join(wt, "change"), "existing change\n")
			before, _ := gitOutput(wt, "status", "--porcelain=v1", "--untracked-files=all")
			var code int
			out, err := captureStdout(t, func() error { code = execute(append([]string{"run"}, args...), io.Discard); return nil })
			want, outcome := 0, loop.Approved
			if changes {
				want, outcome = 7, loop.ChangesRequested
			}
			if err != nil || code != want || !strings.Contains(out, outcome) {
				t.Fatalf("code = %d, err = %v, output = %s", code, err, out)
			}
			if changes && !strings.Contains(out, "gate-1.md") {
				t.Fatal(out)
			}
			dir := onlyRunRecord(t)
			result := recordedResult(t, dir)
			if result["score"] != "review" || result["score_source"] != "built-in" {
				t.Fatal(result)
			}
			after, _ := gitOutput(wt, "status", "--porcelain=v1", "--untracked-files=all")
			data, _ := os.ReadFile(filepath.Join(wt, "change"))
			if before != after || string(data) != "existing change\n" {
				t.Fatalf("change altered: %s, %s", after, data)
			}
			rows := partRows(t, dir)
			parts := map[string]bool{}
			for _, r := range rows {
				parts[r.(map[string]any)["part"].(string)] = true
			}
			if !reflect.DeepEqual(parts, map[string]bool{"reviewer": true, "gate": true, "classifier": true}) {
				t.Fatal(parts)
			}
			text, diag := captureReport(t, dir)
			if diag != "" || !strings.Contains(text, outcome) {
				t.Fatalf("report = %s %s", text, diag)
			}
			if changes {
				for _, jsonFlag := range []bool{false, true} {
					waitArgs := []string{result["run_id"].(string), filepath.Dir(dir)}
					if jsonFlag {
						waitArgs = append(waitArgs, "-json")
					}
					text, diag, code := waitSubprocess(t, waitArgs...)
					if code != 7 || diag != "" || !strings.Contains(text, loop.ChangesRequested) {
						t.Fatalf("wait = %d %s %s", code, text, diag)
					}
				}
			}
		})
	}
}
func TestReviewScoreRefusalsHappenBeforeAnyChairIsSeated(t *testing.T) {
	for _, reason := range []string{"no worktree", "clean", "extra flag", "reserved user", "reserved repository", "ensemble extra part", "ensemble no worktree"} {
		t.Run(reason, func(t *testing.T) {
			standing := playStanding(t)
			writeFile(t, loop.UserConfigPath(loop.Home()), builtInAgentConfig)
			wt := standing.Worktree
			var mu sync.Mutex
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				calls++
				mu.Unlock()
				http.Error(w, "unexpected call", 500)
			}))
			defer srv.Close()
			args := []string{"-task", "probe", "-worktree", wt, "-score", "review", "-cao", srv.URL}
			args = append(args, reviewRoleArgs()...)
			want := "no uncommitted change"
			switch reason {
			case "no worktree":
				args[2] = "-repo"
				want = "pass -worktree"
			case "extra flag":
				args = append(args, "-planner", "p:planner:m")
				want = "no planner part"
			case "reserved user":
				writeFile(t, filepath.Join(loop.Home(), "scores/review.toml"), "invalid")
				want = "built-in name is reserved"
			case "reserved repository":
				writeFile(t, filepath.Join(wt, ".kapellmeister/scores/review.toml"), "invalid")
				for _, gitArgs := range [][]string{{"add", "-f", ".kapellmeister/scores/review.toml"}, {"-c", "commit.gpgsign=false", "commit", "-qm", "fixture"}} {
					if _, err := gitOutput(wt, gitArgs...); err != nil {
						t.Fatal(err)
					}
				}
				want = "built-in name is reserved"
			case "ensemble extra part", "ensemble no worktree":
				text := `score = "review"
[sections.strings.chairs.first]
part = "reviewer"
agents = ["codex"]
[sections.strings.chairs.second]
part = "gate"
agents = ["codex"]
[sections.strings.chairs.third]
part = "classifier"
agents = ["codex"]
`
				if reason == "ensemble extra part" {
					text += "[sections.strings.chairs.extra]\npart = \"planner\"\nagents = [\"codex\"]\n"
					want = `plays unknown part "planner"`
				} else {
					want = "pass -worktree"
				}
				writeFile(t, filepath.Join(wt, ".kapellmeister/ensembles/inspect.toml"), text)
				args = []string{"-task", "probe", "-worktree", wt, "-ensemble", "inspect", "-cao", srv.URL}
				if reason == "ensemble no worktree" {
					args[2] = "-repo"
				}
			}
			err := runAt(context.Background(), args, io.Discard, io.Discard, nil)
			mu.Lock()
			n := calls
			mu.Unlock()
			if err == nil || !strings.Contains(err.Error(), want) || n != 0 {
				t.Fatalf("error = %v, calls = %d, want %s", err, n, want)
			}
		})
	}
}

type reviewStatusStepper struct {
	inspect func(loop.Step)
	mu      sync.Mutex
}

func (f *reviewStatusStepper) Run(_ context.Context, s loop.Step) (loop.StepResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.inspect != nil {
		f.inspect(s)
	}
	return loop.StepResult{Status: "completed", TerminalID: "terminal"}, os.WriteFile(filepath.Join(s.Dir, s.ReplyPath), []byte("APPROVE\n"), 0600)
}
func (*reviewStatusStepper) Release(context.Context, string) error { return nil }
func TestStatusJSONForAReviewRunShowsOnlyItsStepsAndChairsWithCurrentAndTrail(t *testing.T) {
	for _, strict := range []bool{false, true} {
		t.Run(fmt.Sprint(strict), func(t *testing.T) {
			standing := playStanding(t)
			writeFile(t, loop.UserConfigPath(loop.Home()), builtInAgentConfig)
			wt := standing.Worktree
			settings, err := loop.LoadSettings(loop.Home(), wt)
			if err != nil {
				t.Fatal(err)
			}
			settings.ScoreName = "review"
			roles := noRoleFlags(t)
			for _, part := range []string{"reviewer", "gate", "classifier"} {
				*roles[part] = "p:" + part + ":m"
			}
			settings, chairs, members, err := lineUp(settings, wt, "", roles)
			if err != nil {
				t.Fatal(err)
			}
			dir := filepath.Join(loop.StateDir(), "runs", "review-run")
			f := &reviewStatusStepper{}
			seen := map[string]bool{}
			f.inspect = func(s loop.Step) {
				row := statusDataRow(t, filepath.Dir(dir))
				doc, _ := json.Marshal(row)
				for _, absent := range []string{"plan", "test-plan", "implement", "test", "planner", "implementer", "tester"} {
					if strings.Contains(string(doc), `"`+absent+`"`) {
						t.Fatalf("unexpected %s: %s", absent, doc)
					}
				}
				outline := row["score"].(map[string]any)
				var names []string
				for _, v := range outline["steps"].([]any) {
					names = append(names, v.(map[string]any)["name"].(string))
				}
				want := []string{"verify", "review", "gate"}
				if strict {
					want = append(want, "strict")
				}
				if !reflect.DeepEqual(names, want) {
					t.Fatalf("steps = %v", names)
				}
				liveChairs := dataChairs(t, row)
				if len(liveChairs) != 3 || len(row["current"].([]any)) == 0 || len(row["trail"].([]any)) == 0 {
					t.Fatalf("status = %s", doc)
				}
				seen[s.Name] = true
			}
			strictCmd := ""
			if strict {
				strictCmd = "true"
			}
			sum, err := runEnsemble(context.Background(), ensembleRun{Score: settings.Score.Score, Members: members, RunID: "review-run", StateDir: dir, Stepper: f, Verify: func(context.Context, string) (loop.VerifyResult, error) { return loop.VerifyResult{}, nil }, Progress: io.Discard, Config: loop.Config{RunID: "review-run", Task: "probe", Worktree: wt, Chairs: chairs, MaxRounds: 1, VerifyCommand: "true", StrictCommand: strictCmd, ReplyGrace: time.Millisecond}})
			if err != nil || sum.Outcome != loop.Approved || !seen["review"] || !seen["gate"] {
				t.Fatalf("result = %+v, %v, seen = %v", sum, err, seen)
			}
		})
	}
}
func TestKmScoreShowPrintsTheReviewScoreAndItsPartFiles(t *testing.T) {
	var diag bytes.Buffer
	code := 0
	out, err := captureStdout(t, func() error { code = execute([]string{"score", "show", "review"}, &diag); return nil })
	if err != nil || code != 0 || diag.Len() != 0 {
		t.Fatalf("code = %d, err = %v, diag = %s", code, err, &diag)
	}
	for _, text := range []string{"==> scores/review.toml <==", "==> parts/reviewer.md <==", "==> parts/gate.md <==", `next = { approve = "approved", changes = "changes_requested" }`, "{verification}", "{reply}"} {
		if !strings.Contains(out, text) {
			t.Fatalf("missing %s: %s", text, out)
		}
	}
}
func TestKmScoreShowRefusesUnknownNamesAndMissingArguments(t *testing.T) {
	for _, args := range [][]string{{"score", "show"}, {"score", "show", "missing"}} {
		var diag bytes.Buffer
		want := 1
		if len(args) == 2 {
			want = 2
		}
		code := execute(args, &diag)
		if code != want || diag.Len() == 0 {
			t.Fatalf("%v: code = %d, diag = %s", args, code, &diag)
		}
	}
}

func TestTheReviewScoreHonorsGateWhenInAConfiguredEnsemble(t *testing.T) {
	standing := playStanding(t)
	settings, err := loop.LoadSettings(loop.Home(), standing.Worktree)
	if err != nil {
		t.Fatal(err)
	}
	settings.ScoreName = "review"
	settings.Ensembles = map[string]loop.Ensemble{"inspect": {GateWhen: "always"}}
	roles := noRoleFlags(t)
	for _, part := range []string{"reviewer", "gate", "classifier"} {
		*roles[part] = "p:" + part + ":m"
	}
	settings, _, _, err = lineUp(settings, standing.Worktree, "inspect", roles)
	if err != nil || settings.GateWhen != "always" {
		t.Fatalf("gate_when = %q, error = %v", settings.GateWhen, err)
	}
}
