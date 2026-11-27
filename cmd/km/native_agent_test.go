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
	"slices"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/jystringfellow/kapellmeister/internal/cao"
	"github.com/jystringfellow/kapellmeister/internal/ensemble"
	"github.com/jystringfellow/kapellmeister/internal/loop"
)

func TestARunWithoutTheDefinitionAtTheBaseCommitIsRefusedBeforePreflight(t *testing.T) {
	dir := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "--allow-empty", "-qm", "base"}} {
		c := exec.Command("git", args...)
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("%s: %v", out, err)
		}
	}
	members := []ensemble.Member{{Chair: loop.Chair{NativeAgent: "demo"}}}
	err := resolveNativeMembers(dir, true, members)
	if err == nil || !strings.Contains(err.Error(), ".claude/agents/demo.md") {
		t.Fatalf("%v", err)
	}
}

func TestANativeProfileIsInstalledFromItsBasePermissions(t *testing.T) {
	t.Setenv(loop.StateEnv, t.TempDir())
	_, installed := fakeProfileInstall(t)
	srv := permissionCAO(t, map[string]cao.ProfilePermissions{"km-claude": {PermissionMode: "default", AllowedTools: []string{"Read"}}}, nil)
	p := loop.DefaultPolicy()
	chair := loop.Chair{Provider: "claude_code", Profile: "km-claude-native-demo", NativeBase: "km-claude", Agent: "claude", NativeAgent: "demo"}
	if err := ensureProfiles(context.Background(), cao.NewClient(srv.URL), &p, map[string][]loop.Chair{"plan": {chair}}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(installed)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "native_agent: \"demo\"") || strings.Contains(string(data), "claudeConfig:") {
		t.Fatal(string(data))
	}
}

func TestAHandWrittenProfileUnderANativeProfilesNameIsNotUsed(t *testing.T) {
	t.Setenv(loop.StateEnv, t.TempDir())
	fakeProfileInstall(t)
	srv := permissionCAO(t, map[string]cao.ProfilePermissions{"km-claude": {}, "km-claude-native-demo": {Description: "mine"}}, map[string]bool{"km-claude-native-demo": true})
	p := loop.DefaultPolicy()
	chair := loop.Chair{Provider: "claude_code", Profile: "km-claude-native-demo", NativeBase: "km-claude", Agent: "claude", NativeAgent: "demo"}
	if err := ensureProfiles(context.Background(), cao.NewClient(srv.URL), &p, map[string][]loop.Chair{"plan": {chair}}); err == nil {
		t.Fatal("accepted hand-written profile")
	}
}

func TestAChangedDefinitionFailsPreflightWithoutTryingAnotherAgent(t *testing.T) {
	f := &fallbackCAO{}
	srv := f.serve(t)
	agents, err := loop.BuiltInAgents().With(map[string]string{"claude2": "claude_code:km-claude2"})
	if err != nil {
		t.Fatal(err)
	}
	spec, err := ensemble.Check("band", "band.toml", "[sections.dev.chairs.one]\npart = 'planner'\nagents = ['claude', 'claude2']\nnative_agent = 'demo'\n", agents)
	if err != nil {
		t.Fatal(err)
	}
	members, err := spec.Bind(agents, []string{"claude", "claude2"})
	if err != nil {
		t.Fatal(err)
	}
	wt := t.TempDir()
	members[0].Chair.Definition = "repository"
	for _, args := range [][]string{{"init", "-q"}, {"-c", "user.name=T", "-c", "user.email=t@x", "commit", "--allow-empty", "-qm", "base"}} {
		c := exec.Command("git", args...)
		c.Dir = wt
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("%s: %v", out, err)
		}
	}
	outBase, err := exec.Command("git", "-C", wt, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	members[0].Chair.DefinitionBase = strings.TrimSpace(string(outBase))
	members[0].Chair.Manifest = &loop.NativeManifest{}
	writeFile(t, filepath.Join(wt, ".claude", "agents", "demo.md"), "edited definition")
	policy := loop.DefaultPolicy()
	settings := loop.Settings{Agents: []string{"claude", "claude2"}, Defined: agents, SeatFallback: true, PreflightTimeout: time.Second}
	_, changes, _, err := preflightSeats(context.Background(), cao.NewClient(srv.URL), wt, settings, spec, members, loop.Chairs{Planner: members[0].Chair}, &policy, io.Discard)
	if err == nil || !strings.Contains(err.Error(), ".claude/agents/demo.md") {
		t.Fatalf("%v", err)
	}
	if strings.Contains(err.Error(), "no listed agent passed preflight") || len(changes) != 0 || len(f.launches) != 0 || len(f.fake.requests) != 0 {
		t.Fatalf("definition failure tried fallback: changes=%+v launches=%+v requests=%+v error=%v", changes, f.launches, f.fake.requests, err)
	}
}

