package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jystringfellow/kapellmeister/internal/cao"
	"github.com/jystringfellow/kapellmeister/internal/ensemble"
	"github.com/jystringfellow/kapellmeister/internal/loop"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestAScreenReplyIsDisclosedOnceInTheSummary(t *testing.T) {
	var b bytes.Buffer
	_ = finishRunDetail(&b, ensembleSummary{Result: loop.Result{ReplySource: loop.ReplyScreen}}, false)
	if strings.Count(b.String(), "taken from the agent's screen") != 1 {
		t.Fatalf("summary %q", b.String())
	}
}

func TestMessagePreflightFailureLabelsCaptureAsUntrusted(t *testing.T) {
	err := preflightFailure([]loop.PreflightResult{{Chair: loop.Chair{Provider: "claude_code", Profile: "p", Reply: loop.ReplyMessage}, CaptureFailed: true, Error: "final-message reply not captured", Screen: "pane text"}}, time.Second)
	if err == nil || !strings.Contains(err.Error(), "final-message reply not captured") || !strings.Contains(err.Error(), "untrusted") || !strings.Contains(err.Error(), "pane text") {
		t.Fatalf("error %v", err)
	}
}
func TestMessageChairsAppearInStatusText(t *testing.T) {
	var b bytes.Buffer
	runs := []statusRun{{RunID: "run", Ensemble: statusSeating{Sections: []statusSection{{Chairs: []statusChair{{Address: "review.one@ensemble", Reply: loop.ReplyMessage}}}}}}}
	if err := printStatus(&b, runs, time.Now()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), "review.one@ensemble reply message") {
		t.Fatalf("status %s", b.String())
	}
}

