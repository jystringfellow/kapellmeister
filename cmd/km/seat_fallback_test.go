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
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"unicode"

	"github.com/jystringfellow/kapellmeister/internal/cao"
	"github.com/jystringfellow/kapellmeister/internal/ensemble"
	"github.com/jystringfellow/kapellmeister/internal/loop"
)

type fallbackCAO struct {
	profileURL string
	beforeSeat func(cao.Launch)
	fake       playCAO
	mu         sync.Mutex
	blocked    map[string]bool
	pane       string
	checked    map[string]int
	launches   []cao.Launch
	sessions   map[string]string
	deleted    []string
	failPodium bool
}

func (f *fallbackCAO) serve(t *testing.T) *httptest.Server {
	inner := f.fake.serve(t)
	f.checked = map[string]int{}
	f.sessions = map[string]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST" && r.URL.Path == "/sessions":
			f.mu.Lock()
			q := r.URL.Query()
			launch := cao.Launch{Provider: q.Get("provider"), Profile: q.Get("agent_profile"), Model: q.Get("model")}
			id := fmt.Sprintf("seat-%d", len(f.launches))
			if f.beforeSeat != nil {
				f.beforeSeat(launch)
			}
			f.launches = append(f.launches, launch)
			f.sessions[id] = launch.Provider
			f.mu.Unlock()
			if f.failPodium && !strings.Contains(q.Get("session_name"), "preflight") && !strings.Contains(q.Get("name"), "preflight") {
				http.Error(w, "podium refused", 400)
				return
			}
			json.NewEncoder(w).Encode(cao.Terminal{ID: id, SessionName: id})
			return
		case r.Method == "DELETE" && strings.HasPrefix(r.URL.Path, "/sessions/"):
			f.mu.Lock()
			f.deleted = append(f.deleted, strings.TrimPrefix(r.URL.Path, "/sessions/"))
			f.mu.Unlock()
			fmt.Fprint(w, `{}`)
			return
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/output"):
			json.NewEncoder(w).Encode(map[string]string{"output": f.pane})
			return
		case r.Method == "POST" && r.URL.Path == "/terminals/run-step":
			body, _ := io.ReadAll(r.Body)
			var req cao.StepRequest
			json.Unmarshal(body, &req)
			if strings.Contains(req.Prompt, ".kapellmeister/runs/preflight/") {
				f.mu.Lock()
				provider := f.sessions[req.ReuseTerminalID]
				f.checked[provider]++
				f.mu.Unlock()
				if f.blocked[provider] {
					http.Error(w, "check failed", 400)
					return
				}
				path := regexp.MustCompile(`\.kapellmeister/runs/preflight/[^\s]+\.txt`).FindString(req.Prompt)
				if path == "" {
					t.Error("missing preflight path")
					http.Error(w, "missing path", 400)
					return
				}
				if err := os.WriteFile(filepath.Join(req.WorkingDirectory, path), []byte("ready"), 0600); err != nil {
					t.Error(err)
				}
				json.NewEncoder(w).Encode(cao.StepResponse{TerminalID: req.ReuseTerminalID, Status: "completed"})
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
		}
		target := inner.URL
		if f.profileURL != "" && strings.HasPrefix(r.URL.Path, "/agents/") {
			target = f.profileURL
		}
		req, err := http.NewRequestWithContext(r.Context(), r.Method, target+r.URL.RequestURI(), r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Error(err)
			return
		}
		defer resp.Body.Close()
		w.WriteHeader(resp.StatusCode)
		io.Copy(w, resp.Body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func fallbackFixture(t *testing.T, f *fallbackCAO, text bool) (string, []string) {
	t.Helper()
	s := playStanding(t)
	writeFile(t, filepath.Join(loop.Home(), "config.toml"), "[agents]\navailable = ['a', 'b', 'same']\n[agents.define]\na = 'pa:a'\nb = 'pb:b'\nsame = 'pa:other'\nmissing = 'pc:c'\n")
	writeFile(t, filepath.Join(s.Worktree, loop.ConfigFile), "[test]\nverify = 'true'\n[run]\npreflight_timeout = '5s'\npreflight_retry_wait = '0s'\nreply_grace = '1ms'\nlessons = false\n[sizing]\nmode = 'off'\n")
	content := `[sections.build.chairs.plan]
part = 'planner'
agents = ['b']
[sections.build.chairs.dev]
part = 'implementer'
agents = ['b']
[sections.review.chairs.lead]
part = 'reviewer'
agents = ['a', 'b']
[sections.review.chairs.second]
part = 'reviewer'
agents = ['b']
[sections.close.chairs.gate]
part = 'gate'
agents = ['b']
[sections.close.chairs.size]
part = 'classifier'
agents = ['a', 'b']
`
	path := filepath.Join(loop.Home(), ensemble.UserDir, "thorough.toml")
	if text {
		path = filepath.Join(t.TempDir(), "thorough.toml")
	}
	writeFile(t, path, content)
	srv := f.serve(t)
	args := []string{"-task", "probe", "-worktree", s.Worktree, "-cao", srv.URL}
	if text {
		args = append(args, "-ensemble-file", path)
	} else {
		args = append(args, "-ensemble", "thorough")
	}
	return path, args
}
func fallbackRecord(t *testing.T, dir, name string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	return m
}
func requireFallbackLists(t *testing.T, m map[string]any) {
	t.Helper()
	changes, ok := m["seats_changed"].([]any)
	if !ok || len(changes) != 2 {
		t.Fatalf("seats_changed = %v", m["seats_changed"])
	}
	lost, ok := m["providers_lost"].([]any)
	if !ok || len(lost) != 3 {
		t.Fatalf("providers_lost = %v", m["providers_lost"])
	}
	for i, section := range []string{"review", "close", ""} {
		entry := lost[i].(map[string]any)
		blocked := entry["blocked"].([]any)
		if entry["section"] != section || entry["before"] != float64(2) || entry["after"] != float64(1) || len(blocked) != 1 || blocked[0] != "pa" {
			t.Fatal(entry)
		}
	}

	for _, v := range changes {
		c := v.(map[string]any)
		if c["blocked_agent"] != "a" || c["seated_agent"] != "b" || c["blocked_provider"] != "pa" || c["seated_provider"] != "pb" || c["reason"] != paneBudget {
			t.Fatal(c)
		}
	}
}
func TestARunWhosePreflightPassesKeepsTheFirstAgentsAndRecordsNoSeatChange(t *testing.T) {
	f := &fallbackCAO{}
	_, args := fallbackFixture(t, f, false)
	out, err := captureStdout(t, func() error { return runCommand(args) })
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if strings.Contains(out, "takes the seat") {
		t.Fatal(out)
	}
	result := fallbackRecord(t, onlyRunRecord(t), "result.json")
	if result["seats_changed"] != nil {
		t.Fatal(result)
	}
	if f.launches[len(f.launches)-1].Provider != "pa" {
		t.Fatal(f.launches)
	}
}
func TestABlockedFirstAgentIsReplacedForTheStepsThePodiumAndTheRecord(t *testing.T) {
	for _, text := range []bool{false, true} {
		t.Run(fmt.Sprint(text), func(t *testing.T) {
			f := &fallbackCAO{blocked: map[string]bool{"pa": true}, pane: paneBudget}
			path, args := fallbackFixture(t, f, text)
			original, _ := os.ReadFile(path)
			out, err := captureStdout(t, func() error { return runCommand(args) })
			if err != nil {
				t.Fatalf("%v\n%s", err, out)
			}
			line := "  seat    review.lead@thorough: a was BLOCKED (untrusted text from the agent's pane: " + paneBudget + "); b takes the seat"
			if !strings.Contains(out, line) || !strings.Contains(out, "seats changed") {
				t.Fatal(out)
			}
			dir := onlyRunRecord(t)
			requireFallbackLists(t, fallbackRecord(t, dir, "result.json"))
			for _, req := range f.fake.requests {
				if req.Provider != "pb" {
					t.Fatalf("step uses blocked provider: %+v", req)
				}
			}
			if f.launches[len(f.launches)-1].Provider != "pb" {
				t.Fatal(f.launches)
			}
			cfg, _ := os.ReadFile(filepath.Join(dir, "config.json"))
			if strings.Contains(string(cfg), `"provider":"pa"`) {
				t.Fatal(string(cfg))
			}
			now, _ := os.ReadFile(path)
			if !bytes.Equal(now, original) {
				t.Fatal("ensemble changed")
			}
			for id, p := range f.sessions {
				if p == "pa" && !strings.Contains(strings.Join(f.deleted, ","), id) {
					t.Fatalf("blocked session %s leaked", id)
				}
			}
			if text {
				saved, _ := os.ReadFile(filepath.Join(dir, "ensemble.toml"))
				if !bytes.Equal(saved, original) {
					t.Fatal("saved ensemble changed")
				}
			}
		})
	}
}
func TestACandidateWhoseProviderWasBlockedIsSkippedAndTheSkipRecorded(t *testing.T) {
	f := &fallbackCAO{blocked: map[string]bool{"pa": true}, pane: paneBudget}
	path, args := fallbackFixture(t, f, false)
	data, _ := os.ReadFile(path)
	writeFile(t, path, strings.ReplaceAll(string(data), "['a', 'b']", "['a', 'same', 'b']"))
	out, err := captureStdout(t, func() error { return runCommand(args) })
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	c := fallbackRecord(t, onlyRunRecord(t), "result.json")["seats_changed"].([]any)[0].(map[string]any)
	tried := c["tried"].([]any)
	if len(tried) != 1 || tried[0].(map[string]any)["agent"] != "same" || !strings.Contains(tried[0].(map[string]any)["reason"].(string), "provider pa was BLOCKED") {
		t.Fatal(c)
	}
	if f.checked["pa"] != 2 {
		t.Fatal(f.checked)
	}
}
func TestAnAlreadyPassedCandidateIsNotCheckedAgain(t *testing.T) {
	f := &fallbackCAO{blocked: map[string]bool{"pa": true}, pane: paneBudget}
	_, args := fallbackFixture(t, f, false)
	out, err := captureStdout(t, func() error { return runCommand(args) })
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if f.checked["pb"] != 1 {
		t.Fatal(f.checked)
	}
}
func TestWhenEveryCandidateIsBlockedTheRunFailsNamingEachAgentAndWhy(t *testing.T) {
	f := &fallbackCAO{blocked: map[string]bool{"pa": true, "pb": true}, pane: paneBudget}
	path, args := fallbackFixture(t, f, false)
	data, _ := os.ReadFile(path)
	writeFile(t, path, strings.ReplaceAll(string(data), "['a', 'b']", "['a', 'missing', 'b']"))
	_, err := captureStdout(t, func() error { return runCommand(args) })
	if err == nil {
		t.Fatal("wanted failure")
	}
	for _, text := range []string{"review.lead@thorough", "a:", "missing: not in [agents] available", "b:", "provider pb was BLOCKED", paneBudget, "untrusted text from the agent's pane"} {
		if !strings.Contains(err.Error(), text) {
			t.Errorf("missing %q: %v", text, err)
		}
	}
	if len(statusRows(t)) != 0 || len(reportRows(t)) != 0 {
		t.Fatal("failed foreground preflight left a record")
	}
}
func TestSeatFallbackOffInConfigOrByFlagFailsPreflightNamingTheLaterAgents(t *testing.T) {
	for _, mode := range []string{"config", "flag", "override"} {
		t.Run(mode, func(t *testing.T) {
			f := &fallbackCAO{blocked: map[string]bool{"pa": true}, pane: paneBudget}
			_, args := fallbackFixture(t, f, false)
			if mode != "flag" {
				path := filepath.Join(args[3], loop.ConfigFile)
				data, _ := os.ReadFile(path)
				writeFile(t, path, strings.Replace(string(data), "[run]", "[run]\nseat_fallback = false", 1))
			}
			if mode == "flag" {
				args = append(args, "-seat-fallback=false")
			}
			if mode == "override" {
				args = append(args, "-seat-fallback=true")
			}
			out, err := captureStdout(t, func() error { return runCommand(args) })
			if mode == "override" {
				if err != nil {
					t.Fatalf("%v\n%s", err, out)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), "review.lead@thorough also lists b; seat_fallback is off") || !strings.Contains(out, "seat fallback off") {
				t.Fatalf("%v\n%s", err, out)
			}
		})
	}
}
func TestLostProvidersReachOutputSummaryStatusWaitReportAndTheMCPTools(t *testing.T) {
	f := &fallbackCAO{blocked: map[string]bool{"pa": true}, pane: paneBudget}
	_, args := fallbackFixture(t, f, false)
	observed := false
	f.fake.afterReply = func(_ string, name string) {
		if !strings.HasPrefix(name, "plan") || observed {
			return
		}
		observed = true
		dir := onlyRunRecord(t)
		requireFallbackLists(t, fallbackRecord(t, dir, "current.json"))
		var out bytes.Buffer
		if err := statusCommand([]string{"-json"}, &out, io.Discard); err != nil {
			t.Error(err)
			return
		}
		var data map[string]any
		json.Unmarshal(out.Bytes(), &data)
		requireFallbackLists(t, data["runs"].([]any)[0].(map[string]any))
		c := startMCPClient(t, false)
		defer c.close()
		d := mcpData(t, c.call("status", map[string]any{}))
		requireFallbackLists(t, d["runs"].([]any)[0].(map[string]any))
	}
	out, err := captureStdout(t, func() error { return runCommand(args) })
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !observed {
		t.Fatal("no live observation")
	}
	for _, line := range []string{"review.lead@thorough now plays on the same provider as the implementer (pb)", "review.lead@thorough now plays on the same provider as review.second@thorough (pb)", "section review played with 1 provider instead of 2: pa was blocked", "section close played with 1 provider instead of 2: pa was blocked", "the ensemble played with 1 provider instead of 2: pa was blocked"} {
		if strings.Count(out, line) != 2 {
			t.Errorf("progress and summary missing %q: %s", line, out)
		}
	}
	dir := onlyRunRecord(t)
	id := filepath.Base(dir)
	var report bytes.Buffer
	if err := reportCommand([]string{}, &report, io.Discard); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(report.String(), "seats changed: 1") || !strings.Contains(report.String(), id) || !strings.Contains(report.String(), "section review played with 1 provider") {
		t.Fatal(report.String())
	}
	report.Reset()
	if err := reportCommand([]string{"-json"}, &report, io.Discard); err != nil {
		t.Fatal(err)
	}
	var data map[string]any
	json.Unmarshal(report.Bytes(), &data)
	requireFallbackLists(t, data["runs"].([]any)[0].(map[string]any))
	var wait bytes.Buffer
	if err := waitCommand([]string{"-json", id}, &wait, io.Discard); err != nil {
		t.Fatal(err)
	}
	json.Unmarshal(wait.Bytes(), &data)
	requireFallbackLists(t, data["result"].(map[string]any))
	wait.Reset()
	if err := waitCommand([]string{id}, &wait, io.Discard); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(wait.String(), "seats changed") || !strings.Contains(wait.String(), "the ensemble played with 1 provider instead of 2") {
		t.Fatal(wait.String())
	}

	c := startMCPClient(t, false)
	defer c.close()
	d := mcpData(t, c.call("wait", map[string]any{"run": id}))
	requireFallbackLists(t, d["result"].(map[string]any))
	d = mcpData(t, c.call("report", map[string]any{}))
	requireFallbackLists(t, d["runs"].([]any)[0].(map[string]any))
}
func TestPaneControlCharactersInASeatReasonComeOutPrintableAndBounded(t *testing.T) {
	f := &fallbackCAO{blocked: map[string]bool{"pa": true}, pane: strings.Repeat("\x1b[31m\r\u202e"+strings.Repeat("é", 500)+"\n", 6)}
	_, args := fallbackFixture(t, f, false)
	out, err := captureStdout(t, func() error { return runCommand(args) })
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	m := fallbackRecord(t, onlyRunRecord(t), "result.json")
	reason := m["seats_changed"].([]any)[0].(map[string]any)["reason"].(string)
	if len([]rune(reason)) > 300 {
		t.Fatal(len([]rune(reason)))
	}
	for _, r := range reason {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			t.Fatalf("unsafe reason %q", reason)
		}
	}
	count := 0
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "takes the seat") && !strings.Contains(line, "untrusted text from the agent's pane: ") {
			t.Fatal(line)
		}
		if strings.HasPrefix(line, "  seat    ") && strings.Contains(line, "takes the seat") {
			count++
			assertPrintableFallbackReason(t, line, "); ")
		}
	}
	if count == 0 {
		t.Fatal("no seat progress line")
	}

	f = &fallbackCAO{blocked: map[string]bool{"pa": true, "pc": true}, pane: f.pane}
	path, args := fallbackFixture(t, f, false)
	fallbackCandidates(t, path, "['a', 'c']")
	_, err = captureStdout(t, func() error { return runCommand(args) })
	if err == nil {
		t.Fatal("expected blocked error")
	}
	found := map[string]bool{}
	for _, line := range strings.Split(err.Error(), "\n") {
		for _, agent := range []string{"a", "c"} {
			if strings.HasPrefix(line, "  "+agent+": BLOCKED (") {
				found[agent] = true
				assertPrintableFallbackReason(t, line, ")")
			}
		}
	}
	if !found["a"] || !found["c"] {
		t.Fatalf("missing blocked agents: %v\n%v", found, err)
	}
}

