package loop

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jystringfellow/kapellmeister/internal/cao"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode"
)

func TestANativeProfileNamesItsAgentQuotedWithTheUsersPermissionsAndNoModelOrBody(t *testing.T) {
	got, err := NativeProfileSource("claude_code", "km-claude-native-null", "null", Permissions{PermissionMode: "acceptEdits", AllowedTools: []string{"Read", "Write"}})
	if err != nil {
		t.Fatal(err)
	}
	want := "---\nname: km-claude-native-null\ndescription: kapellmeister chair playing native agent null\nprovider: claude_code\nrole: developer\npermissionMode: acceptEdits\nallowedTools: [\"Read\",\"Write\"]\nnative_agent: \"null\"\n---\n"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if strings.Contains(got, "model:") || strings.Contains(got, "claudeConfig:") {
		t.Fatal(got)
	}
}

func TestANativeChairGetsNoModelTierOrEffortVariant(t *testing.T) {
	p := DefaultPolicy()
	chair := Chair{Provider: "claude_code", Profile: "km-claude-native-demo", NativeAgent: "demo"}
	got, assignment := p.Chair("implement", chair, "large", 2)
	if got != chair || assignment != (Assignment{}) {
		t.Fatalf("got %+v %+v", got, assignment)
	}
	got, effort := p.At(chair, "large")
	if got != chair || effort != "" {
		t.Fatalf("got %+v %q", got, effort)
	}
	if variants := p.EffortVariants(map[string][]Chair{"implement": {chair}}); len(variants) != 0 {
		t.Fatalf("got %+v", variants)
	}
}

