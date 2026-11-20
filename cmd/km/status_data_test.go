package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jystringfellow/kapellmeister/internal/ensemble"
	"github.com/jystringfellow/kapellmeister/internal/loop"
)

func statusDataFixture(t *testing.T) (string, map[string]any) {
	t.Helper()
	root := t.TempDir()
	s := statusFixture(t, root, "data")
	b, _ := json.Marshal(s)
	var state map[string]any
	json.Unmarshal(b, &state)

	var seating, score any
	json.Unmarshal([]byte(`{"name":"voices","source":"repository","sections":[{"name":"strings","chairs":[{"address":"strings.pen@voices","name":"pen","part":"scribe","agent":"solo","provider":"p","model":"m","tier":"medium","memory":"resume"}]},{"name":"winds","chairs":[{"address":"winds.one@voices","name":"one","part":"auditor","memory":"fresh"},{"address":"winds.two@voices","name":"two","part":"auditor","memory":"fresh"},{"address":"winds.air@voices","name":"air","part":"breath","memory":"fresh"},{"address":"winds.seal@voices","name":"seal","part":"signer","memory":"fresh"}]}]}`), &seating)
	json.Unmarshal([]byte(`{"name":"voices","source":"built-in","rounds":4,"steps":[{"name":"verify","kind":"verify"},{"name":"draft","kind":"produce","part":"scribe","next":{"done":"audit"}},{"name":"air","kind":"produce","part":"breath"},{"name":"audit","kind":"review","part":"auditor","next":{"approve":"sign-off","changes":"audit"}},{"name":"sign-off","kind":"decide","part":"signer","next":{"approve":"approved"}}]}`), &score)
	state["seating"], state["score"], state["cao"] = seating, score, "http://127.0.0.1:12345"
	state["playing"] = []any{
		map[string]any{"step": "audit", "chair_name": "one", "round": 2, "started": statusNow.Add(-time.Minute)},
		map[string]any{"step": "audit", "chair": "winds.two@voices", "round": 2, "terminal": "second", "started": statusNow.Add(-time.Second)},
		map[string]any{"step": "air", "round": 0, "started": statusNow.Add(-2 * time.Minute)},
	}
	saveStatusData(t, root, state)
	writeFile(t, filepath.Join(root, "data", "telemetry.jsonl"), `{"at":"2026-11-11T05:00:00Z","step":"start"}
{"at":"2026-11-11T05:01:00Z","step":"verify-baseline","exit_code":0,"duration_ms":11}
{"at":"2026-11-11T05:02:00Z","step":"draft","round":1,"chair":{"provider":"p"},"terminal_id":"pen-terminal","kept":true,"usage":{"known":true,"input_tokens":12,"output_tokens":3,"cache_read_tokens":0,"cache_write_tokens":0},"duration_ms":22}
{"at":"2026-11-11T05:03:00Z","step":"audit","round":1,"chair_name":"one","chair":{"provider":"p"},"usage":{"known":false},"duration_ms":33}
{"at":"2026-11-11T05:03:01Z","step":"verdict-audit","round":1,"chair_name":"one","verdict":"CHANGES"}
{"at":"2026-11-11T05:04:00Z","step":"audit","round":1,"chair":{"address":"winds.two@voices"},"chair_name":"two","usage":{"known":true,"input_tokens":2,"output_tokens":1,"cache_read_tokens":0,"cache_write_tokens":0},"duration_ms":44}
{"at":"2026-11-11T05:04:01Z","step":"verdict-audit","round":1,"chair_name":"two","verdict":"APPROVE"}
{"at":"2026-11-11T05:05:00Z","step":"audit-nudge","round":1,"chair_name":"one","chair":{"provider":"p"},"duration_ms":1}
{"at":"2026-11-11T05:06:00Z","step":"audit","round":1,"chair_name":"one","chair":{"provider":"p"},"error":"stopped","duration_ms":1}
{"at":"2026-11-11T05:07:00Z","step":"blocked","chair_name":"one","blocked_step":"audit","terminal_id":"prompt-one","detail":"allow a tool"}
{"at":"2026-11-11T05:08:00Z","step":"blocked","chair_name":"one","blocked_step":"audit","terminal_id":"prompt-two","detail":"choose a value"}
{"at":"2026-11-11T05:09:00Z","step":"unblocked","terminal_id":"prompt-one"}
`)
	return root, state
}

