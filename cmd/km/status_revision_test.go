package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/jystringfellow/kapellmeister/internal/ensemble"
	"github.com/jystringfellow/kapellmeister/internal/loop"
)

func TestStatusJSONOmitsUserinfoFromRecordedServerAddresses(t *testing.T) {
	for _, tc := range []struct{ address, want string }{
		{"http://user:s3cretpw@127.0.0.1:12345", "http://127.0.0.1:12345"},
		{"https://user%40name:p%40ss%3Aword@[::1]:12345/base", "https://[::1]:12345/base"},
		{"http://user@127.0.0.1:12345", "http://127.0.0.1:12345"},
		{"http://127.0.0.1:12345", "http://127.0.0.1:12345"},
		{"http://user:pw@127.0.0.1:12345/%zz", ""},
	} {
		t.Run(tc.address, func(t *testing.T) {
			root, state := statusDataFixture(t)
			state["cao"] = tc.address
			saveStatusData(t, root, state)
			got, _ := statusDataRow(t, root)["cao"].(string)
			if got != tc.want {
				t.Fatalf("server address %q, want %q", got, tc.want)
			}
		})
	}
}

func TestARunRecordsItsServerAddressWithoutUserinfo(t *testing.T) {
	for _, effective := range []string{"", "http://user:pw@127.0.0.1:23456"} {
		t.Run(effective, func(t *testing.T) {
			standing := playStanding(t)
			dir := filepath.Join(loop.StateDir(), "runs", "server-address")
			want := "http://127.0.0.1:12345"
			if effective != "" {
				want = "http://127.0.0.1:23456"
			}
			observed := false
			stepper := observingStepper{observe: func(step loop.Step) {
				if step.Name != "plan" {
					return
				}
				observed = true
				data, err := os.ReadFile(filepath.Join(dir, "current.json"))
				if err != nil {
					t.Fatal(err)
				}
				var state runState
				if err := json.Unmarshal(data, &state); err != nil {
					t.Fatal(err)
				}
				if state.CAO != want {
					t.Errorf("recorded server address %q, want %q", state.CAO, want)
				}
				if got := statusDataRow(t, dir)["cao"]; got != want {
					t.Errorf("status server address %q, want %q", got, want)
				}
			}}
			_, err := runEnsemble(context.Background(), ensembleRun{RunID: "server-address", StateDir: dir, CAOURL: effective, Stepper: stepper, Progress: io.Discard, Config: loop.Config{RunID: "server-address", Worktree: standing.Worktree, Task: "probe", Chairs: plainChairs(), MaxRounds: 1, CAOURL: "http://user:pw@127.0.0.1:12345", ReplyGrace: time.Millisecond}})
			if err != nil || !observed {
				t.Fatalf("%v observed %t", err, observed)
			}
		})
	}
}

func TestStatusJSONReportsNoCurrentStepsAndUnknownChairsWhenAPlayingRunIsAbandoned(t *testing.T) {
	root, state := statusDataFixture(t)
	alive := statusDataRow(t, root)
	state["process_start"] = "different"
	saveStatusData(t, root, state)
	abandoned := statusDataRow(t, root)
	if abandoned["liveness"] != "abandoned" {
		t.Fatal(abandoned["liveness"])
	}
	if current := abandoned["current"].([]any); len(current) != 0 {
		t.Errorf("abandoned run has current steps: %v", current)
	}
	for _, chair := range dataChairs(t, abandoned) {
		if chair["state"] != "unknown" {
			t.Errorf("abandoned chair state: %v", chair)
		}
	}
	for _, field := range []string{"playing", "trail", "usage"} {
		if !reflect.DeepEqual(abandoned[field], alive[field]) {
			t.Errorf("%s changed: %v, want %v", field, abandoned[field], alive[field])
		}
	}
}

func writeStatusRecords(t *testing.T, root string, records ...map[string]any) {
	t.Helper()
	var body []byte
	for _, record := range records {
		data, err := json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		body = append(body, data...)
		body = append(body, '\n')
	}
	writeFile(t, filepath.Join(root, "data", "telemetry.jsonl"), string(body))
}