func TestAnEditedOrMissingDefinitionStopsEveryNewTerminalBeforeAnyCAOCall(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".claude", "agents", "demo.md")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	original := []byte("---\nname: demo\n---\n")
	gitTestNative(t, dir, "init", "-q")
	if err := os.WriteFile(path, original, 0644); err != nil {
		t.Fatal(err)
	}
	gitTestNative(t, dir, "add", ".")
	gitTestNative(t, dir, "commit", "-qm", "base")
	chair, err := ResolveNativeDefinition(dir, Chair{NativeAgent: "demo"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := chair.CheckDefinition(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("changed"), 0644); err != nil {
		t.Fatal(err)
	}
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"terminal_id":"test","status":"completed"}`))
	}))
	defer server.Close()
	stepper := CAOStepper{Client: cao.NewClient(server.URL)}
	if _, err := stepper.Run(context.Background(), Step{Chair: chair, Dir: dir}); !errors.Is(err, ErrNativeDefinition) {
		t.Fatalf("Run: %v", err)
	}
	if _, err := stepper.Seat(context.Background(), "test", chair, dir); !errors.Is(err, ErrNativeDefinition) {
		t.Fatalf("Seat: %v", err)
	}
	if requests != 0 {
		t.Fatalf("fresh launches sent %d requests", requests)
	}
	_, _ = stepper.Run(context.Background(), Step{Chair: chair, Dir: dir, ReuseTerminal: "test"})
	if requests == 0 {
		t.Fatal("reused terminal made no request")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := chair.CheckDefinition(dir); !errors.Is(err, ErrNativeDefinition) {
		t.Fatalf("missing: %v", err)
	}
}

func TestAWorktreeFileThatAppearsOverAUsersDefinitionStopsTheLaunch(t *testing.T) {
	dir := t.TempDir()
	gitTestNative(t, dir, "init", "-q")
	gitTestNative(t, dir, "commit", "--allow-empty", "-qm", "base")
	base := strings.TrimSpace(gitTestNative(t, dir, "rev-parse", "HEAD"))
	chair := Chair{NativeAgent: "demo", Definition: "user", DefinitionBase: base, Manifest: &NativeManifest{}}
	if err := chair.CheckDefinition(dir); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, ".claude", "agents", "demo.md")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("override"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := chair.CheckDefinition(dir); !errors.Is(err, ErrNativeDefinition) || !strings.Contains(err.Error(), "not in the base commit") || strings.Contains(err.Error(), "differs from the base commit") {
		t.Fatalf("got %v", err)
	}
}

func TestAnUnboundNativeChairIsNeverLaunched(t *testing.T) {
	if err := (Chair{NativeAgent: "demo"}).CheckDefinition(t.TempDir()); !errors.Is(err, ErrNativeDefinition) {
		t.Fatalf("got %v", err)
	}
}

func TestANativeStepRecordShowsTheModelAsSetByTheAgent(t *testing.T) {
	var records bytes.Buffer
	e := Engine{Records: &records}
	honored := true
	e.record(Record{Chair: &Chair{NativeAgent: "demo"}, Model: "reported", ModelHonored: &honored})
	var rec map[string]any
	if err := json.Unmarshal(records.Bytes(), &rec); err != nil {
		t.Fatal(err)
	}
	if rec["model_source"] != "agent" || rec["model"] != nil || rec["model_honored"] != nil {
		t.Fatalf("got %v", rec)
	}
}

func TestADefinitionFailureInPreflightIsNotRetried(t *testing.T) {
	dir := t.TempDir()
	attempts, unseats := 0, 0
	host := &preflightHost{fakeStepper: &fakeStepper{t: t, dir: dir}, seat: func(context.Context, string, Chair, string) (Seat, error) {
		attempts++
		return Seat{}, ErrNativeDefinition
	}, unseat: func(context.Context, string) error { unseats++; return nil }}
	results, err := Preflight(context.Background(), host, dir, []Chair{{Provider: "claude_code", NativeAgent: "demo"}}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if unseats != 0 {
		t.Fatalf("unseat called %d times", unseats)
	}
	if attempts != 1 || len(results) != 1 || results[0].DefinitionError == "" {
		t.Fatalf("attempts %d results %+v", attempts, results)
	}
}

func TestARepositoryDefinitionMustBeARegularFileAtTheBaseCommit(t *testing.T) {
	dir := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		c := exec.Command("git", args...)
		c.Dir = dir
		c.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.invalid", "GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.invalid")
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s: %v", args, out, err)
		}
	}
	git("init", "-q")
	git("commit", "--allow-empty", "-qm", "base")
	chair := Chair{NativeAgent: "demo"}
	if _, err := ResolveNativeDefinition(dir, chair, true); err == nil || !strings.Contains(err.Error(), ".claude/agents/demo.md") {
		t.Fatal(err)
	}
	path := filepath.Join(dir, ".claude", "agents", "demo.md")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("elsewhere", path); err != nil {
		t.Fatal(err)
	}
	git("add", ".claude/agents/demo.md")
	git("commit", "-qm", "symlink")
	if _, err := ResolveNativeDefinition(dir, chair, true); err == nil {
		t.Fatal("accepted symlink")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("---\nname: demo\n---\n"), 0644); err != nil {
		t.Fatal(err)
	}
	git("add", ".claude/agents/demo.md")
	git("commit", "-qm", "agent")
	resolved, err := ResolveNativeDefinition(dir, chair, true)
	if err != nil || resolved.Definition != "repository" || resolved.CheckDefinition(dir) != nil {
		t.Fatalf("%+v %v", resolved, err)
	}
}

func TestAUserEnsembleMayTakeItsDefinitionFromTheUsersAgentDirectory(t *testing.T) {
	dir := t.TempDir()
	c := exec.Command("git", "init", "-q", dir)
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("%s: %v", out, err)
	}
	c = exec.Command("git", "-C", dir, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "--allow-empty", "-qm", "base")
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("%s: %v", out, err)
	}
	config := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", config)
	if err := os.MkdirAll(filepath.Join(config, "agents"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(config, "agents", "demo.md"), []byte("---\nname: demo\n---\n"), 0644); err != nil {
		t.Fatal(err)
	}
	got, err := ResolveNativeDefinition(dir, Chair{NativeAgent: "demo"}, false)
	if err != nil || got.Definition != "user" {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestOnlyANativeChairsPromptCarriesTheChairInstructionsAfterThePreamble(t *testing.T) {
	dir := t.TempDir()
	f := &fakeStepper{t: t, dir: dir}
	e := Engine{Stepper: f, Config: Config{Worktree: dir, StepTimeout: time.Second}}
	prompt := preamble("planner") + "<task>task</task>\n"
	for i, chair := range []Chair{{Provider: "claude_code", NativeAgent: "demo"}, {Provider: "claude_code"}} {
		_, err := e.step(context.Background(), Step{Name: "plan", Chair: chair, Prompt: prompt, ReplyPath: fmt.Sprintf("reply-%d", i)})
		if err != nil {
			t.Fatal(err)
		}
	}
	if f.steps[0].Prompt != preamble("planner")+chairInstructions+"<task>task</task>\n" || f.steps[1].Prompt != prompt {
		t.Fatalf("%q / %q", f.steps[0].Prompt, f.steps[1].Prompt)
	}
}

func TestANativeRunStepNeverSendsAModel(t *testing.T) {
	dir := t.TempDir()
	var request map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/terminals/run-step" {
			_ = json.NewDecoder(r.Body).Decode(&request)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"terminal_id":"t","status":"completed"}`))
	}))
	defer server.Close()
	_, _ = (CAOStepper{Client: cao.NewClient(server.URL)}).Run(context.Background(), Step{Chair: Chair{NativeAgent: "demo", Model: "wrong"}, Dir: dir, ReuseTerminal: "t"})
	if request == nil || request["model"] != nil {
		t.Fatalf("request: %v", request)
	}
}

func TestNativePreflightDoesNotReportCAOsModelOrModelHonored(t *testing.T) {
	dir := t.TempDir()
	if err := PrepareRunDir(dir, "preflight"); err != nil {
		t.Fatal(err)
	}
	honored := false
	host := &preflightHost{fakeStepper: &fakeStepper{t: t, dir: dir, errFor: func(Step) error { return errors.New("stop") }}, seat: func(context.Context, string, Chair, string) (Seat, error) {
		return Seat{TerminalID: "t", Session: "s", Model: "reported", Honored: &honored}, nil
	}}
	res, _ := preflightChair(context.Background(), host, dir, 0, Chair{Provider: "claude_code", NativeAgent: "demo"}, time.Second, "test", &atomic.Int64{}, make(chan struct{}, 1))
	if res.Model != "" || res.Honored != nil {
		t.Fatalf("%+v", res)
	}
}