func TestReseatingANativeChairRefusesOtherProviders(t *testing.T) {
	m := ensemble.Member{Chair: loop.Chair{NativeAgent: "demo", Definition: "repository", DefinitionBase: "abc", Manifest: &loop.NativeManifest{}}}
	_, err := prepareMember(context.Background(), nil, loop.DefaultPolicy(), loop.BuiltInAgents(), "codex", m)
	if err == nil || !strings.Contains(err.Error(), "cannot play native agent") {
		t.Fatalf("%v", err)
	}
	_, err = prepareMember(context.Background(), nil, loop.DefaultPolicy(), loop.BuiltInAgents(), "claude@small", m)
	if err == nil || !strings.Contains(err.Error(), "agent definition sets the model") {
		t.Fatalf("%v", err)
	}
}

func TestReportPartsGivesANativeChairItsOwnRowWithUnknownCost(t *testing.T) {
	run := &reportPartRun{rows: map[reportPartKey]*reportPartRow{}, steps: map[reportVerdictKey]*reportPartKey{}, verdicts: map[reportVerdictKey]string{}, partKinds: map[string]map[string]bool{}}
	var rec reportPartRecord
	if err := json.Unmarshal([]byte(`{"step":"implement","chair":{"provider":"claude_code","agent":"claude","native_agent":"demo"},"model":"reported","tier":"large","usage":{"known":true,"input_tokens":1000000,"output_tokens":500000,"cache_read_tokens":0,"cache_write_tokens":0,"models":{"reported":{"input_tokens":1000000,"output_tokens":500000}}}}`), &rec); err != nil {
		t.Fatal(err)
	}
	run.addRecord(rec)
	parts := mergeReportParts([]reportRun{{parts: run}})
	if len(parts.Rows) != 1 {
		t.Fatal(parts.Rows)
	}
	row := parts.Rows[0]
	if row.Model != "" || row.ModelSource != "agent" || row.NativeAgent != "demo" || row.Agent != "claude" || len(row.Tiers) != 0 {
		t.Fatalf("%+v", row)
	}
	report := runReport{Parts: parts}
	prices := map[string]loop.ReportPrice{"reported": {Input: 2, Output: 4}}
	if cost := row.Usage.cost(prices); !cost.CostComplete || cost.CostUSD == nil || cost.CostUSD.String() != "4" {
		t.Fatalf("fixture must have fully priced usage: %+v", cost)
	}
	report.addCosts(prices, "test")
	if row.ReportCost == nil || row.CostComplete || row.CostUSD != nil || len(row.UnpricedModels) != 0 || row.ReportCost.text() != "unknown" {
		t.Fatalf("cost: %+v", row)
	}
	var out bytes.Buffer
	if err := printReportParts(&out, parts); err != nil || !strings.Contains(out.String(), "claude native demo") || !strings.Contains(out.String(), "unknown") {
		t.Fatalf("%s %v", out.String(), err)
	}
}

func TestStatusShowsTheNativeAgentAndItsModelSource(t *testing.T) {
	chair := loop.Chair{Provider: "claude_code", Agent: "claude", NativeAgent: "demo"}
	p := ensembleRun{Members: []ensemble.Member{{Section: "dev", Name: "one", Part: "planner", Chair: chair}}, Agents: loop.BuiltInAgents()}
	seating := recordSeating(p, loop.DefaultScore())
	got := seating.Sections[0].Chairs[0]
	if got.NativeAgent != "demo" || got.ModelSource != "agent" || got.Model != "" || got.Tier != "" {
		t.Fatalf("%+v", got)
	}
}

