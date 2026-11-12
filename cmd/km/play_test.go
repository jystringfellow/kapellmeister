package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jystringfellow/kapellmeister/internal/cao"
	"github.com/jystringfellow/kapellmeister/internal/ensemble"
	"github.com/jystringfellow/kapellmeister/internal/loop"
)

func playStanding(t *testing.T) ensemble.Standing {
	t.Helper()
	t.Setenv(loop.HomeEnv, t.TempDir())
	t.Setenv(loop.StateEnv, t.TempDir())
	t.Setenv(loop.CAOURLEnv, "")
	wt := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"config", "user.name", "Test"}, {"config", "user.email", "test@example.invalid"}} {
		if out, err := exec.Command("git", append([]string{"-C", wt}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	writeFile(t, filepath.Join(wt, ".gitignore"), ".kapellmeister/\n")
	for _, args := range [][]string{{"add", ".gitignore"}, {"-c", "commit.gpgsign=false", "commit", "-qm", "fixture"}} {
		if out, err := exec.Command("git", append([]string{"-C", wt}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	writeFile(t, filepath.Join(wt, loop.ConfigFile), "[test]\nverify = 'true'\ncheck = 'go test'\n[run]\nlessons = true\nrounds = 2\nreply_grace = '1ms'\n")
	s := ensemble.Standing{Ensemble: "score", Session: "cao-standing", Worktree: wt, Task: "recorded task", GateWhen: loop.GateWhenAll}
	for i, part := range []string{"planner", "implementer", "reviewer", "reviewer", "gate", "classifier"} {
		name := fmt.Sprintf("chair-%d", i)
		address := part + "." + name + "@score"
		s.Chairs = append(s.Chairs, ensemble.StandingChair{Address: address, Section: part, Name: name, Part: part, TerminalID: "terminal-" + name, Briefed: true, Binding: loop.Chair{Address: address, Provider: "codex", Profile: "km-codex", Model: "standing-model", Tier: "small"}})
	}
	savePlayStanding(t, s)
	return s
}

func savePlayStanding(t *testing.T, s ensemble.Standing) {
	t.Helper()
	if err := s.Save(filepath.Join(loop.StateDir(), "ensembles")); err != nil {
		t.Fatal(err)
	}
}

type playCAO struct {
	afterReply  func(worktree, name string)
	mu          sync.Mutex
	requests    []cao.StepRequest
	replies     []string
	checked     []string
	unexpected  []string
	released    []string
	missing     map[string]int
	failPart    string
	noReplyPart string
	changes     bool
}

var playReplyPath = regexp.MustCompile(`\.kapellmeister/runs/[^\s,:;]+\.(?:md|verdict|json)`)

func (f *playCAO) serve(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch {
		case r.Method == http.MethodPatch && strings.HasSuffix(r.URL.Path, "/description"):
			w.Write([]byte(`{}`))
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/terminals/"):
			id := strings.TrimPrefix(r.URL.Path, "/terminals/")
			f.checked = append(f.checked, id)
			if status := f.missing[id]; status != 0 {
				w.WriteHeader(status)
				return
			}
			_ = json.NewEncoder(w).Encode(cao.Terminal{ID: id})
		case r.Method == http.MethodPost && r.URL.Path == "/terminals/run-step":
			var req cao.StepRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Error(err)
				http.Error(w, "bad body", 400)
				return
			}
			paths := playReplyPath.FindAllString(req.Prompt, -1)
			if len(paths) == 0 {
				t.Errorf("no reply path in %s", req.Prompt)
				http.Error(w, "no path", 500)
				return
			}
			path := paths[len(paths)-1]
			f.requests = append(f.requests, req)
			f.replies = append(f.replies, filepath.Base(path))
			part := playReplyPart(filepath.Base(path))
			if f.failPart == part || (filepath.Base(path) == "lessons-reply.json" && f.failPart == "lessons") {
				http.Error(w, `{"detail":{"terminal_id":"unexpected-terminal"}}`, 500)
				return
			}
			if f.noReplyPart != part && !(filepath.Base(path) == "lessons-reply.json" && f.noReplyPart == "lessons") {
				text := "reply\nSize: large — exercise fixed binding\n"
				if filepath.Base(path) == "lessons-reply.json" {
					text = "[]"
				}
				if strings.HasSuffix(path, ".verdict") {
					text = "APPROVE"
					if f.changes && strings.HasPrefix(filepath.Base(path), "gate-") {
						text = "CHANGES"
					}
				}
				if err := os.WriteFile(filepath.Join(req.WorkingDirectory, path), []byte(text), 0600); err != nil {
					t.Error(err)
				}
			}
			if f.afterReply != nil {
				f.afterReply(req.WorkingDirectory, filepath.Base(path))
			}
			id := req.ReuseTerminalID
			if id == "" {
				id = fmt.Sprintf("step-%d", len(f.requests))
			}
			_ = json.NewEncoder(w).Encode(cao.StepResponse{TerminalID: id, Status: "completed"})
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/terminals/step-"):
			f.released = append(f.released, strings.TrimPrefix(r.URL.Path, "/terminals/"))
			w.Write([]byte(`{}`))
		default:
			f.unexpected = append(f.unexpected, r.Method+" "+r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func playReplyPart(path string) string {
	switch {
	case strings.HasSuffix(path, ".verdict"):
		return "classifier"
	case path == "lessons-reply.json":
		return "gate"
	case path == "plan.md":
		return "planner"
	case strings.HasPrefix(path, "implement-"):
		return "implementer"
	case strings.HasPrefix(path, "review-"):
		return "reviewer"
	case strings.HasPrefix(path, "gate-"):
		return "gate"
	}
	return "unknown"
}

func capturePlay(t *testing.T, args []string) (string, error) {
	t.Helper()
	return captureStdout(t, func() error { return playCommand(args) })
}

func captureStdout(t *testing.T, run func() error) (string, error) {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	original := os.Stdout
	os.Stdout = writer
	defer func() { os.Stdout = original }()
	done := make(chan string, 1)
	go func() { var b bytes.Buffer; _, _ = io.Copy(&b, reader); done <- b.String() }()
	playErr := run()
	_ = writer.Close()
	out := <-done
	_ = reader.Close()
	return out, playErr
}

func requirePlayStillStanding(t *testing.T, s ensemble.Standing, fake *playCAO, out string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(loop.StateDir(), "ensembles", s.Session+".json")); err != nil {
		t.Errorf("standing record lost: %v", err)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.unexpected) != 0 {
		t.Errorf("unexpected CAO calls (including deletes): %v", fake.unexpected)
	}
	if !strings.HasSuffix(out, "km ensemble down "+s.Session+"\n") {
		t.Errorf("missing final teardown hint: %s", out)
	}
}

func TestPlayRejectsAnUnknownSessionAndListsTheRecordedOnes(t *testing.T) {
	standingState(t)
	err := playCommand([]string{"missing"})
	if err == nil || err.Error() != `unknown session "missing" (recorded: alpha, beta)` {
		t.Fatalf("unknown session: %v", err)
	}
	t.Setenv(loop.StateEnv, t.TempDir())
	err = playCommand([]string{"missing"})
	if err == nil || err.Error() != `unknown session "missing" (none recorded)` {
		t.Fatalf("empty records: %v", err)
	}
}

func TestPlayNeedsExactlyOneSessionAndAtLeastOneRound(t *testing.T) {
	for _, args := range [][]string{nil, {"one", "two"}} {
		if err := playCommand(args); err == nil || !strings.Contains(err.Error(), "usage: km play") {
			t.Errorf("args %v: %v", args, err)
		}
	}
	s := playStanding(t)
	if err := playCommand([]string{s.Session, "-max-rounds", "0"}); err == nil || !strings.Contains(err.Error(), "-max-rounds must be at least 1") {
		t.Fatalf("rounds: %v", err)
	}
}

func TestPlayAcceptsTheSessionBeforeOrAfterItsFlags(t *testing.T) {
	for _, first := range []bool{false, true} {
		t.Run(fmt.Sprint(first), func(t *testing.T) {
			s := playStanding(t)
			fake := &playCAO{}
			srv := fake.serve(t)
			args := []string{"-cao", srv.URL, "-max-rounds", "1"}
			if first {
				args = append([]string{s.Session}, args...)
			} else {
				args = append(args, s.Session)
			}
			out, err := capturePlay(t, args)
			if err != nil || !strings.Contains(out, "approved after 1 round(s)") {
				t.Fatalf("play: %v\n%s", err, out)
			}
		})
	}
}

func TestPlayFindingMissingOccupantsSuggestsKmReseatForEachChair(t *testing.T) {
	s := playStanding(t)
	fake := &playCAO{missing: map[string]int{s.Chairs[0].TerminalID: 404, s.Chairs[3].TerminalID: 404}}
	srv := fake.serve(t)
	out, err := capturePlay(t, []string{s.Session, "-cao", srv.URL})
	for _, want := range []string{"km reseat " + s.Session + " " + s.Chairs[0].Address, "km reseat " + s.Session + " " + s.Chairs[3].Address, "km ensemble down " + s.Session, "km rehearse"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("missing occupants error %v lacks %q", err, want)
		}
	}
	if len(fake.requests) != 0 || len(fake.checked) != len(s.Chairs) {
		t.Errorf("requests = %+v, checked = %v", fake.requests, fake.checked)
	}
	requirePlayStillStanding(t, s, fake, out)
}

func TestPlayReportsCAOErrorsWithoutCallingTheOccupantsMissing(t *testing.T) {
	for _, connection := range []bool{false, true} {
		t.Run(fmt.Sprint(connection), func(t *testing.T) {
			s := playStanding(t)
			fake := &playCAO{missing: map[string]int{s.Chairs[0].TerminalID: 503}}
			srv := fake.serve(t)
			url := srv.URL
			if connection {
				url = unreachableCAO
			}
			_, err := capturePlay(t, []string{s.Session, "-cao", url})
			if err == nil || strings.Contains(err.Error(), "no occupant") {
				t.Fatalf("CAO error = %v", err)
			}
			if len(fake.requests) != 0 {
				t.Fatal("run-step after CAO error")
			}
		})
	}
}

func TestPlayRunsEveryStepInItsChairsStandingOccupantAndDeletesNothing(t *testing.T) {
	s := playStanding(t)
	fake := &playCAO{}
	srv := fake.serve(t)
	out, err := capturePlay(t, []string{s.Session, "-cao", srv.URL})
	if err != nil || !strings.Contains(out, "approved after 1 round(s)") {
		t.Fatalf("play: %v\n%s", err, out)
	}
	if len(fake.requests) != 9 {
		t.Fatalf("run-step calls = %d, replies = %v", len(fake.requests), fake.replies)
	}
	requirePlayBindings(t, s, fake)
	requirePlayStillStanding(t, s, fake, out)
	entries, err := os.ReadDir(filepath.Join(loop.StateDir(), "runs"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("run records = %v, %v", entries, err)
	}
	dir := filepath.Join(loop.StateDir(), "runs", entries[0].Name())
	for _, name := range []string{"telemetry.jsonl", "config.json", "result.json", "replies/plan.md", "replies/verify-0.txt", "replies/verify-1.txt"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("record %s: %v", name, err)
		}
	}
	if !strings.Contains(fake.requests[0].Prompt, s.Task) {
		t.Error("recorded task missing")
	}
	for i, path := range fake.replies {
		if strings.HasPrefix(path, "implement-") && !strings.Contains(fake.requests[i].Prompt, "go test <tests or packages>") {
			t.Error("configured check missing from implementer prompt")
		}
	}
}

func requirePlayBindings(t *testing.T, s ensemble.Standing, fake *playCAO) {
	t.Helper()
	for i, req := range fake.requests {
		path := fake.replies[i]
		part := playReplyPart(path)
		var want ensemble.StandingChair
		for _, c := range s.Chairs {
			if c.Part == part {
				want = c
				break
			}
		}
		if part == "reviewer" && strings.HasSuffix(path, "-2.md") {
			want = s.Chairs[3]
		}
		terminal := want.TerminalID
		if path == "lessons-reply.json" {
			terminal = ""
		}
		if req.ReuseTerminalID != terminal || req.Provider != want.Binding.Provider || req.Agent != want.Binding.Profile || req.Model != want.Binding.Model || req.Teardown || req.WorkingDirectory != s.Worktree {
			t.Errorf("%s: request = %+v, want occupant = %+v", path, req, want)
		}
	}
}

func TestPlayReadsTheTaskFileAgainAtPlayTimeAndReportsUnreadableFiles(t *testing.T) {
	for _, missing := range []bool{false, true} {
		t.Run(fmt.Sprint(missing), func(t *testing.T) {
			s := playStanding(t)
			s.TaskFile = filepath.Join(t.TempDir(), "task.md")
			writeFile(t, s.TaskFile, "original task")
			savePlayStanding(t, s)
			if missing {
				if err := os.Remove(s.TaskFile); err != nil {
					t.Fatal(err)
				}
			} else {
				writeFile(t, s.TaskFile, "edited task at play time")
			}
			fake := &playCAO{}
			srv := fake.serve(t)
			_, err := capturePlay(t, []string{s.Session, "-cao", srv.URL})
			if missing {
				if err == nil || !strings.Contains(err.Error(), s.TaskFile) || len(fake.requests) != 0 {
					t.Fatalf("unreadable task: %v, requests = %v", err, fake.requests)
				}
			} else if err != nil || len(fake.requests) == 0 || !strings.Contains(fake.requests[0].Prompt, "edited task at play time") || strings.Contains(fake.requests[0].Prompt, s.Task) {
				t.Fatalf("task file play: %v, requests = %v", err, fake.requests)
			}
		})
	}
}

func TestPlayKeepsEachChairsRecordedBindingEvenWhenSizingAndEscalationAreConfigured(t *testing.T) {
	s := playStanding(t)
	for i := range s.Chairs {
		s.Chairs[i].Binding.Model = ""
		s.Chairs[i].Binding.Tier = ""
	}
	savePlayStanding(t, s)
	writeFile(t, loop.UserConfigPath(loop.Home()), "[sizing]\nmode = 'on'\nescalate_after = 1\n[tiers.codex.large]\nmodel = 'different-model'\neffort = 'xhigh'\n")
	fake := &playCAO{changes: true}
	srv := fake.serve(t)
	out, err := capturePlay(t, []string{s.Session, "-cao", srv.URL, "-max-rounds", "3"})
	if !errors.Is(err, errNotApproved) || !strings.Contains(out, "rounds_exhausted after 3 round(s)") {
		t.Fatalf("play: %v\n%s", err, out)
	}
	requirePlayBindings(t, s, fake)
	if len(fake.requests) != 23 {
		t.Fatalf("requests across rounds: %d", len(fake.requests))
	}
	for _, req := range fake.requests {
		if strings.Contains(req.Prompt, "End the plan with one line") {
			t.Error("play requested sizing")
		}
	}
	requirePlayStillStanding(t, s, fake, out)
}

func TestPlayHonoursVerifyAndTimeoutFlags(t *testing.T) {
	s := playStanding(t)
	writeFile(t, filepath.Join(s.Worktree, loop.ConfigFile), "[test]\nverify = 'exit 7'\n")
	fake := &playCAO{}
	srv := fake.serve(t)
	out, err := capturePlay(t, []string{s.Session, "-cao", srv.URL, "-verify", "true", "-timeout", "7s"})
	if err != nil || !strings.Contains(out, "approved after 1 round(s)") {
		t.Fatalf("play: %v\n%s", err, out)
	}
	for i, req := range fake.requests {
		want := (7 * time.Second).Seconds()
		if fake.replies[i] == "lessons-reply.json" {
			want = loop.DefaultLessonsTimeout.Seconds()
		}
		if req.Timeout != want {
			t.Errorf("%s timeout = %v, want %v", fake.replies[i], req.Timeout, want)
		}
	}
}

func TestPlayLeavesTheSessionStandingWhenTheRunFails(t *testing.T) {
	for _, noReply := range []bool{false, true} {
		t.Run(fmt.Sprint(noReply), func(t *testing.T) {
			s := playStanding(t)
			fake := &playCAO{failPart: "gate"}
			if noReply {
				fake.failPart, fake.noReplyPart = "", "gate"
			}
			srv := fake.serve(t)
			out, err := capturePlay(t, []string{s.Session, "-cao", srv.URL})
			if !errors.Is(err, errNotApproved) || !strings.Contains(out, "failed after 1 round(s)") {
				t.Fatalf("play: %v\n%s", err, out)
			}
			requirePlayStillStanding(t, s, fake, out)
		})
	}
}

func TestPlayPreservesTheRecordedGateWhenInsteadOfUsingTheDefault(t *testing.T) {
	s := playStanding(t)
	s.GateWhen = loop.GateWhenAlways
	savePlayStanding(t, s)
	fake := &playCAO{noReplyPart: "reviewer"}
	srv := fake.serve(t)
	_, err := capturePlay(t, []string{s.Session, "-cao", srv.URL})
	if !errors.Is(err, errNotApproved) {
		t.Fatalf("run error = %v, want %v", err, errNotApproved)
	}
	entries, err := os.ReadDir(filepath.Join(loop.StateDir(), "runs"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("run entries: %v, %v", entries, err)
	}
	data, err := os.ReadFile(filepath.Join(loop.StateDir(), "runs", entries[0].Name(), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cfg loop.Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.GateWhen != s.GateWhen || cfg.Policy != nil {
		t.Fatalf("play config = %+v", cfg)
	}
	if !slices.Contains(fake.replies, "plan.md") {
		t.Fatal("play did not start")
	}
}

func TestPlayRefusingUnbriefedChairsSuggestsKmReseatForEachChair(t *testing.T) {
	s := playStanding(t)
	s.Chairs[1].Briefed = false
	s.Chairs[3].Briefed, s.Chairs[3].TerminalID = false, ""
	savePlayStanding(t, s)
	fake := &playCAO{}
	srv := fake.serve(t)
	out, err := capturePlay(t, []string{s.Session, "-cao", srv.URL})
	for _, want := range []string{"km reseat " + s.Session + " " + s.Chairs[1].Address, "km reseat " + s.Session + " " + s.Chairs[3].Address, "not briefed", "km ensemble down " + s.Session, "km rehearse"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("unbriefed error %v lacks %q", err, want)
		}
	}
	if len(fake.requests) != 0 || len(fake.checked) != 0 {
		t.Errorf("requests = %+v, checked = %v", fake.requests, fake.checked)
	}
	requirePlayStillStanding(t, s, fake, out)
}

func TestPlayAsksForLessonsInAFreshTerminalBesideTheStandingGateAndReleasesIt(t *testing.T) {
	s := playStanding(t)
	fake := &playCAO{}
	srv := fake.serve(t)
	out, err := capturePlay(t, []string{s.Session, "-cao", srv.URL})
	if err != nil || !strings.Contains(out, "approved after 1 round(s)") {
		t.Fatalf("play: %v\n%s", err, out)
	}
	asked := 0
	for i, req := range fake.requests {
		if fake.replies[i] != "lessons-reply.json" {
			if req.ReuseTerminalID == "" {
				t.Errorf("%s did not play in its standing occupant", fake.replies[i])
			}
			continue
		}
		asked++
		if req.ReuseTerminalID != "" || req.SessionName != s.Session {
			t.Errorf("lessons asked in terminal %q of session %q, want a fresh terminal in %s", req.ReuseTerminalID, req.SessionName, s.Session)
		}
		if want := fmt.Sprintf("step-%d", i+1); !slices.Contains(fake.released, want) {
			t.Errorf("released %v, want the lessons terminal %s", fake.released, want)
		}
	}
	if asked != 1 || len(fake.released) != 1 {
		t.Errorf("lessons asked %d time(s), released %v", asked, fake.released)
	}
	requirePlayStillStanding(t, s, fake, out)
}