func statusMeasuredStep(step, address, at, terminal string) map[string]any {
	return map[string]any{"step": step, "chair": map[string]any{"address": address}, "at": at, "round": 1, "terminal_id": terminal, "kept": true, "usage": map[string]any{"known": true, "input_tokens": 5, "output_tokens": 2, "cache_read_tokens": 0, "cache_write_tokens": 0}}
}

func TestStatusJSONConductorRecordsPreserveMeasuredUsageAndChairSteps(t *testing.T) {
	root, state := statusDataFixture(t)
	state["playing"] = []any{}
	saveStatusData(t, root, state)
	writeStatusRecords(t, root,
		statusMeasuredStep("draft", "strings.pen@voices", "2026-11-11T05:02:00Z", "pen-terminal"),
		statusMeasuredStep("audit", "winds.two@voices", "2026-11-11T05:03:00Z", "wind-terminal"),
		map[string]any{"step": "escalate", "chair": map[string]any{"address": "strings.pen@voices"}, "tier": "large", "at": "2026-11-11T05:04:00Z"},
		map[string]any{"step": "restore", "chair": map[string]any{"address": "winds.two@voices", "model": "ignored"}, "tier": "large", "at": "2026-11-11T05:05:00Z"},
	)
	r := statusDataRow(t, root)
	chairs := dataChairs(t, r)
	if !reflect.DeepEqual(r["usage"], map[string]any{"input_tokens": float64(10), "output_tokens": float64(4), "measured": "all"}) {
		t.Errorf("run usage: %v", r["usage"])
	}
	for _, c := range []map[string]any{chairs[0], chairs[2]} {
		if !reflect.DeepEqual(c["usage"], map[string]any{"input_tokens": float64(5), "output_tokens": float64(2), "measured": "all"}) {
			t.Errorf("chair usage: %v", c)
		}
	}
	for k, want := range map[string]any{"step": "draft", "since": "2026-11-11T05:02:00Z", "terminal": "", "tier": "large"} {
		if chairs[0][k] != want {
			t.Errorf("escalated %s=%v, want %v", k, chairs[0][k], want)
		}
	}
	for k, want := range map[string]any{"step": "audit", "since": "2026-11-11T05:03:00Z", "terminal": "wind-terminal", "tier": "", "model": ""} {
		if chairs[2][k] != want {
			t.Errorf("restored %s=%v, want %v", k, chairs[2][k], want)
		}
	}
}

func TestStatusJSONConductorRecordsAloneDoNotCreateUsageOrPlayedSteps(t *testing.T) {
	root, state := statusDataFixture(t)
	state["playing"] = []any{}
	saveStatusData(t, root, state)
	writeStatusRecords(t, root,
		map[string]any{"step": "restore", "chair": map[string]any{"address": "strings.pen@voices"}},
		map[string]any{"step": "escalate", "chair": map[string]any{"address": "winds.two@voices"}, "tier": "large"},
	)
	r := statusDataRow(t, root)
	if r["usage"] != nil {
		t.Fatal(r["usage"])
	}
	for _, c := range dataChairs(t, r) {
		if c["step"] != "" || c["usage"].(map[string]any)["measured"] != "none" || c["state"] != "waiting" {
			t.Fatal(c)
		}
	}
}