func TestTheSummaryNamesChangedNativeDefinitions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".claude", "agents", "demo.md")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("before"), 0644); err != nil {
		t.Fatal(err)
	}
	members := []ensemble.Member{{Section: "dev", Name: "one", Chair: loop.Chair{Agent: "claude", Provider: "claude_code", NativeAgent: "demo", Definition: "repository"}}}
	snapshot := nativeFileStates(dir, members)
	if err := os.WriteFile(path, []byte("after"), 0644); err != nil {
		t.Fatal(err)
	}
	var sum ensembleSummary
	recordNative(&sum, dir, "band", members, snapshot)
	if len(sum.NativeFilesChanged) != 1 || len(sum.NativeChairs) != 1 || sum.NativeChairs[0].ModelSource != "agent" {
		t.Fatalf("%+v", sum)
	}
}

func TestStatusTextShowsTheNativeAgent(t *testing.T) {
	root := t.TempDir()
	state := statusFixture(t, root, "run")
	state.Seating = &statusSeating{Name: "band", Sections: []statusSection{{Name: "dev", Chairs: []statusChair{{Address: "dev.one@band", NativeAgent: "demo", ModelSource: "agent"}}}}}
	saveStatusFixture(t, root, "run", state)
	out, diagnostics := captureStatus(t, root)
	if diagnostics != "" || !strings.Contains(out, "native run: dev.one@band native agent demo") {
		t.Fatalf("%s\n%s", out, diagnostics)
	}
	rows := statusRows(t, root)
	if len(rows) != 1 || len(rows[0].Ensemble.Sections) != 1 || len(rows[0].Ensemble.Sections[0].Chairs) != 1 {
		t.Fatalf("%+v", rows)
	}
	chair := rows[0].Ensemble.Sections[0].Chairs[0]
	if chair.NativeAgent != "demo" || chair.ModelSource != "agent" || chair.Model != "" || chair.Tier != "" {
		t.Fatalf("%+v", chair)
	}
}

func TestReseatingANativeChairKeepsItsDefinitionAndInstallsTheReplacementProfile(t *testing.T) {
	t.Setenv(loop.StateEnv, t.TempDir())
	_, installed := fakeProfileInstall(t)
	perms := cao.ProfilePermissions{PermissionMode: "default", AllowedTools: []string{"Read", "Write"}}
	srv := permissionCAO(t, map[string]cao.ProfilePermissions{"km-claude2": perms}, nil)
	agents, err := loop.BuiltInAgents().With(map[string]string{"claude2": "claude_code:km-claude2"})
	if err != nil {
		t.Fatal(err)
	}
	original := loop.Chair{Provider: "claude_code", Profile: "km-claude-native-demo", Agent: "claude", NativeBase: "km-claude", NativeAgent: "demo", Definition: "repository", DefinitionBase: strings.Repeat("a", 64), Manifest: &loop.NativeManifest{}, Address: "dev.one@band", Focus: "security"}
	got, err := prepareMember(context.Background(), cao.NewClient(srv.URL), loop.DefaultPolicy(), agents, "claude2", ensemble.Member{Section: "dev", Name: "one", Part: "implementer", Chair: original})
	if err != nil {
		t.Fatal(err)
	}
	want := original
	want.Agent, want.NativeBase, want.Profile = "claude2", "km-claude2", "km-claude2-native-demo"
	if got.Chair != want {
		t.Fatalf("reseated chair = %#v, want %#v", got.Chair, want)
	}
	data, err := os.ReadFile(installed)
	if err != nil {
		t.Fatal(err)
	}
	wantSource, err := loop.NativeProfileSource("claude_code", want.Profile, "demo", loop.Permissions{PermissionMode: perms.PermissionMode, AllowedTools: perms.AllowedTools})
	if err != nil || string(data) != wantSource {
		t.Fatalf("installed profile: %s\nwant: %s\nerror: %v", data, wantSource, err)
	}
}

