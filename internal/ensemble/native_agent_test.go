package ensemble

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jystringfellow/kapellmeister/internal/loop"
)

func TestAuditionDoesNotLaunchANativeChairWhoseDefinitionChanged(t *testing.T) {
	dir, chair := changedNativeFixture(t)
	c := conductor{Ensemble: "test", Worktree: dir, Members: []Member{{Chair: chair}}}
	reports := make([]ChairReport, 1)
	if got := c.boot(context.Background(), "test", reports); got != "" {
		t.Fatal(got)
	}
	if !strings.Contains(reports[0].Problem, "differs from the base commit") {
		t.Fatal(reports[0].Problem)
	}
}

func TestReseatDoesNotLaunchANativeChairWhoseDefinitionChanged(t *testing.T) {
	dir, chair := changedNativeFixture(t)
	c := conductor{Worktree: dir}
	var report ChairReport
	c.bootInto(context.Background(), "test", Member{Chair: chair}, &report)
	if !strings.Contains(report.Problem, "differs from the base commit") {
		t.Fatal(report.Problem)
	}
}

func TestARepositoryEnsembleMaySetNativeAgent(t *testing.T) {
	spec, err := CheckPortable("band", "band.toml", "[sections.dev.chairs.one]\npart = \"planner\"\nagents = [\"claude\"]\nnative_agent = \"demo_1\"\n", loop.BuiltInAgents())
	if err != nil {
		t.Fatal(err)
	}
	if spec.Sections[0].Chairs[0].NativeAgent != "demo_1" || !spec.Portable() {
		t.Fatal(spec)
	}
}

func TestANativeAgentNameOutsideThePatternIsRefusedNamingTheFileAndChair(t *testing.T) {
	for _, name := range []string{"", "Bad", "../x", strings.Repeat("a", 65)} {
		_, err := CheckPortable("band", "band.toml", fmt.Sprintf("[sections.dev.chairs.one]\npart = \"planner\"\nagents = [\"claude\"]\nnative_agent = %q\n", name), loop.BuiltInAgents())
		if err == nil || !strings.Contains(err.Error(), "band.toml") || !strings.Contains(err.Error(), "dev.one@band") {
			t.Fatalf("%q: %v", name, err)
		}
	}
}

func TestANativeChairRefusesATierAPinOrAnExplicitModelBecauseTheDefinitionSetsIt(t *testing.T) {
	for _, line := range []string{"tier = \"small\"\nagents = [\"claude\"]", "agents = [\"claude@small\"]", "agents = [\"claude_code:foo:model\"]"} {
		_, err := Check("band", "band.toml", "[sections.dev.chairs.one]\npart = \"planner\"\nnative_agent = \"demo\"\n"+line+"\n", loop.BuiltInAgents())
		if err == nil || !strings.Contains(err.Error(), "agent definition sets the model") {
			t.Fatalf("%s: %v", line, err)
		}
	}
}

func TestANativeChairSkipsAgentsOnOtherProviders(t *testing.T) {
	spec, err := CheckPortable("band", "band.toml", "[sections.dev.chairs.one]\npart = \"planner\"\nagents = [\"codex\", \"claude\"]\nnative_agent = \"demo\"\n", loop.BuiltInAgents())
	if err != nil {
		t.Fatal(err)
	}
	members, err := spec.Bind(loop.BuiltInAgents(), []string{"codex", "claude"})
	if err != nil {
		t.Fatal(err)
	}
	c := members[0].Chair
	if c.Provider != "claude_code" || c.Profile != "km-claude-native-demo" || c.Agent != "claude" || c.NativeBase != "km-claude" || c.NativeAgent != "demo" {
		t.Fatalf("%+v", c)
	}
	_, err = spec.Bind(loop.BuiltInAgents(), []string{"codex"})
	if err == nil || !strings.Contains(err.Error(), "only claude_code") {
		t.Fatalf("%v", err)
	}
}

func changedNativeFixture(t *testing.T) (string, loop.Chair) {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}} {
		c := exec.Command("git", args...)
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("%s: %v", out, err)
		}
	}
	path := filepath.Join(dir, ".claude/agents/demo.md")
	os.MkdirAll(filepath.Dir(path), 0755)
	os.WriteFile(path, []byte("---\nname: demo\n---\n"), 0644)
	for _, args := range [][]string{{"add", "."}, {"-c", "user.name=T", "-c", "user.email=t@x", "commit", "-qm", "base"}} {
		c := exec.Command("git", args...)
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("%s: %v", out, err)
		}
	}
	chair, err := loop.ResolveNativeDefinition(dir, loop.Chair{NativeAgent: "demo"}, true)
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(path, []byte("changed"), 0644)
	return dir, chair
}

func TestNativeProfileNamesLeaveRoomForCAOsWindowSuffix(t *testing.T) {
	for _, n := range []int{42, 43, 44, 45, 46, 47, 48} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			name := strings.Repeat("a", n)
			spec, err := CheckPortable("band", "band.toml", "[sections.dev.chairs.one]\npart = 'planner'\nagents = ['claude']\nnative_agent = '"+name+"'\n", loop.BuiltInAgents())
			if err != nil {
				t.Fatal(err)
			}
			_, err = spec.Bind(loop.BuiltInAgents(), []string{"claude"})
			if n == 42 {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), "59") || !strings.Contains(err.Error(), "64") || !strings.Contains(err.Error(), "window") {
				t.Fatalf("expected refusal reserving CAO's window suffix: %v", err)
			}
		})
	}
}

func TestAUserNativeChairRejectsRawEntriesWithTheRepositoryAgentMessage(t *testing.T) {
	for _, profile := range []string{"x/../../../y", "km-claude2"} {
		text := fmt.Sprintf("[sections.dev.chairs.one]\npart = 'planner'\nagents = ['claude_code:%s']\nnative_agent = 'demo'\n", profile)
		_, err := Check("band", "band.toml", text, loop.BuiltInAgents())
		want := checkAgent("claude_code:"+profile, true, loop.BuiltInAgents())
		if err == nil || want == nil || !strings.Contains(err.Error(), "band.toml") || !strings.Contains(err.Error(), want.Error()) {
			t.Fatalf("%s: %v", profile, err)
		}
	}
}