func TestStatusJSONTracksResolvedModelsAndTiersFromAttributedSteps(t *testing.T) {
	for _, tc := range []struct {
		name    string
		chair   map[string]any
		model   string
		honored bool
		want    string
	}{
		{"resolved", map[string]any{"address": "strings.pen@voices", "model": "resolved-model"}, "", true, "resolved-model"},
		{"reported", map[string]any{"address": "strings.pen@voices", "model": "requested-model"}, "actual-model", false, "actual-model"},
		{"honored", map[string]any{"address": "strings.pen@voices", "model": "requested-model"}, "reported-model", true, "requested-model"},
		{"fallback", map[string]any{"address": "strings.pen@voices"}, "reported-model", true, "reported-model"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, state := statusDataFixture(t)
			state["playing"] = []any{}
			saveStatusData(t, root, state)
			record := statusMeasuredStep("draft", "strings.pen@voices", "2026-11-11T05:02:00Z", "pen")
			record["chair"], record["model"], record["model_honored"], record["tier"] = tc.chair, tc.model, tc.honored, "large"
			writeStatusRecords(t, root, record)
			c := dataChairs(t, statusDataRow(t, root))[0]
			if c["model"] != tc.want || c["tier"] != "large" {
				t.Fatal(c)
			}
		})
	}
}

func TestARunRecordsPolicyModelsAndTiersForAgentNamedChairsBeforeTheyPlay(t *testing.T) {
	standing := playStanding(t)
	chair := loop.Chair{Provider: "p", Profile: "base"}
	policy := loop.Policy{Tiers: map[string]map[string]loop.TierSpec{"p": {"small": {Model: "small-model"}, "medium": {Model: "medium-model"}, "large": {Model: "large-model"}}}, Steps: map[string]loop.StepPolicy{}}
	for _, s := range loop.DefaultScore().Steps {
		if s.Background {
			continue
		}
		policy.Steps[s.Name] = loop.StepPolicy{Tier: "medium", Min: "small", Max: "large"}
	}
	policy.Steps["classify"] = loop.StepPolicy{Tier: "small", Min: "small", Max: "small"}
	chairs := loop.Chairs{Planner: chair, Implementer: chair, Reviewers: []loop.Chair{chair}, Gate: chair, Classifier: chair, Tester: &chair}
	dir := filepath.Join(loop.StateDir(), "runs", "policy-data")
	observed := false
	stepper := observingStepper{observe: func(step loop.Step) {
		if step.Name != "plan" {
			return
		}
		observed = true
		recorded := dataChairs(t, statusDataRow(t, dir))
		for _, c := range recorded {
			model, tier := "medium-model", "medium"
			if c["part"] == loop.DefaultScore().Classifier {
				model, tier = "small-model", "small"
			}
			if c["agent"] != "solo" || c["model"] != model || c["tier"] != tier {
				t.Errorf("initial chair: %v", c)
			}
		}
	}}
	_, err := runEnsemble(context.Background(), ensembleRun{RunID: "policy-data", StateDir: dir, Agents: loop.Agents{{Name: "solo", Chair: chair}}, Stepper: stepper, Progress: io.Discard, Config: loop.Config{RunID: "policy-data", Worktree: standing.Worktree, Task: "probe", Chairs: chairs, Policy: &policy, MaxRounds: 1, ReplyGrace: time.Millisecond}})
	if err != nil || !observed {
		t.Fatalf("%v observed %t", err, observed)
	}
}

func TestARunRecordsTheAgentOfAStandingChairWithAnEffortProfile(t *testing.T) {
	standing := playStanding(t)
	score := loop.DefaultScore()
	base := loop.Chair{Provider: "codex", Profile: "base"}
	defined := loop.Agents{{Name: "solo", Chair: base}, {Name: "exact", Chair: loop.Chair{Provider: "codex", Profile: "base-low"}}}
	for _, tc := range []struct{ profile, want string }{{"base-high", "solo"}, {"base-low", "exact"}, {"base-other", "base-other"}} {
		t.Run(tc.profile, func(t *testing.T) {
			chair := base
			chair.Profile = tc.profile
			chair.Model = "resolved-model"
			p := ensembleRun{Agents: defined, Members: []ensemble.Member{{Name: "pen", Part: score.Parts[0].Name, Chair: chair}}, Config: loop.Config{Worktree: standing.Worktree}}
			s := recordSeating(p, score)
			if got := s.Sections[0].Chairs[0].Agent; got != tc.want {
				t.Fatalf("agent %s, want %s", got, tc.want)
			}
		})
	}
}