func TestAChangedNativeDefinitionStopsThePodiumLaunchAfterPreflight(t *testing.T) {
	fakeProfileInstall(t)
	profiles := permissionCAO(t, map[string]cao.ProfilePermissions{"km-claude": {}}, nil)
	f := &fallbackCAO{profileURL: profiles.URL, failPodium: true}
	path, args := fallbackFixture(t, f, false)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, strings.Replace(string(data), "part = 'classifier'\nagents = ['a', 'b']", "part = 'classifier'\nagents = ['claude']\nnative_agent = 'demo'", 1))
	writeFile(t, filepath.Join(loop.Home(), "config.toml"), "[agents]\navailable = ['a', 'b', 'claude']\n[agents.define]\na = 'pa:a'\nb = 'pb:b'\n")
	wt := args[3]
	definition := filepath.Join(wt, ".claude", "agents", "demo.md")
	writeFile(t, definition, "---\nname: demo\n---\nbase agent")
	for _, gitArgs := range [][]string{{"add", ".claude/agents/demo.md"}, {"-c", "commit.gpgsign=false", "commit", "-qm", "native agent fixture"}} {
		if out, err := exec.Command("git", append([]string{"-C", wt}, gitArgs...)...).CombinedOutput(); err != nil {
			t.Fatalf("%s: %v", out, err)
		}
	}
	f.beforeSeat = func(launch cao.Launch) {
		if launch.Profile == "km-claude-native-demo" {
			if err := os.WriteFile(definition, []byte("edited after the preflight launch"), 0644); err != nil {
				t.Error(err)
			}
		}
	}
	out, err := captureStdout(t, func() error { return runCommand(args) })
	if err == nil || !strings.Contains(err.Error(), "differs from the base commit") {
		t.Fatalf("%v\n%s", err, out)
	}
	if f.checked["claude_code"] != 1 || len(f.fake.requests) != 0 || len(f.launches) != 3 {
		t.Fatalf("wanted only three successful preflight chairs, no podium or steps: checked=%v launches=%+v steps=%+v", f.checked, f.launches, f.fake.requests)
	}
}

func TestANativeRunInstallsItsProfileAndSendsNoModel(t *testing.T) {
	_, installed := fakeProfileInstall(t)
	profiles := permissionCAO(t, map[string]cao.ProfilePermissions{"km-claude": {PermissionMode: "default"}}, nil)
	f := &fallbackCAO{profileURL: profiles.URL}
	path, args := fallbackFixture(t, f, false)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	content := strings.Replace(string(data), "agents = ['b']", "agents = ['claude']\nnative_agent = 'demo'", 1)
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(loop.Home(), "config.toml"), "[agents]\navailable = ['a', 'b', 'same', 'claude']\n[agents.define]\na = 'pa:a'\nb = 'pb:b'\nsame = 'pa:other'\n")
	config := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", config)
	writeFile(t, filepath.Join(config, "agents", "demo.md"), "---\nname: demo\n---\n")
	out, err := captureStdout(t, func() error { return runCommand(args) })
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	b, err := os.ReadFile(installed)
	if err != nil || !strings.Contains(string(b), "native_agent: \"demo\"") {
		t.Fatalf("%s %v", b, err)
	}
	found := false
	for _, req := range f.fake.requests {
		if req.Agent == "km-claude-native-demo" {
			found = true
			if req.Model != "" {
				t.Fatal(req)
			}
		}
	}
	if !found {
		t.Fatalf("native request not found: %+v", f.fake.requests)
	}
	if !strings.Contains(out, "native agent demo") {
		t.Fatal(out)
	}
}

func TestAnUnloadableHandWrittenNativeProfileIsNotReplaced(t *testing.T) {
	t.Setenv(loop.StateEnv, t.TempDir())
	fakeProfileInstall(t)
	srv := permissionCAO(t, map[string]cao.ProfilePermissions{"km-claude": {}, "km-claude-native-demo": {Description: "mine"}}, map[string]bool{"km-claude-native-demo": false})
	p := loop.DefaultPolicy()
	chair := loop.Chair{Provider: "claude_code", Profile: "km-claude-native-demo", NativeBase: "km-claude", Agent: "claude", NativeAgent: "demo"}
	if err := ensureProfiles(context.Background(), cao.NewClient(srv.URL), &p, map[string][]loop.Chair{"plan": {chair}}); err == nil {
		t.Fatal("accepted hand-written profile")
	}
}