func TestStatusJSONReportsTheScreenReplySourceAndMessageChair(t *testing.T) {
	root, state := statusDataFixture(t)
	state["reply_source"] = "screen"
	sections := state["seating"].(map[string]any)["sections"].([]any)
	chairs := sections[1].(map[string]any)["chairs"].([]any)
	chairs[0].(map[string]any)["reply"] = "message"
	saveStatusData(t, root, state)
	f, err := os.OpenFile(filepath.Join(root, "data", "telemetry.jsonl"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.WriteString("{\"at\":\"2026-11-11T05:10:00Z\",\"step\":\"audit\",\"round\":1,\"chair_name\":\"one\",\"chair\":{\"provider\":\"p\",\"reply\":\"message\"},\"reply_source\":\"screen\"}\n")
	if err != nil {
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	row := statusDataRow(t, root)
	if row["reply_source"] != "screen" {
		t.Fatalf("run %+v", row)
	}
	cs := dataChairs(t, row)
	if cs[1]["reply"] != "message" || cs[1]["reply_source"] != "screen" {
		t.Fatalf("chair %+v", cs[1])
	}
	trail := row["trail"].([]any)
	if trail[len(trail)-1].(map[string]any)["reply_source"] != "screen" {
		t.Fatalf("trail %+v", trail)
	}
}

func TestAFileReplyDoesNotAddAScreenDisclosure(t *testing.T) {
	var b bytes.Buffer
	_ = finishRunDetail(&b, ensembleSummary{}, false)
	if strings.Contains(b.String(), "taken from the agent's screen") {
		t.Fatal(b.String())
	}
}
func TestReseatingAMessageChairOnAnotherProviderIsRefused(t *testing.T) {
	member := ensemble.Member{Part: "reviewer", Chair: loop.Chair{Provider: "claude_code", Reply: loop.ReplyMessage}}
	_, err := prepareMember(context.Background(), nil, loop.Policy{}, loop.BuiltInAgents(), "codex", member)
	if err == nil || !strings.Contains(err.Error(), "proven live") {
		t.Fatalf("error %v", err)
	}
}

func TestACaptureFailureIsNamedOnceWhenAnotherChairHasADefinitionError(t *testing.T) {
	err := preflightFailure([]loop.PreflightResult{
		{Chair: loop.Chair{Provider: "claude_code", Reply: loop.ReplyMessage}, CaptureFailed: true, Error: "final-message reply not captured", Screen: "pane text"},
		{DefinitionError: "agent definition changed"},
	}, time.Second)
	if err == nil || strings.Count(err.Error(), "final-message reply not captured") != 1 || !strings.Contains(err.Error(), "agent definition changed") || !strings.Contains(err.Error(), "untrusted") {
		t.Fatalf("error %v", err)
	}
}

type screenReplyingStepper struct{ replyingStepper }

func (s screenReplyingStepper) Run(ctx context.Context, step loop.Step) (loop.StepResult, error) {
	if step.Chair.Reply != loop.ReplyMessage {
		return s.replyingStepper.Run(ctx, step)
	}
	nonce := regexp.MustCompile(`km-begin ([0-9a-f]+)`).FindStringSubmatch(step.Prompt)[1]
	return loop.StepResult{TerminalID: "screen", Status: "completed", LastMessage: "km-begin " + nonce + "\nAPPROVE\nkm-end " + nonce}, nil
}
func TestRunEnsemblePublishesTheScreenSourceAndMessageChairOnlyForMessageReplies(t *testing.T) {
	for _, message := range []bool{false, true} {
		t.Run(fmt.Sprint(message), func(t *testing.T) {
			standing := playStanding(t)
			chairs := plainChairs()
			if message {
				chairs.Reviewers[0].Provider, chairs.Reviewers[0].Reply = "claude_code", loop.ReplyMessage
			}
			member := ensemble.Member{Section: "review", Name: "one", Part: "reviewer", Chair: chairs.Reviewers[0]}
			dir := filepath.Join(loop.StateDir(), "runs", "message")
			var progress bytes.Buffer
			sum, err := runEnsemble(context.Background(), ensembleRun{RunID: "message", StateDir: dir, Ensemble: "band", Members: []ensemble.Member{member}, Stepper: screenReplyingStepper{}, Progress: &progress, Config: loop.Config{RunID: "message", Task: "probe", Worktree: standing.Worktree, Chairs: chairs, MaxRounds: 1, ReplyGrace: time.Millisecond}})
			if err != nil || sum.Outcome != loop.Approved {
				t.Fatalf("result %+v error %v", sum, err)
			}
			if strings.Contains(progress.String(), "review.one@band reply message") != message {
				t.Fatalf("banner %s", progress.String())
			}
			for _, name := range []string{"current.json", "result.json"} {
				record := fallbackRecord(t, dir, name)
				source, present := record["reply_source"]
				if present != message || message && source != "screen" {
					t.Fatalf("%s source %v present %v", name, source, present)
				}
			}
		})
	}
}
func TestFileOnlyStatusJSONOmitsEveryReplySource(t *testing.T) {
	root, _ := statusDataFixture(t)
	row := statusDataRow(t, root)
	data, err := json.Marshal(row)
	if err != nil || strings.Contains(string(data), `"reply_source"`) {
		t.Fatalf("status %s error %v", data, err)
	}
}
func TestTheAuditionChairTableShowsMessageReplies(t *testing.T) {
	var b bytes.Buffer
	printChairTable(&b, ensemble.Report{Chairs: []ensemble.ChairReport{{Address: "review.one@band", Chair: "claude_code:km-claude", Reply: loop.ReplyMessage, OK: true}}}, "CUE")
	if !strings.Contains(b.String(), "claude_code:km-claude reply message") {
		t.Fatal(b.String())
	}
}
func TestMessagePreflightLinesShowTheReplyModeForPassedAndBlockedChairs(t *testing.T) {
	for _, ready := range []bool{false, true} {
		t.Run(fmt.Sprint(ready), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.URL.Path == "/sessions":
					fmt.Fprint(w, `{"id":"t","session_name":"preflight"}`)
				case r.URL.Path == "/terminals/run-step":
					var req cao.StepRequest
					if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
						t.Error(err)
					}
					nonce := regexp.MustCompile(`km-begin ([0-9a-f]+)`).FindStringSubmatch(req.Prompt)[1]
					body := "not ready"
					if ready {
						body = "ready"
					}
					json.NewEncoder(w).Encode(cao.StepResponse{TerminalID: "t", Status: "completed", LastMessage: "km-begin " + nonce + "\n" + body + "\nkm-end " + nonce})
				default:
					fmt.Fprint(w, `{}`)
				}
			}))
			defer srv.Close()
			var results []loop.PreflightResult
			out, err := captureStdout(t, func() error {
				var err error
				results, err = checkPreflight(context.Background(), cao.NewClient(srv.URL), t.TempDir(), time.Second, 0, loop.Chair{Provider: "claude_code", Profile: "km-claude", Reply: loop.ReplyMessage})
				return err
			})
			label := "BLOCKED"
			if ready {
				label = "ok"
			}
			if err != nil || len(results) != 1 || results[0].OK != ready || !strings.Contains(out, label) || !strings.Contains(out, "claude_code:km-claude reply message") || !strings.Contains(out, "reply screen") {
				t.Fatalf("output %s results %+v error %v", out, results, err)
			}
		})
	}
}
func TestReseatingAMessageChairOnClaudePreservesItsReplyMode(t *testing.T) {
	fakeProfileInstall(t)
	srv := permissionCAO(t, map[string]cao.ProfilePermissions{"km-claude": {}}, nil)
	original := ensemble.Member{Section: "review", Name: "one", Part: "reviewer", Chair: loop.Chair{Provider: "claude_code", Reply: loop.ReplyMessage, Address: "review.one@band", Focus: "security"}}
	got, err := prepareMember(context.Background(), cao.NewClient(srv.URL), loop.DefaultPolicy(), loop.BuiltInAgents(), "claude", original)
	if err != nil || got.Chair.Reply != loop.ReplyMessage || got.Chair.Provider != "claude_code" || got.Chair.Address != original.Chair.Address || got.Chair.Focus != original.Chair.Focus {
		t.Fatalf("chair %+v error %v", got, err)
	}
}
func TestMessageSeatFallbackSkipsUnprovenProvidersAndShowsItsReplyMode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/sessions":
			if r.URL.Query().Get("provider") != "claude_code" {
				t.Error("seated unproven provider")
			}
			fmt.Fprint(w, `{"id":"t","session_name":"preflight"}`)
		case r.URL.Path == "/terminals/run-step":
			json.NewEncoder(w).Encode(cao.StepResponse{TerminalID: "t", Status: "completed", LastMessage: "capture failed"})
		case strings.HasSuffix(r.URL.Path, "/output"):
			fmt.Fprint(w, `{"output":"capture failed"}`)
		default:
			fmt.Fprint(w, `{}`)
		}
	}))
	defer srv.Close()
	spec, err := ensemble.Check("band", "band.toml", `[sections.review.chairs.one]
part="reviewer"
agents=["claude", "codex"]
reply="message"`, loop.BuiltInAgents())
	if err != nil {
		t.Fatal(err)
	}
	members, err := spec.Bind(loop.BuiltInAgents(), []string{"claude", "codex"})
	if err != nil {
		t.Fatal(err)
	}
	policy := loop.DefaultPolicy()
	settings := loop.Settings{Defined: loop.BuiltInAgents(), Agents: []string{"claude", "codex"}, SeatFallback: true, PreflightTimeout: time.Second}
	_, err = captureStdout(t, func() error {
		_, _, _, err := preflightSeats(context.Background(), cao.NewClient(srv.URL), t.TempDir(), settings, spec, members, loop.Chairs{Reviewers: []loop.Chair{members[0].Chair}}, &policy, io.Discard)
		return err
	})
	if err == nil || !strings.Contains(err.Error(), "codex:") || !strings.Contains(err.Error(), "other providers are refused until proven live") {
		t.Fatalf("error %v", err)
	}
	lines := ensemble.SeatLines([]ensemble.SeatChange{{Address: "review.one@band", BlockedAgent: "claude", SeatedAgent: "claude2", Reply: loop.ReplyMessage}}, nil)
	if len(lines) != 1 || !strings.Contains(lines[0], "reply message") {
		t.Fatalf("lines %v", lines)
	}
}