func TestStatusJSONFindsBlockedChairsByAddressAndPartOrdinal(t *testing.T) {
	for _, name := range []string{"winds.two@voices", "auditor-2"} {
		t.Run(name, func(t *testing.T) {
			root, state := statusDataFixture(t)
			state["playing"] = []any{}
			saveStatusData(t, root, state)
			writeStatusRecords(t, root, map[string]any{"step": "blocked", "blocked_step": "audit", "part": "auditor", "chair_name": name, "terminal_id": "blocked-terminal", "detail": "allow a tool"})
			chairs := dataChairs(t, statusDataRow(t, root))
			c := chairs[2]
			if c["terminal"] != "blocked-terminal" || !reflect.DeepEqual(c["needs_input"], map[string]any{"count": float64(1), "reason": "allow a tool"}) {
				t.Fatal(c)
			}
			if chairs[1]["needs_input"].(map[string]any)["count"] != float64(0) {
				t.Fatal(chairs[1])
			}
		})
	}
}

func TestStatusJSONKeepsServiceChairsWaitingWhileVerdictStepsAreReachable(t *testing.T) {
	for _, tc := range []struct{ active, trailStep, exit, want string }{
		{"audit", "draft", "done", "waiting"},
		{"audit-nudge", "draft", "done", "waiting"},
		{"classify-audit", "draft", "done", "waiting"},
		{"classify-audit-nudge", "draft", "done", "waiting"},
		{"verify-0", "", "", "waiting"},
		{"strict", "sign-off", "approve", "done"},
	} {
		t.Run(tc.active, func(t *testing.T) {
			root, state := statusDataFixture(t)
			seating := state["seating"].(map[string]any)
			sections := seating["sections"].([]any)
			section := sections[0].(map[string]any)
			section["chairs"] = append(section["chairs"].([]any), map[string]any{"address": "strings.pitch@voices", "name": "pitch", "part": "tuner", "memory": "fresh"})
			state["playing"] = []any{map[string]any{"step": tc.active, "part": "auditor", "chair": "winds.one@voices", "started": statusNow}}
			saveStatusData(t, root, state)
			records := []map[string]any{{"step": "classify-audit", "part": "tuner", "chair": map[string]any{"address": "strings.pitch@voices"}}}
			if tc.trailStep != "" {
				partAddress := "strings.pen@voices"
				if tc.trailStep == "sign-off" {
					partAddress = "winds.seal@voices"
				}
				records = append(records, statusMeasuredStep(tc.trailStep, partAddress, "2026-11-11T05:02:00Z", ""))
				if tc.exit != "done" {
					records = append(records, map[string]any{"step": "verdict-" + tc.trailStep, "round": 1, "verdict": tc.exit})
				}
			}
			writeStatusRecords(t, root, records...)
			c := dataChairs(t, statusDataRow(t, root))[1]
			if c["state"] != tc.want {
				t.Fatalf("service state %v, want %s", c, tc.want)
			}
		})
	}
}

func TestStatusJSONKeepsOnlyTheReachableForegroundPartsWaitingDuringANudge(t *testing.T) {
	for _, active := range []string{"sign-off-nudge", "classify-sign-off", "classify-sign-off-nudge"} {
		t.Run(active, func(t *testing.T) {
			root, state := statusDataFixture(t)
			state["playing"] = []any{map[string]any{"step": active, "chair": "winds.seal@voices", "part": "signer", "started": statusNow}}
			saveStatusData(t, root, state)
			record := statusMeasuredStep("audit", "winds.one@voices", "2026-11-11T05:02:00Z", "")
			record["chair_name"] = "one"
			writeStatusRecords(t, root, record, map[string]any{"step": "verdict-audit", "round": 1, "chair_name": "one", "verdict": "CHANGES"})
			chairs := dataChairs(t, statusDataRow(t, root))
			if chairs[1]["state"] != "done" {
				t.Fatal(chairs[1])
			}
		})
	}
}