func TestANativeProfileInstallMustActuallyInstallTheRequestedSource(t *testing.T) {
	t.Setenv(loop.StateEnv, t.TempDir())
	fakeProfileInstall(t)
	script := filepath.Join(strings.Split(os.Getenv("PATH"), string(os.PathListSeparator))[0], "cao")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
		t.Fatal(err)
	}
	srv := permissionCAO(t, map[string]cao.ProfilePermissions{"km-claude": {}}, nil)
	p := loop.DefaultPolicy()
	chair := loop.Chair{Provider: "claude_code", Profile: "km-claude-native-demo", NativeBase: "km-claude", Agent: "claude", NativeAgent: "demo"}
	err := ensureProfiles(context.Background(), cao.NewClient(srv.URL), &p, map[string][]loop.Chair{"plan": {chair}})
	if err == nil {
		t.Fatal("accepted a successful installer that installed nothing")
	}
}

func TestProfileGuardAcceptsAnUnchangedGeneratedNativeProfile(t *testing.T) {
	home := t.TempDir()
	source, err := loop.NativeProfileSource("claude_code", "km-claude-native-demo", "demo", loop.Permissions{})
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(home, "agent-store", "km-claude-native-demo.md"), source)
	if err := guardProfile(home, "km-claude-native-demo", source); err != nil {
		t.Fatal(err)
	}
	if !kmAuthoredDescription("kapellmeister chair playing native agent demo") {
		t.Fatal("native description rejected")
	}
}

func TestANativeProfileWhosePermissionsDifferFromItsBaseIsReinstalled(t *testing.T) {
	t.Setenv(loop.StateEnv, t.TempDir())
	_, installed := fakeProfileInstall(t)
	srv := permissionCAO(t, map[string]cao.ProfilePermissions{"km-claude": {PermissionMode: "default"}, "km-claude-native-demo": {Description: "kapellmeister chair playing native agent demo", PermissionMode: "acceptEdits"}}, map[string]bool{"km-claude-native-demo": true})
	p := loop.DefaultPolicy()
	chair := loop.Chair{Provider: "claude_code", Profile: "km-claude-native-demo", NativeBase: "km-claude", Agent: "claude", NativeAgent: "demo"}
	if err := ensureProfiles(context.Background(), cao.NewClient(srv.URL), &p, map[string][]loop.Chair{"plan": {chair}}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(installed)
	if err != nil || !strings.Contains(string(data), "permissionMode: default") {
		t.Fatalf("%s %v", data, err)
	}
}

func TestSeatFallbackSkipsOtherProvidersForANativeChair(t *testing.T) {
	fakeProfileInstall(t)
	profiles := permissionCAO(t, map[string]cao.ProfilePermissions{"km-claude": {}, "km-claude2": {}}, nil)
	f := &fallbackCAO{profileURL: profiles.URL, blocked: map[string]bool{"claude_code": true}, pane: "requires confirmation"}
	path, args := fallbackFixture(t, f, false)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	content := strings.Replace(string(data), "agents = ['b']", "agents = ['claude', 'codex', 'claude2']\nnative_agent = 'demo'", 1)
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(loop.Home(), "config.toml"), "[agents]\navailable = ['a', 'b', 'same', 'claude', 'codex', 'claude2']\n[agents.define]\na = 'pa:a'\nb = 'pb:b'\nsame = 'pa:other'\nclaude2 = 'claude_code:km-claude2'\n")
	config := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", config)
	writeFile(t, filepath.Join(config, "agents", "demo.md"), "---\nname: demo\n---\n")
	out, err := captureStdout(t, func() error { return runCommand(args) })
	if err == nil || !strings.Contains(err.Error(), "codex cannot play native agent demo (only claude_code can)") {
		t.Fatalf("%v\n%s", err, out)
	}
}

func TestNativeSummaryNamesAddedAndRemovedAgentTreeFiles(t *testing.T) {
	dir := t.TempDir()
	old := filepath.Join(dir, ".claude/agents/old.md")
	writeFile(t, old, "old")
	members := []ensemble.Member{{Chair: loop.Chair{NativeAgent: "demo"}}}
	before := nativeFileStates(dir, members)
	os.Remove(old)
	writeFile(t, filepath.Join(dir, ".claude/agents/sub/shadow.md"), "---\nname: demo\n---\n")
	var sum ensembleSummary
	recordNative(&sum, dir, "band", members, before)
	got := strings.Join(sum.NativeFilesChanged, ",")
	if got != ".claude/agents/old.md,.claude/agents/sub/shadow.md" {
		t.Fatal(got)
	}
}