func saveStatusData(t *testing.T, root string, state map[string]any) {
	t.Helper()
	b, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "data", "current.json"), string(b))
}

func statusDataRow(t *testing.T, root string) map[string]any {
	t.Helper()
	out, diag := captureStatus(t, "-json", root)
	if diag != "" {
		t.Fatal(diag)
	}
	var doc struct {
		Runs []map[string]any `json:"runs"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Runs) != 1 {
		t.Fatal(out)
	}
	return doc.Runs[0]
}

func dataChairs(t *testing.T, r map[string]any) []map[string]any {
	t.Helper()
	seating, ok := r["ensemble"].(map[string]any)
	if !ok {
		t.Fatalf("missing ensemble: %+v", r)
	}
	var chairs []map[string]any
	for _, sec := range seating["sections"].([]any) {
		for _, c := range sec.(map[string]any)["chairs"].([]any) {
			chairs = append(chairs, c.(map[string]any))
		}
	}
	return chairs
}

func TestStatusJSONReportsEnsembleAsAnObjectWithItsNameSourceAndSectionsAndNoSeatingKey(t *testing.T) {
	root, _ := statusDataFixture(t)
	r := statusDataRow(t, root)
	seating, ok := r["ensemble"].(map[string]any)
	if !ok {
		t.Fatalf("ensemble = %v, want an object", r["ensemble"])
	}
	if len(seating) != 3 || seating["name"] != "thorough" || seating["source"] != "repository" {
		t.Fatal(seating)
	}
	sections := seating["sections"].([]any)
	if len(sections) != 2 || sections[0].(map[string]any)["name"] != "strings" || sections[1].(map[string]any)["name"] != "winds" {
		t.Fatal(sections)
	}
	if len(sections[0].(map[string]any)["chairs"].([]any)) != 1 || len(sections[1].(map[string]any)["chairs"].([]any)) != 4 {
		t.Fatal(sections)
	}
	if _, exists := r["seating"]; exists {
		t.Fatal("unexpected seating key")
	}
}

func TestStatusJSONReportsARunRecordedBeforeSeatingWithItsEnsembleNameAnEmptySourceAndAnEmptySectionsList(t *testing.T) {
	for _, current := range []bool{true, false} {
		t.Run(map[bool]string{true: "with current record", false: "without current record"}[current], func(t *testing.T) {
			root := t.TempDir()
			name := ""
			if current {
				name = statusFixture(t, root, "old").Ensemble
			} else {
				writeFile(t, filepath.Join(root, "old", "telemetry.jsonl"), `{"at":"2026-11-09T14:00:00Z","step":"start"}`+"\n")
			}
			r := statusDataRow(t, root)
			want := map[string]any{"name": name, "source": "", "sections": []any{}}
			if !reflect.DeepEqual(r["ensemble"], want) {
				t.Fatalf("ensemble = %#v, want %#v", r["ensemble"], want)
			}
			if _, exists := r["seating"]; exists {
				t.Fatal("unexpected seating key")
			}
		})
	}
}

func TestStatusJSONKeepsEveryFieldItPrintedBeforeWithTheSameMeaning(t *testing.T) {
	root, _ := statusDataFixture(t)
	r := statusDataRow(t, root)
	for k, want := range map[string]any{"schema": float64(1), "run_id": "data", "command": "run", "round": float64(2), "max_rounds": float64(3), "liveness": "alive", "repository": "host/owner/app"} {
		if r[k] != want {
			t.Errorf("%s = %v, want %v", k, r[k], want)
		}
	}
	if r["ensemble"].(map[string]any)["name"] != "thorough" {
		t.Fatal(r["ensemble"])
	}
	if len(r["playing"].([]any)) != 3 || r["started"] == nil || r["updated"] == nil || r["path"] == nil {
		t.Fatal(r)
	}
}

func TestStatusJSONDrawsAScoreWhosePartsAndStepsHaveOtherNames(t *testing.T) {
	root, _ := statusDataFixture(t)
	r := statusDataRow(t, root)
	chairs := dataChairs(t, r)
	c := chairs[0]
	for k, want := range map[string]any{"address": "strings.pen@voices", "name": "pen", "part": "scribe", "agent": "solo", "provider": "p", "model": "m", "tier": "medium", "memory": "resume", "state": "done", "step": "draft", "terminal": "pen-terminal", "since": "2026-11-11T05:02:00Z"} {
		if c[k] != want {
			t.Errorf("%s = %v, want %v", k, c[k], want)
		}
	}
	if chairs[1]["state"] != "playing" || chairs[2]["state"] != "playing" || chairs[3]["state"] != "playing" || chairs[4]["state"] != "waiting" {
		t.Fatal(chairs)
	}
	if r["cao"] != "http://127.0.0.1:12345" || !reflect.DeepEqual(r["score"].(map[string]any)["steps"].([]any)[1].(map[string]any)["next"], map[string]any{"done": "audit"}) {
		t.Fatal(r)
	}
}

func TestStatusJSONListsEveryStepPlayingAtOnceInCurrent(t *testing.T) {
	root, s := statusDataFixture(t)
	r := statusDataRow(t, root)
	current, ok := r["current"].([]any)
	if !ok || len(current) != 2 {
		t.Fatal(r)
	}
	if current[0].(map[string]any)["step"] != "air" || !reflect.DeepEqual(current[1].(map[string]any)["chairs"], []any{"winds.one@voices", "winds.two@voices"}) {
		t.Fatal(current)
	}
	s["playing"] = []any{map[string]any{"step": "verify-2", "round": 2, "started": statusNow}}
	saveStatusData(t, root, s)
	current = statusDataRow(t, root)["current"].([]any)
	if current[0].(map[string]any)["step"] != "verify" {
		t.Fatal(current)
	}
}

func TestStatusJSONTrailListsFinishedStepsInOrderWithExitChairAndDuration(t *testing.T) {
	root, _ := statusDataFixture(t)
	r := statusDataRow(t, root)
	trail, ok := r["trail"].([]any)
	if !ok || len(trail) != 4 {
		t.Fatal(r)
	}
	for i, want := range []map[string]any{
		{"step": "verify", "round": float64(0), "exit": "pass", "chair": "", "duration_ms": float64(11)},
		{"step": "draft", "round": float64(1), "exit": "done", "chair": "strings.pen@voices", "duration_ms": float64(22)},
		{"step": "audit", "round": float64(1), "exit": "changes", "chair": "winds.one@voices", "duration_ms": float64(33)},
		{"step": "audit", "round": float64(1), "exit": "approve", "chair": "winds.two@voices", "duration_ms": float64(44)},
	} {
		if !reflect.DeepEqual(trail[i], want) {
			t.Errorf("%v want %v", trail[i], want)
		}
	}
}

func TestStatusJSONUsageSaysWhetherAllSomeOrNoneOfTheStepsWereMeasured(t *testing.T) {
	root, _ := statusDataFixture(t)
	r := statusDataRow(t, root)
	chairs := dataChairs(t, r)
	for i, want := range []string{"all", "none", "all", "none", "none"} {
		if chairs[i]["usage"].(map[string]any)["measured"] != want {
			t.Fatal(chairs)
		}
	}
	if !reflect.DeepEqual(r["usage"], map[string]any{"input_tokens": float64(14), "output_tokens": float64(4), "measured": "some"}) {
		t.Fatal(r["usage"])
	}
}

func TestStatusJSONCountsAChairBlockedOnAPromptAsNeedingInputWithItsReason(t *testing.T) {
	root, _ := statusDataFixture(t)
	c := dataChairs(t, statusDataRow(t, root))[1]
	if !reflect.DeepEqual(c["needs_input"], map[string]any{"count": float64(1), "reason": "choose a value"}) || c["terminal"] != "prompt-two" {
		t.Fatal(c)
	}
}

func TestStatusJSONGivesTheTerminalOfASeatedOccupantAndNoneWhenNoOccupantIsSeated(t *testing.T) {
	root, s := statusDataFixture(t)
	s["playing"] = []any{map[string]any{"step": "draft", "round": 2, "started": statusNow}}
	saveStatusData(t, root, s)
	if c := dataChairs(t, statusDataRow(t, root))[0]; c["terminal"] != "" {
		t.Fatal(c)
	}
	s["playing"] = []any{}
	saveStatusData(t, root, s)
	f, err := os.OpenFile(filepath.Join(root, "data", "telemetry.jsonl"), os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(`{"step":"draft","round":2,"chair":{"address":"strings.pen@voices"},"terminal_id":"released"}` + "\n")
	f.Close()
	if c := dataChairs(t, statusDataRow(t, root))[0]; c["terminal"] != "" {
		t.Fatal(c)
	}
}

func TestStatusJSONSaysAnAbandonedChairHasUnknownState(t *testing.T) {
	root, s := statusDataFixture(t)
	s["process_start"] = "different"
	s["playing"] = []any{}
	saveStatusData(t, root, s)
	for _, c := range dataChairs(t, statusDataRow(t, root)) {
		if c["state"] != "unknown" {
			t.Fatal(c)
		}
	}
}

func TestStatusJSONListsARunRecordedBeforeTheseRecordsWithTodaysFieldsAndTheRestEmpty(t *testing.T) {
	root := t.TempDir()
	statusFixture(t, root, "old")
	r := statusDataRow(t, root)
	if r["schema"] != float64(1) || r["score"] != nil || len(r["current"].([]any)) != 0 || len(r["trail"].([]any)) != 0 {
		t.Fatal(r)
	}
}

func TestARunRecordedByTheLoopShowsItsSeatingScoreTrailAndUsageInStatus(t *testing.T) {
	standing := playStanding(t)
	dir := filepath.Join(loop.StateDir(), "runs", "recorded-data")
	observed := false
	stepper := observingStepper{observe: func(step loop.Step) {
		if step.Name != "review" {
			return
		}
		observed = true
		r := statusDataRow(t, dir)
		chairs := dataChairs(t, r)
		if r["cao"] != "http://127.0.0.1:12345" || r["score"].(map[string]any)["name"] != "default" || len(r["trail"].([]any)) < 2 || r["usage"] == nil {
			t.Fatal(r)
		}
		if chairs[0]["agent"] != "solo" || len(chairs) != 5 || chairs[1]["terminal"] != "terminal" || chairs[1]["memory"] != "resume" || chairs[2]["state"] != "playing" {
			t.Fatal(chairs)
		}
		if r["ensemble"].(map[string]any)["name"] != "" || r["ensemble"].(map[string]any)["sections"].([]any)[0].(map[string]any)["name"] != "chairs" {
			t.Fatal(r)
		}
	}}
	sum, err := runEnsemble(context.Background(), ensembleRun{RunID: "recorded-data", StateDir: dir, Agents: loop.Agents{{Name: "solo", Chair: plainChairs().Planner}}, Stepper: stepper, Progress: io.Discard, Config: loop.Config{RunID: "recorded-data", Worktree: standing.Worktree, Task: "probe", Chairs: plainChairs(), MaxRounds: 1, CAOURL: "http://127.0.0.1:12345", ReplyGrace: time.Millisecond}})
	if err != nil || sum.Outcome != loop.Approved || !observed {
		t.Fatalf("%+v %v observed %t", sum, err, observed)
	}
}

func TestStatusJSONScoreStepsListVerifyAndStrictWhereTheyRunAndStrictOnlyWithAStrictCommand(t *testing.T) {
	for _, strict := range []string{"", "true"} {
		t.Run(strict, func(t *testing.T) {
			standing := playStanding(t)
			dir := filepath.Join(loop.StateDir(), "runs", "outline")
			stepper := observingStepper{observe: func(step loop.Step) {
				if step.Name != "plan" {
					return
				}
				r := statusDataRow(t, dir)
				score, ok := r["score"].(map[string]any)
				if !ok {
					t.Fatal(r)
				}
				steps := score["steps"].([]any)
				if steps[0].(map[string]any)["kind"] != "verify" {
					t.Fatal(steps)
				}
				last := steps[len(steps)-1].(map[string]any)
				if (last["kind"] == "strict") != (strict != "") {
					t.Fatal(steps)
				}
			}}
			_, err := runEnsemble(context.Background(), ensembleRun{RunID: "outline", StateDir: dir, Stepper: stepper, Progress: io.Discard, Config: loop.Config{RunID: "outline", Worktree: standing.Worktree, Task: "probe", Chairs: plainChairs(), MaxRounds: 1, StrictCommand: strict}})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestStatusJSONDiagnosesMalformedTelemetryWithoutChangingPlainStatus(t *testing.T) {
	root, _ := statusDataFixture(t)
	writeFile(t, filepath.Join(root, "data", "telemetry.jsonl"), "{\"step\":\"start\",\"at\":\"2026-11-11T05:00:00Z\"}\ninvalid\n")
	_, diag := captureStatus(t, "-json", root)
	if !strings.Contains(diag, "telemetry") {
		t.Fatal(diag)
	}
	_, diag = captureStatus(t, root)
	if diag != "" {
		t.Fatal(diag)
	}
}

func TestStatusJSONLeavesCurrentEmptyWhenAnOlderRunHasNoRecordedScore(t *testing.T) {
	root := t.TempDir()
	s := statusFixture(t, root, "old")
	s.Playing = []loop.ActiveStep{{Step: "draft", Started: statusNow}}
	saveStatusFixture(t, root, "old", s)
	r := statusDataRow(t, root)
	if len(r["current"].([]any)) != 0 {
		t.Fatal(r)
	}
}

func TestStatusJSONKeepsChairsWaitingBetweenStepsWhileOnlyTheBackgroundStepPlays(t *testing.T) {
	root, s := statusDataFixture(t)
	path := filepath.Join(root, "data", "telemetry.jsonl")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	before, _, _ := strings.Cut(string(data), `{"at":"2026-11-11T05:04:00Z"`)
	writeFile(t, path, before)

	s["playing"] = []any{map[string]any{"step": "air", "started": statusNow}}
	saveStatusData(t, root, s)
	chairs := dataChairs(t, statusDataRow(t, root))
	if chairs[1]["state"] != "waiting" || chairs[2]["state"] != "waiting" {
		t.Fatal(chairs)
	}
}

func TestStatusJSONAttributesARecordedPartBeforeANameThatBelongsToTheSubject(t *testing.T) {
	root, _ := statusDataFixture(t)
	file, err := os.OpenFile(filepath.Join(root, "data", "telemetry.jsonl"), os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = file.WriteString(`{"step":"tune-tone","part":"scribe","chair_name":"one","chair":{"provider":"p"},"usage":{"known":true,"input_tokens":5,"output_tokens":2,"cache_read_tokens":0,"cache_write_tokens":0}}` + "\n")
	file.Close()
	if err != nil {
		t.Fatal(err)
	}
	chairs := dataChairs(t, statusDataRow(t, root))
	if chairs[0]["usage"].(map[string]any)["input_tokens"] != float64(17) || chairs[1]["usage"].(map[string]any)["input_tokens"] != float64(0) {
		t.Fatal(chairs)
	}
}

func TestStatusJSONScorePlacesVerifyBeforeTheScoreAndAfterWorktreeChanges(t *testing.T) {
	outline := loop.DefaultScore().Outline(true, true)
	var names []string
	for _, step := range outline.Steps {
		names = append(names, step.Name)
	}
	want := []string{"verify", "plan", "test-plan", "implement", "verify", "review", "gate", "test", "strict"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("steps %v, want %v", names, want)
	}
}

func TestARunRecordedByTheLoopKeepsAnEnsembleFilesSectionsChairsAndSource(t *testing.T) {
	for _, source := range []string{"user", "repository"} {
		t.Run(source, func(t *testing.T) {
			standing := playStanding(t)
			chairs := chairsFromEnsembleFile(t, standing.Worktree)
			if source == "repository" {
				path := filepath.Join(standing.Worktree, ".kapellmeister", "ensembles", "thorough.toml")
				writeFile(t, path, recordedEnsemble)
				if err := os.Remove(filepath.Join(loop.Home(), "ensembles", "thorough.toml")); err != nil {
					t.Fatal(err)
				}
			}
			dir := filepath.Join(loop.StateDir(), "runs", "sections")
			observed := false
			stepper := observingStepper{observe: func(step loop.Step) {
				if step.Name != "plan" {
					return
				}
				observed = true
				r := statusDataRow(t, dir)
				seating := r["ensemble"].(map[string]any)
				if seating["source"] != source || seating["name"] != "thorough" {
					t.Fatal(seating)
				}
				sections := seating["sections"].([]any)
				if len(sections) != 2 || sections[0].(map[string]any)["name"] != "leads" || sections[1].(map[string]any)["name"] != "review" {
					t.Fatal(sections)
				}
				found := false
				for _, c := range dataChairs(t, r) {
					if c["address"] == "review.security@thorough" && c["part"] == "reviewer" && c["name"] == "security" {
						found = true
					}
				}
				if !found {
					t.Fatal(seating)
				}
			}}
			_, err := runEnsemble(context.Background(), ensembleRun{RunID: "sections", StateDir: dir, Ensemble: "thorough", Stepper: stepper, Progress: io.Discard, Config: loop.Config{RunID: "sections", Worktree: standing.Worktree, Task: "probe", Chairs: chairs, MaxRounds: 1}})
			if err != nil || !observed {
				t.Fatalf("%v observed %t", err, observed)
			}
		})
	}
}

func TestStatusJSONMarksUsageAllSomeOrNoneWithoutInventingMissingCounters(t *testing.T) {
	measured := `{"step":"draft","chair":{"address":"strings.pen@voices"},"usage":{"known":true,"input_tokens":5,"output_tokens":2,"cache_read_tokens":0,"cache_write_tokens":0}}` + "\n"
	unknown := `{"step":"draft","chair":{"address":"strings.pen@voices"},"usage":{"known":false}}` + "\n"
	for _, tc := range []struct {
		name, records string
		input         float64
	}{
		{"all", measured + measured, 10}, {"some", measured + unknown, 5}, {"none", unknown + unknown, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, _ := statusDataFixture(t)
			writeFile(t, filepath.Join(root, "data", "telemetry.jsonl"), tc.records)
			r := statusDataRow(t, root)
			for _, u := range []any{r["usage"], dataChairs(t, r)[0]["usage"]} {
				usage := u.(map[string]any)
				if usage["measured"] != tc.name || usage["input_tokens"] != tc.input {
					t.Fatal(usage)
				}
			}
		})
	}
}

func TestStatusJSONReportsAStandingOccupantsTerminalBetweenSteps(t *testing.T) {
	root, s := statusDataFixture(t)
	s["command"] = "play"
	s["playing"] = []any{}
	s["seating"].(map[string]any)["sections"].([]any)[0].(map[string]any)["chairs"].([]any)[0].(map[string]any)["terminal"] = "standing"
	saveStatusData(t, root, s)
	writeFile(t, filepath.Join(root, "data", "telemetry.jsonl"), `{"step":"draft","chair":{"address":"strings.pen@voices"},"terminal_id":"standing"}`+"\n")
	if c := dataChairs(t, statusDataRow(t, root))[0]; c["terminal"] != "standing" {
		t.Fatal(c)
	}
}

func TestStatusJSONKeepsMeasuredTokensWhenARecordedUsageIsPartial(t *testing.T) {
	root, _ := statusDataFixture(t)
	writeFile(t, filepath.Join(root, "data", "telemetry.jsonl"), `{"step":"draft","chair":{"address":"strings.pen@voices"},"usage":{"known":true,"unknown_steps":1,"input_tokens":5,"output_tokens":2,"cache_read_tokens":0,"cache_write_tokens":0}}`+"\n")
	r := statusDataRow(t, root)
	want := map[string]any{"input_tokens": float64(5), "output_tokens": float64(2), "measured": "some"}
	if !reflect.DeepEqual(r["usage"], want) || !reflect.DeepEqual(dataChairs(t, r)[0]["usage"], want) {
		t.Fatal(r)
	}
}

func TestAStandingRunFromChairsRecordsOneSectionNamedChairs(t *testing.T) {
	standing := playStanding(t)
	chairs, err := standing.RebuildChairs()
	if err != nil {
		t.Fatal(err)
	}
	var members []ensemble.Member
	terminals := map[string]string{}
	for _, c := range standing.Chairs {
		members = append(members, ensemble.Member{Section: c.Section, Name: c.Name, Part: c.Part, Chair: c.Binding})
		terminals[c.Address] = c.TerminalID
	}
	dir := filepath.Join(loop.StateDir(), "runs", "standing-data")
	stepper := observingStepper{observe: func(step loop.Step) {
		if step.Name != "plan" {
			return
		}
		r := statusDataRow(t, dir)
		if r["ensemble"].(map[string]any)["name"] != standing.Ensemble {
			t.Fatal(r["ensemble"])
		}
		sections := r["ensemble"].(map[string]any)["sections"].([]any)
		if len(sections) != 1 || sections[0].(map[string]any)["name"] != "chairs" {
			t.Fatal(sections)
		}
		if c := dataChairs(t, r)[0]; c["terminal"] != standing.Chairs[0].TerminalID || c["address"] != standing.Chairs[0].Address {
			t.Fatal(c)
		}
	}}
	_, err = runEnsemble(context.Background(), ensembleRun{Command: "play", RunID: "standing-data", StateDir: dir, Ensemble: standing.Ensemble, Members: members, Terminals: terminals, Stepper: stepper, Progress: io.Discard, Config: loop.Config{RunID: "standing-data", Worktree: standing.Worktree, Task: "probe", Chairs: chairs, MaxRounds: 1}})
	if err != nil {
		t.Fatal(err)
	}
}

func TestStatusJSONUsesTelemetryOrderForTheLatestInputReason(t *testing.T) {
	root, _ := statusDataFixture(t)
	file, err := os.OpenFile(filepath.Join(root, "data", "telemetry.jsonl"), os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = file.WriteString(`{"at":"2026-11-11T05:07:00Z","step":"blocked","chair_name":"one","blocked_step":"audit","terminal_id":"last-prompt","detail":"last recorded prompt"}` + "\n")
	file.Close()
	if err != nil {
		t.Fatal(err)
	}
	c := dataChairs(t, statusDataRow(t, root))[1]
	if c["needs_input"].(map[string]any)["reason"] != "last recorded prompt" || c["terminal"] != "last-prompt" {
		t.Fatal(c)
	}
}