func TestAUserEnsembleUsesTheDefaultClaudeAgentDirectory(t *testing.T) {
	dir := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		c := exec.Command("git", args...)
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("%s: %v", out, err)
		}
	}
	git("init", "-q")
	git("-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "--allow-empty", "-qm", "base")
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	path := filepath.Join(home, ".claude", "agents", "demo.md")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("---\nname: demo\n---\n"), 0644); err != nil {
		t.Fatal(err)
	}
	got, err := ResolveNativeDefinition(dir, Chair{NativeAgent: "demo"}, false)
	if err != nil || got.Definition != "user" {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestNativeDefinitionsAreResolvedAgainstTheSelectedBaseCommit(t *testing.T) {
	dir := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		c := exec.Command("git", args...)
		c.Dir = dir
		c.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.invalid", "GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.invalid")
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("%s: %v", out, err)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q")
	path := filepath.Join(dir, ".claude", "agents", "demo.md")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("---\nname: demo\n---\nfirst"), 0644); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-qm", "first")
	base := git("rev-parse", "HEAD")
	if err := os.WriteFile(path, []byte("---\nname: demo\n---\nsecond"), 0644); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-qm", "second")
	chair, err := ResolveNativeDefinitionAt(dir, base, Chair{NativeAgent: "demo"}, true)
	if err != nil || !errors.Is(chair.CheckDefinition(dir), ErrNativeDefinition) {
		t.Fatalf("%+v %v", chair, err)
	}
}

func TestNativeBindingRejectsAmbiguousAndMismatchedNames(t *testing.T) {
	for _, tc := range []struct{ name, definition, other string }{
		{"mismatch", "---\nname: renamed\n---\n", ""},
		{"ambiguous", "---\nname: demo\n---\n", "---\nname: demo\n---\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			run := func(args ...string) {
				t.Helper()
				c := exec.Command("git", args...)
				c.Dir = dir
				c.Env = append(os.Environ(), "GIT_AUTHOR_NAME=T", "GIT_AUTHOR_EMAIL=t@x", "GIT_COMMITTER_NAME=T", "GIT_COMMITTER_EMAIL=t@x")
				if out, err := c.CombinedOutput(); err != nil {
					t.Fatalf("%s: %v", out, err)
				}
			}
			run("init", "-q")
			os.MkdirAll(filepath.Join(dir, ".claude/agents/sub"), 0755)
			os.WriteFile(filepath.Join(dir, ".claude/agents/demo.md"), []byte(tc.definition), 0644)
			if tc.other != "" {
				os.WriteFile(filepath.Join(dir, ".claude/agents/sub/x.md"), []byte(tc.other), 0644)
			}
			run("add", ".")
			run("commit", "-qm", "base")
			if _, err := ResolveNativeDefinition(dir, Chair{NativeAgent: "demo"}, true); err == nil {
				t.Fatal("accepted invalid definition")
			}
		})
	}
}

func TestAnyNewAgentTreeFileStopsNativeLaunch(t *testing.T) {
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		c := exec.Command("git", args...)
		c.Dir = dir
		c.Env = append(os.Environ(), "GIT_AUTHOR_NAME=T", "GIT_AUTHOR_EMAIL=t@x", "GIT_COMMITTER_NAME=T", "GIT_COMMITTER_EMAIL=t@x")
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("%s: %v", out, err)
		}
	}
	run("init", "-q")
	os.MkdirAll(filepath.Join(dir, ".claude/agents/sub"), 0755)
	os.WriteFile(filepath.Join(dir, ".claude/agents/demo.md"), []byte("---\nname: demo\n---\n"), 0644)
	run("add", ".")
	run("commit", "-qm", "base")
	chair, err := ResolveNativeDefinition(dir, Chair{NativeAgent: "demo"}, true)
	if err != nil {
		t.Fatal(err)
	}
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests++ }))
	defer server.Close()
	stepper := CAOStepper{Client: cao.NewClient(server.URL)}
	for _, content := range []string{"---\nname: demo\n---\n", "unrelated"} {
		path := filepath.Join(dir, ".claude/agents/sub/x.md")
		os.WriteFile(path, []byte(content), 0644)
		err = chair.CheckDefinition(dir)
		if !errors.Is(err, ErrNativeDefinition) || !strings.Contains(err.Error(), "sub/x.md") {
			t.Fatalf("got %v", err)
		}
		if _, err := stepper.Run(context.Background(), Step{Chair: chair, Dir: dir}); !errors.Is(err, ErrNativeDefinition) {
			t.Fatalf("Run: %v", err)
		}
		if _, err := stepper.Seat(context.Background(), "test", chair, dir); !errors.Is(err, ErrNativeDefinition) {
			t.Fatalf("Seat: %v", err)
		}
		if requests != 0 {
			t.Fatalf("sent %d requests", requests)
		}
		os.Remove(path)
	}
}

func gitTestNative(t *testing.T, dir string, args ...string) string {
	t.Helper()
	c := exec.Command("git", args...)
	c.Dir = dir
	c.Env = append(os.Environ(), "GIT_AUTHOR_NAME=T", "GIT_AUTHOR_EMAIL=t@x", "GIT_COMMITTER_NAME=T", "GIT_COMMITTER_EMAIL=t@x")
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("git: %s: %v", out, err)
	}
	return string(out)
}