func TestDetachedFailureRecordsNativeChairs(t *testing.T) {
	dir := t.TempDir()
	wt := t.TempDir()
	state := runState{Started: time.Now(), Seating: &statusSeating{Sections: []statusSection{{Chairs: []statusChair{{Address: "dev.one@band", Agent: "claude", Provider: "claude_code", NativeAgent: "demo", ModelSource: "agent", Definition: "user"}}}}}}
	recordDetachedFailure(context.Background(), dir, detachedLaunchRecord{RunID: "test", Worktree: wt}, state, errors.New("failed"))
	data, err := os.ReadFile(filepath.Join(dir, "result.json"))
	if err != nil {
		t.Fatal(err)
	}
	var result ensembleSummary
	if err = json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	if len(result.NativeChairs) != 1 || result.NativeChairs[0].Definition != "user" {
		t.Fatalf("%s", data)
	}
}

func TestNativeProfilesWithDifferentAgentsCannotShareAName(t *testing.T) {
	p := loop.DefaultPolicy()
	first := loop.Chair{Provider: "claude_code", Profile: "shared", NativeBase: "km-claude", NativeAgent: "one"}
	second := first
	second.NativeAgent = "two"
	err := ensureProfiles(context.Background(), nil, &p, map[string][]loop.Chair{"plan": {first, second}})
	if err == nil || !strings.Contains(err.Error(), "native agents") {
		t.Fatal(err)
	}
}
func TestMissingNativeBaseProfileSuggestsInstallingItsAgent(t *testing.T) {
	srv := permissionCAO(t, map[string]cao.ProfilePermissions{}, nil)
	p := loop.DefaultPolicy()
	chair := loop.Chair{Provider: "claude_code", Profile: "km-claude-native-demo", NativeBase: "km-claude", NativeAgent: "demo"}
	err := ensureProfiles(context.Background(), cao.NewClient(srv.URL), &p, map[string][]loop.Chair{"plan": {chair}})
	if err == nil || !strings.Contains(err.Error(), "km profiles install claude") {
		t.Fatal(err)
	}
}

func TestNativeSnapshotsNeverBlockOnNamedPipes(t *testing.T) {
	for _, path := range []string{".claude", ".claude/agents", ".claude/agents/pipe"} {
		t.Run(path, func(t *testing.T) {
			dir := t.TempDir()
			full := filepath.Join(dir, path)
			if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
				t.Fatal(err)
			}
			if err := syscall.Mkfifo(full, 0600); err != nil {
				t.Fatal(err)
			}
			done := make(chan map[string]string, 1)
			go func() { done <- nativeFileStates(dir, []ensemble.Member{{Chair: loop.Chair{NativeAgent: "demo"}}}) }()
			select {
			case states := <-done:
				if _, ok := states[path]; !ok {
					t.Fatalf("missing %s in %v", path, states)
				}
			case <-time.After(300 * time.Millisecond):
				t.Fatal("snapshot blocked on FIFO")
			}
		})
	}
}