func TestDetachedFailureRecordsTheScreenReplySource(t *testing.T) {
	dir := t.TempDir()
	out, err := captureStdout(t, func() error {
		recordDetachedFailure(context.Background(), dir, detachedLaunchRecord{RunID: "test", Worktree: t.TempDir()}, runState{Started: time.Now(), ReplySource: loop.ReplyScreen}, errors.New("failed"))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "result.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"reply_source": "screen"`) || !strings.Contains(out, "taken from the agent's screen") {
		t.Fatalf("result %s output %s", data, out)
	}
}
func TestMessageChairReadinessReportsAMissingClaudeAgent(t *testing.T) {
	spec, err := ensemble.Check("band", "band.toml", `[sections.review.chairs.one]
part="reviewer"
agents=["codex", "claude"]
reply="message"`, loop.BuiltInAgents())
	if err != nil {
		t.Fatal(err)
	}
	got := repertoireReadiness(spec, loop.DefaultScore(), loop.Settings{Defined: loop.BuiltInAgents(), Agents: []string{"codex"}})
	if got != "review.one: none of claude is available" {
		t.Fatalf("readiness %q", got)
	}
}
func TestPlayOnlyPrefixesErrorsFromTheEnsembleFile(t *testing.T) {
	for _, kind := range []string{"old", "record", "file"} {
		t.Run(kind, func(t *testing.T) {
			standing := playStanding(t)
			var body strings.Builder
			for _, c := range standing.Chairs {
				fmt.Fprintf(&body, "[sections.%s.chairs.%s]\npart=%q\nagents=[%q]\n", c.Section, c.Name, c.Part, "codex")
				if kind == "file" && c.Part == "implementer" {
					body.WriteString("agents=[\"claude\"]\nreply=\"message\"\n")
				}
			}
			text := body.String()
			if kind == "file" {
				text = strings.Replace(text, "agents=[\"codex\"]\nagents=[\"claude\"]", "agents=[\"claude\"]", 1)
				standing.Chairs[1].Binding.Provider = "claude_code"
				standing.Chairs[1].Binding.Reply = loop.ReplyMessage
			}
			if kind == "old" {
				standing.Chairs[0].Binding.Provider = ""
				text = strings.Replace(text, "part=\"implementer\"", "part=\"planner\"", 1)
			}
			if kind == "record" {
				standing.Chairs[1].Part = "planner"
			}
			path := filepath.Join(loop.Home(), ensemble.UserDir, "score.toml")
			writeFile(t, path, text)
			savePlayStanding(t, standing)
			err := playCommand([]string{standing.Session})
			if err == nil {
				t.Fatal("expected error")
			}
			if strings.Contains(err.Error(), path) != (kind == "file") {
				t.Fatalf("error %v", err)
			}
			if kind == "old" && !strings.Contains(err.Error(), "older km") {
				t.Fatal(err)
			}
			if kind == "file" && !strings.Contains(err.Error(), "implementer.chair-1@score") {
				t.Fatal(err)
			}
		})
	}
}

func TestPreflightRefusesMissingProfilesBeforeSeating(t *testing.T) {
	var seats int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/agents/profiles" {
			fmt.Fprint(w, `[]`)
			return
		}
		if r.URL.Path == "/sessions" {
			seats++
		}
		fmt.Fprint(w, `{}`)
	}))
	defer srv.Close()
	var results []loop.PreflightResult
	out, err := captureStdout(t, func() error {
		var e error
		results, e = checkPreflight(context.Background(), cao.NewClient(srv.URL), t.TempDir(), time.Second, 0, loop.Chair{Provider: "claude_code", Profile: "km-claude"})
		return e
	})
	want := "profile km-claude is not installed in CAO; install it with km profiles install claude"
	if err != nil || seats != 0 || len(results) != 1 || !strings.Contains(out, want) || !strings.Contains(preflightFailure(results, time.Second).Error(), want) {
		t.Fatalf("seats %d output %s results %+v err %v", seats, out, results, err)
	}
}