func TestAUserDefinitionShadowedByAProjectAgentIsRefused(t *testing.T) {
	dir := t.TempDir()
	gitTestNative(t, dir, "init", "-q")
	os.MkdirAll(filepath.Join(dir, ".claude/agents"), 0755)
	os.WriteFile(filepath.Join(dir, ".claude/agents/other.md"), []byte("---\nname: demo\n---\n"), 0644)
	gitTestNative(t, dir, "add", ".")
	gitTestNative(t, dir, "commit", "-qm", "base")
	config := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", config)
	os.MkdirAll(filepath.Join(config, "agents"), 0755)
	os.WriteFile(filepath.Join(config, "agents/demo.md"), []byte("---\nname: demo\n---\n"), 0644)
	_, err := ResolveNativeDefinition(dir, Chair{NativeAgent: "demo"}, false)
	if err == nil || !strings.Contains(err.Error(), `other.md declares name "demo"`) {
		t.Fatalf("got %v", err)
	}
}

func TestNativeTreeComparisonReportsIgnoredFilesAndLimitsTheList(t *testing.T) {
	dir := t.TempDir()
	gitTestNative(t, dir, "init", "-q")
	os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(".claude/agents/sub/\n"), 0644)
	os.MkdirAll(filepath.Join(dir, ".claude/agents"), 0755)
	os.WriteFile(filepath.Join(dir, ".claude/agents/demo.md"), []byte("---\nname: demo\n---\n"), 0644)
	gitTestNative(t, dir, "add", ".")
	gitTestNative(t, dir, "commit", "-qm", "base")
	chair, err := ResolveNativeDefinition(dir, Chair{NativeAgent: "demo"}, true)
	if err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(filepath.Join(dir, ".claude/agents/sub"), 0755)
	for i := 0; i < 7; i++ {
		os.WriteFile(filepath.Join(dir, fmt.Sprintf(".claude/agents/sub/%d", i)), []byte("x"), 0644)
	}
	err = chair.CheckDefinition(dir)
	if err == nil || !strings.Contains(err.Error(), "sub/0") || !strings.Contains(err.Error(), "and 2 more") || strings.Contains(err.Error(), "sub/5") {
		t.Fatalf("%v", err)
	}
}
func TestUserDefinitionCreatesAnEmptyProjectDirectory(t *testing.T) {
	dir := t.TempDir()
	gitTestNative(t, dir, "init", "-q")
	gitTestNative(t, dir, "commit", "--allow-empty", "-qm", "base")
	config := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", config)
	os.MkdirAll(filepath.Join(config, "agents"), 0755)
	os.WriteFile(filepath.Join(config, "agents/demo.md"), []byte("---\nname: demo\n---\n"), 0644)
	chair, err := ResolveNativeDefinition(dir, Chair{NativeAgent: "demo"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if err = chair.CheckDefinition(dir); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(dir, ".claude/agents"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("%v %v", entries, err)
	}
	if status := gitTestNative(t, dir, "status", "--porcelain"); status != "" {
		t.Fatal(status)
	}
}

func TestNativeDefinitionReportsCRLineEndings(t *testing.T) {
	dir := t.TempDir()
	gitTestNative(t, dir, "init", "-q")
	path := filepath.Join(dir, ".claude/agents/demo.md")
	os.MkdirAll(filepath.Dir(path), 0755)
	os.WriteFile(path, []byte("---\r\nname: demo\r\n---\r\n"), 0644)
	gitTestNative(t, dir, "add", ".")
	gitTestNative(t, dir, "commit", "-qm", "base")
	_, err := ResolveNativeDefinition(dir, Chair{NativeAgent: "demo"}, true)
	if err == nil || !strings.Contains(err.Error(), "line endings") {
		t.Fatal(err)
	}
	os.WriteFile(path, []byte("---\nname: demo\n---\n"), 0644)
	gitTestNative(t, dir, "add", ".")
	gitTestNative(t, dir, "commit", "-qm", "lf")
	chair, err := ResolveNativeDefinition(dir, Chair{NativeAgent: "demo"}, true)
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(path, []byte("---\r\nname: demo\r\n---\r\n"), 0644)
	err = chair.CheckDefinition(dir)
	if err == nil || !strings.Contains(err.Error(), "line endings") {
		t.Fatal(err)
	}
}

func TestNativeBindingRejectsMismatchedUserNameAndProjectSymlinks(t *testing.T) {
	dir := t.TempDir()
	gitTestNative(t, dir, "init", "-q")
	gitTestNative(t, dir, "commit", "--allow-empty", "-qm", "base")
	config := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", config)
	path := filepath.Join(config, "agents/demo.md")
	os.MkdirAll(filepath.Dir(path), 0755)
	os.WriteFile(path, []byte("---\nname: renamed\n---\n"), 0644)
	_, err := ResolveNativeDefinition(dir, Chair{NativeAgent: "demo"}, false)
	if err == nil || !strings.Contains(err.Error(), "renamed") || !strings.Contains(err.Error(), path) {
		t.Fatal(err)
	}
	os.WriteFile(path, []byte("---\nname: demo\n---\n"), 0644)
	os.MkdirAll(filepath.Join(dir, ".claude/agents"), 0755)
	os.Symlink("elsewhere", filepath.Join(dir, ".claude/agents/link"))
	gitTestNative(t, dir, "add", ".")
	gitTestNative(t, dir, "commit", "-qm", "link")
	_, err = ResolveNativeDefinition(dir, Chair{NativeAgent: "demo"}, false)
	if err == nil || !strings.Contains(err.Error(), "link") {
		t.Fatal(err)
	}
}

func TestNativeBaseCannotBeReplacedByGitReplacementRefs(t *testing.T) {
	for _, kind := range []string{"commit", "blob"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			gitTestNative(t, dir, "init", "-q")
			path := filepath.Join(dir, ".claude/agents/demo.md")
			os.MkdirAll(filepath.Dir(path), 0755)
			os.WriteFile(path, []byte("---\nname: demo\n---\n"), 0644)
			gitTestNative(t, dir, "add", ".")
			gitTestNative(t, dir, "commit", "-qm", "base")
			chair, err := ResolveNativeDefinition(dir, Chair{NativeAgent: "demo"}, true)
			if err != nil {
				t.Fatal(err)
			}
			if kind == "commit" {
				shadow := filepath.Join(dir, ".claude/agents/sub/x.md")
				os.MkdirAll(filepath.Dir(shadow), 0755)
				os.WriteFile(shadow, []byte("---\nname: demo\n---\n"), 0644)
				gitTestNative(t, dir, "add", ".")
				tree := strings.TrimSpace(gitTestNative(t, dir, "write-tree"))
				commit := strings.TrimSpace(gitTestNative(t, dir, "commit-tree", tree, "-m", "replacement"))
				gitTestNative(t, dir, "reset", "-q")
				gitTestNative(t, dir, "replace", chair.DefinitionBase, commit)
			} else {
				old := strings.TrimSpace(gitTestNative(t, dir, "rev-parse", "HEAD:.claude/agents/demo.md"))
				os.WriteFile(path, []byte("---\nname: demo\n---\nchanged\n"), 0644)
				oid := strings.TrimSpace(gitTestNative(t, dir, "hash-object", "-w", path))
				gitTestNative(t, dir, "replace", old, oid)
			}
			if err := chair.CheckDefinition(dir); !errors.Is(err, ErrNativeDefinition) {
				t.Fatalf("replacement hides change: %v", err)
			}
		})
	}
}