func assertPrintableFallbackReason(t *testing.T, line, end string) {
	t.Helper()
	for _, r := range line {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			t.Fatalf("unsafe line %q", line)
		}
	}
	const marker = "untrusted text from the agent's pane: "
	start := strings.Index(line, marker)
	stop := strings.LastIndex(line, end)
	if start < 0 || stop < start+len(marker) {
		t.Fatalf("missing reason in %q", line)
	}
	reason := line[start+len(marker) : stop]
	if len([]rune(reason)) > 300 || !strings.Contains(reason, "é") {
		t.Fatalf("invalid reason (%d runes): %q", len([]rune(reason)), reason)
	}
}

func fallbackCandidates(t *testing.T, path string, entries string) {
	t.Helper()
	data, _ := os.ReadFile(path)
	writeFile(t, path, strings.ReplaceAll(string(data), "['a', 'b']", entries))
	user := filepath.Join(loop.Home(), "config.toml")
	data, _ = os.ReadFile(user)
	text := strings.Replace(string(data), "['a', 'b', 'same']", "['a', 'b', 'same', 'c', 'd', 'alt', 'claude']", 1)
	writeFile(t, user, text+"c = 'pc:c'\nd = 'pd:d'\nalt = 'pc:other'\n")
}
func TestACandidateGetsTheSameRetriesAsTheFirstAgentAndLaterSeatsSkipItsBlockedProvider(t *testing.T) {
	f := &fallbackCAO{blocked: map[string]bool{"pa": true, "pc": true}, pane: paneBudget}
	path, args := fallbackFixture(t, f, false)
	fallbackCandidates(t, path, "['a', 'c', 'alt', 'd']")
	out, err := captureStdout(t, func() error { return runCommand(args) })
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if f.checked["pa"] != 2 || f.checked["pc"] != 2 || f.checked["pd"] != 1 {
		t.Fatal(f.checked)
	}
	changes := fallbackRecord(t, onlyRunRecord(t), "result.json")["seats_changed"].([]any)
	for _, v := range changes {
		c := v.(map[string]any)
		tried := c["tried"].([]any)
		if c["seated_agent"] != "d" || len(tried) != 2 || tried[0].(map[string]any)["agent"] != "c" || !strings.Contains(tried[1].(map[string]any)["reason"].(string), "provider pc was BLOCKED") {
			t.Fatal(c)
		}
	}
}
func TestWhenANewCandidateIsBlockedTheFailureNamesItsPaneReason(t *testing.T) {
	f := &fallbackCAO{blocked: map[string]bool{"pa": true, "pc": true}, pane: paneBudget}
	path, args := fallbackFixture(t, f, false)
	fallbackCandidates(t, path, "['a', 'c']")
	_, err := captureStdout(t, func() error { return runCommand(args) })
	if err == nil || !strings.Contains(err.Error(), "c: BLOCKED (untrusted text from the agent's pane: "+paneBudget+")") {
		t.Fatal(err)
	}
	if f.checked["pc"] != 2 {
		t.Fatal(f.checked)
	}
	kept := 0
	for id, p := range f.sessions {
		if p == "pc" && !slices.Contains(f.deleted, id) {
			kept++
		}
	}
	if kept != 1 {
		t.Fatalf("wanted last blocked candidate attachable: sessions %v, deleted %v", f.sessions, f.deleted)
	}

}
func TestASubstitutesEffortProfilesAreInstalledBeforeItIsSeated(t *testing.T) {
	f := &fallbackCAO{blocked: map[string]bool{"pa": true}, pane: paneBudget}
	path, args := fallbackFixture(t, f, false)
	fallbackCandidates(t, path, "['a', 'claude']")
	_, installed := fakeProfileInstall(t)
	profiles := permissionCAO(t, map[string]cao.ProfilePermissions{"km-claude": {PermissionMode: "default", AllowedTools: []string{"Read"}}}, nil)
	f.profileURL = profiles.URL
	f.beforeSeat = func(launch cao.Launch) {
		if launch.Provider != "claude_code" {
			return
		}
		data, err := os.ReadFile(installed)
		if err != nil || !strings.Contains(string(data), "name: "+launch.Profile+"\n") {
			t.Errorf("profile %s was not installed before seating: %s %v", launch.Profile, data, err)
		}
	}
	out, err := captureStdout(t, func() error { return runCommand(args) })
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(out, "installed CAO profile km-claude-high") {
		t.Fatal(out)
	}
}
func TestADetachedRunThatFailsAfterSubstitutingStillRecordsTheSeatChange(t *testing.T) {
	f := &fallbackCAO{blocked: map[string]bool{"pa": true}, pane: paneBudget, failPodium: true}
	_, args := fallbackFixture(t, f, false)
	id := "20261125-174834-abcdef"
	launch := filepath.Join(t.TempDir(), "launch.json")
	if err := writeJSON(launch, detachedLaunchRecord{RunID: id, Worktree: args[3], Task: "probe"}); err != nil {
		t.Fatal(err)
	}
	args = append(args, "-detached-launch", launch)
	out, err := captureStdout(t, func() error { return runAt(context.Background(), args, os.Stdout, io.Discard, nil) })
	if err == nil || !strings.Contains(err.Error(), "podium refused") {
		t.Fatalf("%v\n%s", err, out)
	}
	dir := filepath.Join(loop.StateDir(), "runs", id)
	requireFallbackLists(t, fallbackRecord(t, dir, "result.json"))
	requireFallbackLists(t, fallbackRecord(t, dir, "current.json"))
	if !strings.Contains(out, "seats changed") {
		t.Fatal(out)
	}
}
func TestASubstitutedRunsCurrentRecordKeepsItsSeatingSourceSectionsAndAddresses(t *testing.T) {
	for _, source := range []string{"text", "repository"} {
		t.Run(source, func(t *testing.T) {
			f := &fallbackCAO{blocked: map[string]bool{"pa": true}, pane: paneBudget, failPodium: true}
			path, args := fallbackFixture(t, f, source == "text")
			id := "20261125-174834-abcdef"
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if source == "text" {
				writeFile(t, filepath.Join(loop.StateDir(), "runs", id, "ensemble.toml"), string(data))
			} else {
				writeFile(t, filepath.Join(args[3], ensemble.RepoDir, "thorough.toml"), string(data))
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			}
			launch := filepath.Join(t.TempDir(), "launch.json")
			if err := writeJSON(launch, detachedLaunchRecord{RunID: id, Worktree: args[3], Task: "probe"}); err != nil {
				t.Fatal(err)
			}
			args = append(args, "-detached-launch", launch)
			out, err := captureStdout(t, func() error { return runAt(context.Background(), args, os.Stdout, io.Discard, nil) })
			if err == nil || !strings.Contains(err.Error(), "podium refused") {
				t.Fatalf("%v\n%s", err, out)
			}
			record := fallbackRecord(t, filepath.Join(loop.StateDir(), "runs", id), "current.json")
			seating := record["seating"].(map[string]any)
			if seating["name"] != "thorough" || seating["source"] != source {
				t.Errorf("seating name/source: %v", seating)
			}
			var sections, addresses []string
			for _, item := range seating["sections"].([]any) {
				section := item.(map[string]any)
				sections = append(sections, section["name"].(string))
				for _, item := range section["chairs"].([]any) {
					chair := item.(map[string]any)
					address := chair["address"].(string)
					addresses = append(addresses, address)
					if address == "review.lead@thorough" || address == "close.size@thorough" {
						if chair["provider"] != "pb" || chair["agent"] != "b" {
							t.Errorf("substituted chair: %v", chair)
						}
					}
				}
			}
			if !slices.Equal(sections, []string{"build", "review", "close"}) {
				t.Errorf("sections: %v", sections)
			}
			want := []string{"build.plan@thorough", "build.dev@thorough", "review.lead@thorough", "review.second@thorough", "close.gate@thorough", "close.size@thorough"}
			if !slices.Equal(addresses, want) {
				t.Errorf("addresses: %v", addresses)
			}
			for _, item := range record["seats_changed"].([]any) {
				address := item.(map[string]any)["address"].(string)
				if !slices.Contains(addresses, address) {
					t.Errorf("changed address %s absent from seating", address)
				}
			}
		})
	}
}