func TestPreflightSeatsOtherChairsAndFallsBackWhenProfileListingFails(t *testing.T) {
	for _, failList := range []bool{false, true} {
		t.Run(fmt.Sprint(failList), func(t *testing.T) {
			var seated []string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/agents/profiles":
					if failList {
						http.Error(w, "unavailable", 500)
					} else {
						fmt.Fprint(w, `[{"name":"installed"}]`)
					}
				case "/sessions":
					seated = append(seated, r.URL.Query().Get("agent_profile"))
					fmt.Fprint(w, `{"id":"t","session_name":"preflight"}`)
				case "/terminals/run-step":
					var req cao.StepRequest
					_ = json.NewDecoder(r.Body).Decode(&req)
					m := regexp.MustCompile(`km-begin ([0-9a-f]+)`).FindStringSubmatch(req.Prompt)
					json.NewEncoder(w).Encode(cao.StepResponse{TerminalID: "t", Status: "completed", LastMessage: "km-begin " + m[1] + "\nready\nkm-end " + m[1]})
				default:
					fmt.Fprint(w, `{}`)
				}
			}))
			defer srv.Close()
			chairs := []loop.Chair{{Provider: "p", Profile: "missing", Reply: loop.ReplyMessage}, {Provider: "p", Profile: "installed", Reply: loop.ReplyMessage}}
			rs, err := checkPreflight(context.Background(), cao.NewClient(srv.URL), t.TempDir(), time.Second, 0, chairs...)
			if err != nil || len(rs) != 2 || !rs[1].OK {
				t.Fatalf("results %+v %v", rs, err)
			}
			if failList {
				if len(seated) != 2 || !rs[0].OK {
					t.Fatalf("seated %v results %+v", seated, rs)
				}
			} else if len(seated) != 1 || seated[0] != "installed" || !rs[0].ProfileMissing {
				t.Fatalf("seated %v results %+v", seated, rs)
			}
		})
	}
}