func TestNativeBindingRefusesAWorktreeSubdirectory(t *testing.T) {
	dir := t.TempDir()
	gitTestNative(t, dir, "init", "-q")
	gitTestNative(t, dir, "commit", "--allow-empty", "-qm", "base")
	sub := filepath.Join(dir, "sub")
	os.MkdirAll(sub, 0755)
	config := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", config)
	os.MkdirAll(filepath.Join(config, "agents"), 0755)
	os.WriteFile(filepath.Join(config, "agents/demo.md"), []byte("---\nname: demo\n---\n"), 0644)
	_, err := ResolveNativeDefinition(sub, Chair{NativeAgent: "demo"}, false)
	if err == nil || !strings.Contains(err.Error(), "repository root") {
		t.Fatalf("got %v", err)
	}
}

func TestNativeBindingExplainsNamesAndListsAmbiguousDefinitions(t *testing.T) {
	for _, tc := range []struct {
		name, definition, other string
		want                    []string
		accept                  bool
	}{
		{"mismatch", "---\nname: renamed-agent\n---\n", "", []string{".claude/agents/demo.md", `declares name "renamed-agent", not "demo"`}, false},
		{"missing-front-matter", "name: demo\n", "", []string{".claude/agents/demo.md", "no name"}, false},
		{"missing-name", "---\ndescription: demo\n---\n", "", []string{".claude/agents/demo.md", "no name"}, false},
		{"duplicate-names", "---\nname: demo\nname: other\n---\n", "", []string{".claude/agents/demo.md", "demo", "other"}, false},
		{"quoted-definition", "---\nname: \"demo\" # comment\n---\n", "", nil, true},
		{"ambiguous-tab-comment", "---\nname: demo\n---\n", "---\nname: demo\t# comment\n---\n", []string{"ambiguous", ".claude/agents/demo.md", ".claude/agents/sub/demo.md"}, false},
		{"ambiguous-quoted-crlf", "---\nname: demo\n---\n", "---\r\n  'name': \"demo\" # comment\r\n---\r\n", []string{"ambiguous", ".claude/agents/demo.md", ".claude/agents/sub/demo.md"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			gitTestNative(t, dir, "init", "-q")
			p := filepath.Join(dir, ".claude/agents/demo.md")
			os.MkdirAll(filepath.Dir(p), 0755)
			os.WriteFile(p, []byte(tc.definition), 0644)
			if tc.other != "" {
				p = filepath.Join(dir, ".claude/agents/sub/demo.md")
				os.MkdirAll(filepath.Dir(p), 0755)
				os.WriteFile(p, []byte(tc.other), 0644)
			}
			gitTestNative(t, dir, "add", ".")
			gitTestNative(t, dir, "commit", "-qm", "base")
			_, err := ResolveNativeDefinition(dir, Chair{NativeAgent: "demo"}, true)
			if tc.accept {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil {
				t.Fatal("accepted invalid definition")
			}
			for _, w := range tc.want {
				if !strings.Contains(err.Error(), w) {
					t.Fatalf("missing %q: %v", w, err)
				}
			}
		})
	}
}

func TestNativeChecksKeepTheBasePinAndRejectOtherTreeChanges(t *testing.T) {
	for _, kind := range []string{"commit-shadow", "change-unrelated", "remove-unrelated", "entry-link", "agents-link", "claude-link", "content"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			gitTestNative(t, dir, "init", "-q")
			agents := filepath.Join(dir, ".claude/agents")
			os.MkdirAll(agents, 0755)
			demo := filepath.Join(agents, "demo.md")
			other := filepath.Join(agents, "other.md")
			os.WriteFile(demo, []byte("---\nname: demo\n---\n"), 0644)
			os.WriteFile(other, []byte("---\nname: other\n---\n"), 0644)
			gitTestNative(t, dir, "add", ".")
			gitTestNative(t, dir, "commit", "-qm", "base")
			chair, err := ResolveNativeDefinition(dir, Chair{NativeAgent: "demo"}, true)
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "commit-shadow":
				os.MkdirAll(filepath.Join(agents, "sub"), 0755)
				os.WriteFile(filepath.Join(agents, "sub/x.md"), []byte("---\nname: demo\n---\n"), 0644)
				gitTestNative(t, dir, "add", ".")
				gitTestNative(t, dir, "commit", "-qm", "shadow")
			case "change-unrelated":
				os.WriteFile(other, []byte("changed"), 0644)
			case "remove-unrelated":
				os.Remove(other)
			case "entry-link":
				os.Symlink("demo.md", filepath.Join(agents, "link"))
			case "agents-link":
				os.Rename(agents, filepath.Join(dir, "saved"))
				os.Symlink(filepath.Join(dir, "saved"), agents)
			case "claude-link":
				os.Rename(filepath.Join(dir, ".claude"), filepath.Join(dir, "saved"))
				os.Symlink(filepath.Join(dir, "saved"), filepath.Join(dir, ".claude"))
			case "content":
				os.WriteFile(demo, []byte("---\nname: demo\n---\nchanged\n"), 0644)
			}
			err = chair.CheckDefinition(dir)
			if !errors.Is(err, ErrNativeDefinition) {
				t.Fatalf("%s: %v", kind, err)
			}
			if kind == "content" && strings.Contains(err.Error(), "only in line endings") {
				t.Fatal(err)
			}
			if kind == "commit-shadow" && !strings.Contains(err.Error(), "sub/x.md") {
				t.Fatal(err)
			}
		})
	}
}