func TestKmConfigShowsSeatFallback(t *testing.T) {
	f := &fallbackCAO{}
	_, args := fallbackFixture(t, f, false)
	out, err := captureStdout(t, func() error { return configCommand([]string{"-worktree", args[3]}) })
	if err != nil || !strings.Contains(out, "seat fallback on") {
		t.Fatalf("%v\n%s", err, out)
	}
}

func TestMembersSharingABlockedConfigFollowTheirOwnListsAndPreserveTheirChairSettings(t *testing.T) {
	f := &fallbackCAO{blocked: map[string]bool{"pa": true}, pane: paneBudget}
	path, args := fallbackFixture(t, f, false)
	fallbackCandidates(t, path, "['a', 'c']")
	data, _ := os.ReadFile(path)
	text := strings.Replace(string(data), "[sections.review.chairs.lead]\n", "[sections.review.chairs.lead]\ntimeout = '7m'\nfocus = 'security'\n", 1)
	text = strings.Replace(text, "[sections.close.chairs.size]\npart = 'classifier'\nagents = ['a', 'c']", "[sections.close.chairs.size]\npart = 'classifier'\nagents = ['a', 'd']", 1)
	writeFile(t, path, text)
	out, err := captureStdout(t, func() error { return runCommand(args) })
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	changes := fallbackRecord(t, onlyRunRecord(t), "result.json")["seats_changed"].([]any)
	if changes[0].(map[string]any)["seated_agent"] != "c" || changes[1].(map[string]any)["seated_agent"] != "d" {
		t.Fatal(changes)
	}
	found := false
	for _, req := range f.fake.requests {
		if strings.Contains(req.Prompt, "security") {
			found = true
			if req.Provider != "pc" {
				t.Fatal(req)
			}
		}
	}
	if !found {
		t.Fatal("chair focus lost")
	}
	var cfg loop.Config
	data, _ = os.ReadFile(filepath.Join(onlyRunRecord(t), "config.json"))
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Chairs.Reviewers[0].Timeout.String() != "7m0s" {
		t.Fatal(cfg.Chairs.Reviewers)
	}
}

func TestDisablingPreflightKeepsOriginalEnsembleSeats(t *testing.T) {
	f := &fallbackCAO{blocked: map[string]bool{"pa": true}, pane: paneBudget}
	_, args := fallbackFixture(t, f, false)
	out, err := captureStdout(t, func() error { return runCommand(append(args, "-preflight-timeout=0")) })
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if len(f.checked) != 0 || strings.Contains(out, "takes the seat") || f.launches[len(f.launches)-1].Provider != "pa" {
		t.Fatal(out, f.checked, f.launches)
	}
}