func TestATranscriptReplyOmitsTheScreenFormattingNotice(t *testing.T) {
	var out bytes.Buffer
	_ = finishRunDetail(&out, ensembleSummary{Result: loop.Result{ReplySource: loop.ReplyTranscript}}, false)
	if strings.Contains(out.String(), "taken from the agent's screen") || strings.Contains(out.String(), "re-wraps") {
		t.Fatalf("summary %q", out.String())
	}
}

func TestPreflightFailureIncludesTheTranscriptCaptureCause(t *testing.T) {
	const cause = "marker not on a line of its own"
	err := preflightFailure([]loop.PreflightResult{{Chair: loop.Chair{Provider: "claude_code", Reply: loop.ReplyMessage}, CaptureFailed: true, CaptureCause: cause, Error: "final-message reply not captured"}}, time.Second)
	if err == nil || !strings.Contains(err.Error(), "final-message reply not captured: "+cause) {
		t.Fatalf("error %v", err)
	}
}

func TestMessagePreflightPrintsTheTranscriptSource(t *testing.T) {
	home, dir := t.TempDir(), t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", home)
	dest := filepath.Join(home, "projects", strings.ReplaceAll(dir, string(filepath.Separator), "-"))
	var seats int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/agents/profiles":
			fmt.Fprint(w, `[{"name":"km-claude"}]`)
		case "/sessions":
			if r.Method == http.MethodPost {
				seats++
			}
			fmt.Fprint(w, `{"id":"t","session_name":"preflight"}`)
		case "/terminals/run-step":
			var req cao.StepRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Error(err)
				return
			}
			match := regexp.MustCompile(`km-begin ([0-9a-f]+)`).FindStringSubmatch(req.Prompt)
			if len(match) != 2 {
				t.Error("missing nonce")
				return
			}
			data, err := json.Marshal(map[string]any{"type": "assistant", "message": map[string]any{"id": "final", "content": []map[string]string{{"type": "text", "text": "km-begin " + match[1] + "\nready\nkm-end " + match[1]}}}})
			if err != nil {
				t.Error(err)
				return
			}
			if err := os.MkdirAll(dest, 0700); err != nil {
				t.Error(err)
				return
			}
			if err := os.WriteFile(filepath.Join(dest, "session.jsonl"), append(data, '\n'), 0600); err != nil {
				t.Error(err)
				return
			}
			json.NewEncoder(w).Encode(cao.StepResponse{TerminalID: "t", Status: "completed", LastMessage: "screen capture lost"})
		default:
			fmt.Fprint(w, `{}`)
		}
	}))
	defer srv.Close()
	var results []loop.PreflightResult
	out, err := captureStdout(t, func() error {
		var err error
		results, err = checkPreflight(context.Background(), cao.NewClient(srv.URL), dir, time.Second, 0, loop.Chair{Provider: "claude_code", Profile: "km-claude", Reply: loop.ReplyMessage})
		return err
	})
	if err != nil || seats != 1 || len(results) != 1 || !results[0].OK || results[0].ReplySource != loop.ReplyTranscript || !strings.Contains(out, "ok      claude_code:km-claude reply message; reply transcript") || strings.Contains(out, "reply screen") {
		t.Fatalf("output %s results %+v seats %d error %v", out, results, seats, err)
	}
}