func TestPreflightDefinitionErrorDeletesOtherBlockedSessions(t *testing.T) {
	f := &fallbackCAO{blocked: map[string]bool{"pa": true}, pane: paneBudget}
	srv := f.serve(t)
	policy := loop.DefaultPolicy()
	settings := loop.Settings{PreflightTimeout: time.Second}
	chairs := loop.Chairs{Planner: loop.Chair{Provider: "pa", Profile: "blocked"}, Implementer: loop.Chair{Provider: "claude_code", Profile: "native", NativeAgent: "demo"}}
	_, _, _, err := preflightSeats(context.Background(), cao.NewClient(srv.URL), t.TempDir(), settings, ensemble.Spec{}, nil, chairs, &policy, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "definition") {
		t.Fatalf("%v", err)
	}
	if strings.Contains(err.Error(), "tmux attach") {
		t.Errorf("refers to a deleted preflight session: %v", err)
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	blocked := ""
	for i, launch := range f.launches {
		if launch.Provider == "pa" {
			blocked = fmt.Sprintf("seat-%d", i)
		}
	}
	if blocked == "" || !slices.Contains(f.deleted, blocked) {
		t.Fatalf("blocked session %s not deleted; launches=%v deleted=%v", blocked, f.launches, f.deleted)
	}
}

func TestNativeSummaryPrintsFilesPresentBeforeTheSnapshotButAbsentFromBase(t *testing.T) {
	dir := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"-c", "user.name=T", "-c", "user.email=t@x", "commit", "--allow-empty", "-qm", "base"}} {
		c := exec.Command("git", args...)
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("%s: %v", out, err)
		}
	}
	out, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	path := ".claude/agents/sub/shadow.md"
	writeFile(t, filepath.Join(dir, path), "---\nname: demo\n---\n")
	members := []ensemble.Member{{Section: "dev", Name: "one", Chair: loop.Chair{NativeAgent: "demo", Manifest: &loop.NativeManifest{}, DefinitionBase: strings.TrimSpace(string(out))}}}
	before := nativeFileStates(dir, members)
	var sum ensembleSummary
	recordNative(&sum, dir, "band", members, before)
	if len(sum.NativeFilesChanged) != 1 || sum.NativeFilesChanged[0] != path {
		t.Fatalf("%+v", sum.NativeFilesChanged)
	}
	var output bytes.Buffer
	_ = finishRun(&output, sum)
	if !strings.Contains(output.String(), "changed   "+path+":") || !strings.Contains(output.String(), ".claude/agents/") {
		t.Fatal(output.String())
	}
}

func TestDetachedRunFailureRecordsNativeChairWithoutASeatChange(t *testing.T) {
	fakeProfileInstall(t)
	profiles := permissionCAO(t, map[string]cao.ProfilePermissions{"km-claude": {}}, nil)
	f := &fallbackCAO{profileURL: profiles.URL, failPodium: true}
	path, args := fallbackFixture(t, f, false)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, strings.Replace(string(data), "part = 'classifier'\nagents = ['a', 'b']", "part = 'classifier'\nagents = ['claude']\nnative_agent = 'demo'", 1))
	writeFile(t, filepath.Join(loop.Home(), "config.toml"), "[agents]\navailable = ['a', 'b', 'claude']\n[agents.define]\na = 'pa:a'\nb = 'pb:b'\n")
	config := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", config)
	writeFile(t, filepath.Join(config, "agents/demo.md"), "---\nname: demo\n---\n")
	id := "20261125-174834-abcdef"
	launch := filepath.Join(t.TempDir(), "launch.json")
	if err := writeJSON(launch, detachedLaunchRecord{RunID: id, Worktree: args[3], Task: "probe"}); err != nil {
		t.Fatal(err)
	}
	args = append(args, "-detached-launch", launch)
	_, err = captureStdout(t, func() error { return runAt(context.Background(), args, os.Stdout, io.Discard, nil) })
	if err == nil || !strings.Contains(err.Error(), "podium refused") {
		t.Fatalf("%v", err)
	}
	result := fallbackRecord(t, filepath.Join(loop.StateDir(), "runs", id), "result.json")
	native, ok := result["native_chairs"].([]any)
	if !ok || len(native) != 1 {
		t.Fatalf("%v", result)
	}
	chair := native[0].(map[string]any)
	if chair["native_agent"] != "demo" || chair["definition"] != "user" {
		t.Fatalf("%v", chair)
	}
	if changes, ok := result["seats_changed"].([]any); ok && len(changes) > 0 {
		t.Fatalf("unexpected substitution %v", changes)
	}
}

func TestDefinitionErrorReportsBlockedReason(t *testing.T) {
	err := preflightFailure([]loop.PreflightResult{{DefinitionError: "definition failed"}, {Chair: loop.Chair{Provider: "pa", Profile: "blocked"}, Error: "check failed"}}, time.Second)
	if err == nil || !strings.Contains(err.Error(), "check failed") {
		t.Fatalf("%v", err)
	}
}