func TestNativeBindingReadsUnreplacedCommitAndDefinitionBlob(t *testing.T) {
	for _, kind := range []string{"commit", "blob"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			gitTestNative(t, dir, "init", "-q")
			path := filepath.Join(dir, ".claude/agents/demo.md")
			os.MkdirAll(filepath.Dir(path), 0755)
			os.WriteFile(path, []byte("---\nname: demo\n---\n"), 0644)
			gitTestNative(t, dir, "add", ".")
			gitTestNative(t, dir, "commit", "-qm", "base")
			base := strings.TrimSpace(gitTestNative(t, dir, "rev-parse", "HEAD"))
			if kind == "blob" {
				old := strings.TrimSpace(gitTestNative(t, dir, "rev-parse", "HEAD:.claude/agents/demo.md"))
				os.WriteFile(path, []byte("---\nname: impostor\n---\n"), 0644)
				replacement := strings.TrimSpace(gitTestNative(t, dir, "hash-object", "-w", path))
				gitTestNative(t, dir, "replace", old, replacement)
			} else {
				shadow := filepath.Join(dir, ".claude/agents/shadow.md")
				os.WriteFile(shadow, []byte("---\nname: demo\n---\n"), 0644)
				gitTestNative(t, dir, "add", ".")
				tree := strings.TrimSpace(gitTestNative(t, dir, "write-tree"))
				replacement := strings.TrimSpace(gitTestNative(t, dir, "commit-tree", tree, "-m", "replacement"))
				gitTestNative(t, dir, "replace", base, replacement)
			}
			chair, err := ResolveNativeDefinitionAt(dir, base, Chair{NativeAgent: "demo"}, true)
			if err != nil || chair.DefinitionBase != base {
				t.Fatalf("%+v %v", chair, err)
			}
		})
	}
}