func TestAMissingProfileAllowsOtherChairsAndFallbackOnTheSameProvider(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	var seated []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/agents/profiles":
			fmt.Fprint(w, `[{"name":"installed"},{"name":"other"}]`)
		case "/sessions":
			if r.Method == http.MethodPost {
				seated = append(seated, r.URL.Query().Get("agent_profile"))
			}
			fmt.Fprint(w, `{"id":"t","session_name":"preflight"}`)
		case "/terminals/run-step":
			var req cao.StepRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Error(err)
				return
			}
			match := regexp.MustCompile(`km-begin ([0-9a-f]+)`).FindStringSubmatch(req.Prompt)
			if len(match) != 2 {
				t.Error("missing nonce")
				return
			}
			json.NewEncoder(w).Encode(cao.StepResponse{TerminalID: "t", Status: "completed", LastMessage: "km-begin " + match[1] + "\nready\nkm-end " + match[1]})
		default:
			fmt.Fprint(w, `{}`)
		}
	}))
	defer srv.Close()
	defined := loop.Agents{
		{Name: "first", Chair: loop.Chair{Provider: "claude_code", Profile: "missing"}},
		{Name: "later", Chair: loop.Chair{Provider: "claude_code", Profile: "installed"}},
		{Name: "other", Chair: loop.Chair{Provider: "claude_code", Profile: "other"}},
	}
	spec, err := ensemble.Check("band", "band.toml", `[sections.review.chairs.one]
part="reviewer"
agents=["first", "later"]
reply="message"
[sections.review.chairs.two]
part="reviewer"
agents=["other"]
reply="message"`, defined)
	if err != nil {
		t.Fatal(err)
	}
	members, err := spec.Bind(defined, defined.Names())
	if err != nil {
		t.Fatal(err)
	}
	policy := loop.Policy{}
	settings := loop.Settings{Defined: defined, Agents: defined.Names(), SeatFallback: true, PreflightTimeout: time.Second}
	var after []ensemble.Member
	var changes []ensemble.SeatChange
	var lost []ensemble.ProvidersLost
	out, err := captureStdout(t, func() error {
		var err error
		after, changes, lost, err = preflightSeats(context.Background(), cao.NewClient(srv.URL), t.TempDir(), settings, spec, members, loop.Chairs{Reviewers: []loop.Chair{members[0].Chair, members[1].Chair}}, &policy, io.Discard)
		return err
	})
	if err != nil || len(after) != 2 || after[0].Chair.Profile != "installed" || after[1].Chair.Profile != "other" || after[0].Chair.Reply != loop.ReplyMessage {
		t.Fatalf("members %+v error %v output %s", after, err, out)
	}
	if len(seated) != 2 || seated[0] != "other" || seated[1] != "installed" {
		t.Fatalf("seated %v", seated)
	}
	if len(changes) != 1 || changes[0].BlockedAgent != "first" || changes[0].SeatedAgent != "later" || changes[0].BlockedProvider != "claude_code" || changes[0].SeatedProvider != "claude_code" || !strings.Contains(changes[0].Reason, "profile missing is not installed") || len(lost) != 0 {
		t.Fatalf("changes %+v lost %+v", changes, lost)
	}
}

func TestPreflightReturnsNilResultsWhenPreparationFails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `[{"name":"km-claude"}]`)
	}))
	defer server.Close()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".kapellmeister"), []byte("file"), 0600); err != nil {
		t.Fatal(err)
	}
	results, err := checkPreflight(context.Background(), cao.NewClient(server.URL), dir, time.Second, 0, loop.Chair{Provider: "claude_code", Profile: "km-claude"})
	if err == nil || results != nil {
		t.Fatalf("results %+v error %v", results, err)
	}
}