func TestAStandingRecordWithoutAManifestIsRefusedByReseatAndPlay(t *testing.T) {
	s := playStanding(t)
	s.Chairs[0].Binding.NativeAgent = "demo"
	s.Chairs[0].Binding.Definition = "repository"
	s.Chairs[0].Binding.DefinitionBase = "base"
	savePlayStanding(t, s)
	var count atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { count.Add(1) }))
	defer srv.Close()
	for _, call := range []func() error{func() error { return reseatCommand([]string{s.Session, s.Chairs[0].Address, "-cao", srv.URL}) }, func() error { return playCommand([]string{s.Session, "-cao", srv.URL}) }} {
		if err := call(); err == nil || !strings.Contains(err.Error(), "rehearse again") || !strings.Contains(err.Error(), "no pinned agent definitions") {
			t.Fatalf("%v", err)
		}
		if count.Load() != 0 {
			t.Fatalf("received %d requests", count.Load())
		}
	}
}

func TestADefinitionErrorInACandidateRoundDeletesBlockedSessions(t *testing.T) {
	fakeProfileInstall(t)
	profiles := permissionCAO(t, map[string]cao.ProfilePermissions{"km-claude2": {}}, nil)
	f := &fallbackCAO{profileURL: profiles.URL, blocked: map[string]bool{"pa": true}, pane: paneBudget}
	srv := f.serve(t)
	agents, err := loop.BuiltInAgents().With(map[string]string{"claude2": "claude_code:km-claude2"})
	if err != nil {
		t.Fatal(err)
	}
	spec, err := ensemble.Check("band", "band.toml", "[sections.dev.chairs.one]\npart = 'planner'\nagents = ['claude', 'claude2']\nnative_agent = 'demo'\n", agents)
	if err != nil {
		t.Fatal(err)
	}
	members := []ensemble.Member{{Section: "dev", Name: "one", Part: "planner", Chair: loop.Chair{Provider: "pa", Profile: "blocked"}}}
	settings := loop.Settings{Agents: []string{"claude", "claude2"}, Defined: agents, SeatFallback: true, PreflightTimeout: time.Second}
	policy := loop.DefaultPolicy()
	_, _, _, err = preflightSeats(context.Background(), cao.NewClient(srv.URL), t.TempDir(), settings, spec, members, loop.Chairs{Planner: members[0].Chair}, &policy, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "definition") || strings.Contains(err.Error(), "tmux attach") {
		t.Fatalf("%v", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !slices.Contains(f.deleted, "seat-0") {
		t.Fatalf("deleted %v, launches %v", f.deleted, f.launches)
	}
}

func TestReseatingANativeChairRefusesAnUnsafeProfileBeforeInstalling(t *testing.T) {
	var requests atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1) }))
	defer srv.Close()
	m := ensemble.Member{Chair: loop.Chair{NativeAgent: "demo", Definition: "repository", DefinitionBase: "base", Manifest: &loop.NativeManifest{}}}
	_, err := prepareMember(context.Background(), cao.NewClient(srv.URL), loop.DefaultPolicy(), loop.BuiltInAgents(), "claude_code:x/../y", m)
	if err == nil || !strings.Contains(err.Error(), "[A-Za-z0-9_-]") || requests.Load() != 0 {
		t.Fatalf("error %v, requests %d", err, requests.Load())
	}
}

func TestAStandingRecordWhoseManifestHasNoSizesIsRefusedByReseatAndPlay(t *testing.T) {
	s := playStanding(t)
	s.Chairs[0].Binding.NativeAgent = "demo"
	s.Chairs[0].Binding.Definition = "repository"
	s.Chairs[0].Binding.DefinitionBase = "base"
	s.Chairs[0].Binding.Manifest = &loop.NativeManifest{Entries: []loop.NativeManifestEntry{{Path: []byte("demo.md"), SHA256: "hash"}}}
	savePlayStanding(t, s)
	var count atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { count.Add(1) }))
	defer srv.Close()
	for _, call := range []func() error{func() error { return reseatCommand([]string{s.Session, s.Chairs[0].Address, "-cao", srv.URL}) }, func() error { return playCommand([]string{s.Session, "-cao", srv.URL}) }} {
		if err := call(); err == nil || !strings.Contains(err.Error(), "rehearse again") || !strings.Contains(err.Error(), "no pinned agent definitions") {
			t.Fatalf("%v", err)
		}
		if count.Load() != 0 {
			t.Fatalf("received %d requests", count.Load())
		}
	}
}