func TestForgingABaseBlobCannotHideAnEditedNativeAgent(t *testing.T) {
	for _, format := range []string{"sha1", "sha256"} {
		t.Run(format, func(t *testing.T) {
			dir := t.TempDir()
			gitTestNative(t, dir, "init", "-q", "--object-format="+format)
			path := ".claude/agents/demo.md"
			full := filepath.Join(dir, path)
			if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(full, []byte("---\nname: demo\n---\n"), 0644); err != nil {
				t.Fatal(err)
			}
			gitTestNative(t, dir, "add", ".")
			gitTestNative(t, dir, "commit", "-qm", "base")
			chair, err := ResolveNativeDefinition(dir, Chair{NativeAgent: "demo"}, true)
			if err != nil {
				t.Fatal(err)
			}
			oid := strings.TrimSpace(gitTestNative(t, dir, "rev-parse", "HEAD:"+path))
			edited := []byte("---\nname: demo\n---\nUse the shadow persona.\n")
			if err := os.WriteFile(full, edited, 0644); err != nil {
				t.Fatal(err)
			}
			object := filepath.Join(dir, ".git/objects", oid[:2], oid[2:])
			var compressed bytes.Buffer
			zw := zlib.NewWriter(&compressed)
			if _, err := fmt.Fprintf(zw, "blob %d%c%s", len(edited), 0, edited); err != nil {
				t.Fatal(err)
			}
			if err := zw.Close(); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(object); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(object, compressed.Bytes(), 0444); err != nil {
				t.Fatal(err)
			}
			if got := gitTestNative(t, dir, "cat-file", "blob", oid); got != string(edited) {
				t.Fatalf("forgery did not reproduce: %q", got)
			}
			diffs, err := NativeTreeDiff(dir, chair.Manifest)
			if err != nil || len(diffs) != 1 || diffs[0].Path != path {
				t.Errorf("forged blob hid changed path: %v, %v", diffs, err)
			}
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				http.Error(w, "unexpected launch", http.StatusBadRequest)
			}))
			defer server.Close()
			stepper := CAOStepper{Client: cao.NewClient(server.URL)}
			if _, err := stepper.Run(context.Background(), Step{Chair: chair, Dir: dir}); !errors.Is(err, ErrNativeDefinition) || !strings.Contains(err.Error(), path) {
				t.Errorf("Run: %v", err)
			}
			if _, err := stepper.Seat(context.Background(), "test", chair, dir); !errors.Is(err, ErrNativeDefinition) || !strings.Contains(err.Error(), path) {
				t.Errorf("Seat: %v", err)
			}
			if requests != 0 {
				t.Errorf("forged blob allowed %d CAO requests", requests)
			}
		})
	}
}

func TestAnAgentFilePresentBeforeBindingIsReportedWithoutInventingWhenItAppeared(t *testing.T) {
	dir := t.TempDir()
	gitTestNative(t, dir, "init", "-q")
	gitTestNative(t, dir, "commit", "--allow-empty", "-qm", "base")
	config := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", config)
	for _, p := range []string{filepath.Join(config, "agents/demo.md"), filepath.Join(dir, ".claude/agents/demo.md")} {
		if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("---\nname: demo\n---\n"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	chair, err := ResolveNativeDefinition(dir, Chair{NativeAgent: "demo"}, false)
	if err != nil {
		t.Fatal(err)
	}
	err = chair.CheckDefinition(dir)
	if !errors.Is(err, ErrNativeDefinition) || !strings.Contains(err.Error(), "not in the base commit") || strings.Contains(err.Error(), "after the run started") {
		t.Fatalf("got %v", err)
	}
}

func TestForgingTheBaseAgentsTreeCannotHideAnEditedOrAddedAgent(t *testing.T) {
	for _, scenario := range []string{"edited", "added-shadow"} {
		t.Run(scenario, func(t *testing.T) {
			dir := t.TempDir()
			gitTestNative(t, dir, "init", "-q")
			path := filepath.Join(dir, ".claude/agents/demo.md")
			if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("---\nname: demo\n---\n"), 0644); err != nil {
				t.Fatal(err)
			}
			gitTestNative(t, dir, "add", ".")
			gitTestNative(t, dir, "commit", "-qm", "base")
			chair, err := ResolveNativeDefinition(dir, Chair{NativeAgent: "demo"}, true)
			if err != nil {
				t.Fatal(err)
			}
			oid := strings.TrimSpace(gitTestNative(t, dir, "rev-parse", "HEAD:.claude/agents"))
			changed := path
			if scenario == "added-shadow" {
				changed = filepath.Join(dir, ".claude/agents/sub/helper.md")
				if err := os.MkdirAll(filepath.Dir(changed), 0755); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(changed, []byte("---\nname: demo\n---\nchanged\n"), 0644); err != nil {
				t.Fatal(err)
			}
			gitTestNative(t, dir, "add", ".")
			tree := strings.TrimSpace(gitTestNative(t, dir, "write-tree"))
			forged := strings.TrimSpace(gitTestNative(t, dir, "rev-parse", tree+":.claude/agents"))
			gitTestNative(t, dir, "reset", "-q")
			object := func(id string) string { return filepath.Join(dir, ".git/objects", id[:2], id[2:]) }
			data, err := os.ReadFile(object(forged))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(object(oid), 0644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(object(oid), data, 0444); err != nil {
				t.Fatal(err)
			}
			listing := gitTestNative(t, dir, "ls-tree", "-r", "HEAD", "--", ".claude/agents")
			if scenario == "added-shadow" && !strings.Contains(listing, "helper.md") {
				t.Fatal(listing)
			}
			if scenario == "edited" && !strings.Contains(listing, strings.TrimSpace(gitTestNative(t, dir, "hash-object", changed))) {
				t.Fatal(listing)
			}
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests++ }))
			defer server.Close()
			stepper := CAOStepper{Client: cao.NewClient(server.URL)}
			wantPath, wantReason := "demo.md", "differs from the base commit"
			if scenario == "added-shadow" {
				wantPath, wantReason = "sub/helper.md", "is not in the base commit"
			}
			check := func(err error) bool {
				return errors.Is(err, ErrNativeDefinition) && strings.Contains(err.Error(), wantPath) && strings.Contains(err.Error(), wantReason)
			}
			if _, err := stepper.Run(context.Background(), Step{Chair: chair, Dir: dir}); !check(err) {
				t.Errorf("Run: %v", err)
			}
			if _, err := stepper.Seat(context.Background(), "test", chair, dir); !check(err) {
				t.Errorf("Seat: %v", err)
			}
			if requests != 0 {
				t.Errorf("got %d requests", requests)
			}
		})
	}
}

func TestNativePathQuotesNonPrintableRunes(t *testing.T) {
	for _, s := range []string{"a\u009bb", "a\u2028b"} {
		if got := nativePath(s); got == s {
			t.Errorf("not quoted: %q", got)
		}
	}
}

func TestTheLaunchCheckNeverReadsTheGitObjectStoreAfterBinding(t *testing.T) {
	dir := t.TempDir()
	gitTestNative(t, dir, "init", "-q")
	path := filepath.Join(dir, ".claude/agents/demo.md")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("---\nname: demo\n---\n"), 0644); err != nil {
		t.Fatal(err)
	}
	gitTestNative(t, dir, "add", ".")
	gitTestNative(t, dir, "commit", "-qm", "base")
	chair, err := ResolveNativeDefinition(dir, Chair{NativeAgent: "demo"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(dir, ".git"), filepath.Join(dir, "hidden")); err != nil {
		t.Fatal(err)
	}
	if err := chair.CheckDefinition(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("---\r\nname: demo\r\n---\r\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := chair.CheckDefinition(dir); err == nil || !strings.Contains(err.Error(), "only in line endings (CR)") {
		t.Fatalf("CR: %v", err)
	}
}

func TestAChairWithoutAPinnedManifestIsNeverLaunched(t *testing.T) {
	c := Chair{NativeAgent: "demo", Definition: "repository", DefinitionBase: "base"}
	if err := c.CheckDefinition(t.TempDir()); !errors.Is(err, ErrNativeDefinition) || !strings.Contains(err.Error(), "rehearse again") {
		t.Fatalf("%v", err)
	}
}

func TestBindingRefusesABlobThatDoesNotMatchItsObjectID(t *testing.T) {
	dir := t.TempDir()
	gitTestNative(t, dir, "init", "-q")
	path := filepath.Join(dir, ".claude/agents/demo.md")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("---\nname: demo\n---\n"), 0644); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(dir, ".claude/agents/other.txt")
	if err := os.WriteFile(other, []byte("original"), 0644); err != nil {
		t.Fatal(err)
	}
	gitTestNative(t, dir, "add", ".")
	gitTestNative(t, dir, "commit", "-qm", "base")
	oid := strings.TrimSpace(gitTestNative(t, dir, "rev-parse", "HEAD:.claude/agents/other.txt"))
	obj := filepath.Join(dir, ".git/objects", oid[:2], oid[2:])
	var b bytes.Buffer
	z := zlib.NewWriter(&b)
	fmt.Fprint(z, "blob 6\x00forged")
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(obj, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(obj, b.Bytes(), 0444); err != nil {
		t.Fatal(err)
	}
	_, err := ResolveNativeDefinition(dir, Chair{NativeAgent: "demo"}, true)
	if err == nil || !strings.Contains(err.Error(), "content does not match its object ID") {
		t.Fatalf("%v", err)
	}
}

func TestNativeErrorsQuoteNonPrintablePaths(t *testing.T) {
	for _, runeName := range []string{"\u009b", "\u2028"} {
		for _, scenario := range []string{"launch", "ambiguity", "shadow"} {
			t.Run(fmt.Sprintf("%U-%s", []rune(runeName)[0], scenario), func(t *testing.T) {
				dir := t.TempDir()
				gitTestNative(t, dir, "init", "-q")
				path := filepath.Join(dir, ".claude/agents/demo.md")
				if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
					t.Fatal(err)
				}
				if scenario != "shadow" {
					if err := os.WriteFile(path, []byte("---\nname: demo\n---\n"), 0644); err != nil {
						t.Fatal(err)
					}
				}
				odd := filepath.Join(dir, ".claude/agents", "odd"+runeName+".md")
				if scenario != "launch" {
					if err := os.WriteFile(odd, []byte("---\nname: demo\n---\n"), 0644); err != nil {
						t.Fatal(err)
					}
				}
				gitTestNative(t, dir, "add", ".")
				gitTestNative(t, dir, "commit", "-qm", "base")
				if scenario == "shadow" {
					config := t.TempDir()
					t.Setenv("CLAUDE_CONFIG_DIR", config)
					user := filepath.Join(config, "agents/demo.md")
					if err := os.MkdirAll(filepath.Dir(user), 0755); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(user, []byte("---\nname: demo\n---\n"), 0644); err != nil {
						t.Fatal(err)
					}
				}
				chair, err := ResolveNativeDefinition(dir, Chair{NativeAgent: "demo"}, scenario != "shadow")
				if scenario == "launch" {
					if err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(odd, []byte("new"), 0644); err != nil {
						t.Fatal(err)
					}
					err = chair.CheckDefinition(dir)
				}
				if err == nil {
					t.Fatal("expected error")
				}
				for _, r := range err.Error() {
					if !unicode.IsPrint(r) {
						t.Fatalf("non-printable rune in %q", err)
					}
				}
				if !strings.Contains(err.Error(), "odd") {
					t.Fatal(err)
				}
			})
		}
	}
}
