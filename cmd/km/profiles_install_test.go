package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jystringfellow/kapellmeister/internal/cao"
	"github.com/jystringfellow/kapellmeister/internal/loop"
	"github.com/jystringfellow/kapellmeister/profiles"
)

func baseProfileInstall(t *testing.T, config string) (home, args, url string) {
	t.Helper()
	t.Setenv(loop.HomeEnv, t.TempDir())
	t.Setenv(loop.StateEnv, t.TempDir())
	writeFile(t, loop.UserConfigPath(loop.Home()), config)
	args, _ = fakeProfileInstall(t)
	t.Setenv("KM_TEST_REFUSE_INSTALL", "")
	script := `#!/bin/sh
set -eu
printf '%s\n' "$@" >> "$KM_TEST_INSTALL_ARGS"
if [ -n "$KM_TEST_REFUSE_INSTALL" ]; then
 echo 'Error: name already installed from another source'
 exit 0
fi
mkdir -p "$CAO_HOME_DIR/agent-store"
cp "$2" "$CAO_HOME_DIR/agent-store/$(basename "$2")"
`
	writeFile(t, filepath.Join(filepath.Dir(args), "cao"), script)
	home = os.Getenv("CAO_HOME_DIR")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if profileProbe(w, r, home) {
			return
		}
		if r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/agents/profiles/") {
			data, err := os.ReadFile(filepath.Join(home, "agent-store", strings.TrimPrefix(r.URL.Path, "/agents/profiles/")+".md"))
			if err != nil {
				http.NotFound(w, r)
				return
			}
			description := ""
			for _, line := range strings.Split(string(data), "\n") {
				if strings.HasPrefix(line, "description: ") {
					description = strings.TrimPrefix(line, "description: ")
					break
				}
			}
			json.NewEncoder(w).Encode(cao.ProfilePermissions{Description: description})
			return
		}
		fmt.Fprint(w, `[]`)
	}))
	t.Cleanup(srv.Close)
	t.Setenv(loop.CAOURLEnv, srv.URL)
	return home, args, srv.URL
}

func embeddedProfile(t *testing.T, name string) string {
	t.Helper()
	data, err := profiles.Source(name)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func noBaseInstaller(t *testing.T, args string) {
	t.Helper()
	if data, err := os.ReadFile(args); !os.IsNotExist(err) {
		t.Fatalf("installer called: %s (%v)", data, err)
	}
}

func TestProfilesInstallInstallsTheBaseProfilesOfTheAvailableAgentsAndReadsThemBack(t *testing.T) {
	home, _, _ := baseProfileInstall(t, "[agents]\navailable = [\"claude\", \"codex\", \"claude\"]\n")
	var out bytes.Buffer
	if err := profilesCommand([]string{"install"}, &out); err != nil {
		t.Fatal(err)
	}
	if out.String() != "installed km-claude (claude_code)\ninstalled km-codex (codex)\n" {
		t.Fatal(out.String())
	}
	for _, name := range []string{"km-claude", "km-codex"} {
		data, err := os.ReadFile(filepath.Join(home, "agent-store", name+".md"))
		if err != nil || string(data) != embeddedProfile(t, name) {
			t.Fatalf("readback %s: %s %v", name, data, err)
		}
	}
	if _, err := os.Stat(filepath.Join(home, "agent-store/km-agy.md")); !os.IsNotExist(err) {
		t.Fatal("unselected profile installed")
	}
}

func TestProfilesInstallInstallsOnlyTheAgentsNamedOnTheCommandLine(t *testing.T) {
	home, _, _ := baseProfileInstall(t, "[agents]\navailable = [\"claude\"]\n")
	var out bytes.Buffer
	if err := profilesCommand([]string{"install", "codex", "codex"}, &out); err != nil {
		t.Fatal(err)
	}
	if out.String() != "installed km-codex (codex)\n" {
		t.Fatal(out.String())
	}
	if _, err := os.Stat(filepath.Join(home, "agent-store/km-claude.md")); !os.IsNotExist(err) {
		t.Fatal("available agent installed despite named selection")
	}
}

func TestProfilesInstallRunAgainReportsEveryProfileUnchangedAndRunsNoInstaller(t *testing.T) {
	_, args, _ := baseProfileInstall(t, "[agents]\navailable = [\"claude\",\"codex\"]\n")
	var out bytes.Buffer
	if err := profilesCommand([]string{"install"}, &out); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(args); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := profilesCommand([]string{"install"}, &out); err != nil {
		t.Fatal(err)
	}
	if out.String() != "unchanged km-claude\nunchanged km-codex\n" {
		t.Fatal(out.String())
	}
	noBaseInstaller(t, args)
}

func TestProfilesInstallRefusesToReplaceDifferingContentWithoutReplace(t *testing.T) {
	home, args, _ := baseProfileInstall(t, "")
	path := filepath.Join(home, "agent-store/km-claude.md")
	original := embeddedProfile(t, "km-claude") + "\ncustom\n"
	writeFile(t, path, original)
	var out bytes.Buffer
	err := profilesCommand([]string{"install", "claude", "codex"}, &out)
	if err == nil || !strings.Contains(out.String(), "refused km-claude: content differs") || !strings.Contains(out.String(), "-replace") || !strings.Contains(out.String(), "installed km-codex") {
		t.Fatalf("%s %v", &out, err)
	}
	data, _ := os.ReadFile(path)
	if string(data) != original {
		t.Fatal("differing profile changed")
	}
	calls, _ := os.ReadFile(args)
	if strings.Contains(string(calls), "km-claude.md") {
		t.Fatal("refused profile sent to cao")
	}
}

func TestProfilesInstallWithReplaceReplacesAProfileKmAuthored(t *testing.T) {
	home, _, _ := baseProfileInstall(t, "")
	path := filepath.Join(home, "agent-store/km-claude.md")
	writeFile(t, path, embeddedProfile(t, "km-claude")+"\ncustom\n")
	var out bytes.Buffer
	if err := profilesCommand([]string{"install", "-replace", "claude"}, &out); err != nil {
		t.Fatal(err)
	}
	if out.String() != "replaced km-claude (claude_code)\n" {
		t.Fatal(out.String())
	}
	data, _ := os.ReadFile(path)
	if string(data) != embeddedProfile(t, "km-claude") {
		t.Fatal("replacement differs")
	}
}

func TestProfilesInstallNeverReplacesAProfileKmDidNotAuthorEvenWithReplace(t *testing.T) {
	home, args, _ := baseProfileInstall(t, "")
	path := filepath.Join(home, "agent-store/km-claude.md")
	original := "---\nname: km-claude\ndescription: my chair\nprovider: claude_code\n---\n"
	writeFile(t, path, original)
	var out bytes.Buffer
	if err := profilesCommand([]string{"install", "-replace", "claude"}, &out); err == nil || !strings.Contains(out.String(), "not authored by kapellmeister") {
		t.Fatalf("%s %v", &out, err)
	}
	data, _ := os.ReadFile(path)
	if string(data) != original {
		t.Fatal("user profile replaced")
	}
	noBaseInstaller(t, args)
}

func TestProfilesInstallLeavesUserDefinedAgentsUntouched(t *testing.T) {
	for _, name := range []string{"mine", "claude"} {
		t.Run(name, func(t *testing.T) {
			_, args, _ := baseProfileInstall(t, fmt.Sprintf("[agents]\navailable = [%q]\n[agents.define]\n%s = \"claude_code:my-chair\"\n", name, name))
			var out bytes.Buffer
			if err := profilesCommand([]string{"install"}, &out); err != nil || out.String() != "nothing to install\n" {
				t.Fatalf("%s %v", &out, err)
			}
			out.Reset()
			if err := profilesCommand([]string{"install", name}, &out); err == nil || !strings.Contains(out.String(), "defined in [agents.define]") {
				t.Fatalf("%s %v", &out, err)
			}
			noBaseInstaller(t, args)
		})
	}
}

func TestProfilesInstallRefusesWhenTheCAOServerDoesNotUseTheCAOHome(t *testing.T) {
	home, args, _ := baseProfileInstall(t, "")
	t.Setenv(loop.CAOURLEnv, safetyProfiles(t, t.TempDir(), 0).URL)
	writeFile(t, filepath.Join(home, "agent-store/km-claude.md"), embeddedProfile(t, "km-claude"))
	var out bytes.Buffer
	err := profilesCommand([]string{"install", "claude"}, &out)
	if err == nil || !strings.Contains(err.Error(), "probe") || out.Len() != 0 {
		t.Fatalf("%s %v", &out, err)
	}
	noBaseInstaller(t, args)
}

func TestProfilesInstallRefusesReplacementWhileAnotherRunThatMayDependOnTheProfileIsAlive(t *testing.T) {
	home, args, _ := baseProfileInstall(t, "")
	path := filepath.Join(home, "agent-store/km-codex.md")
	original := embeddedProfile(t, "km-codex") + "\nold\n"
	writeFile(t, path, original)
	root := filepath.Join(loop.StateDir(), "runs")
	s := statusFixture(t, root, "conducting")
	child := statusChild(t)
	s.PID = child.Process.Pid
	var err error
	s.ProcessStart, err = processStart(s.PID)
	if err != nil {
		t.Fatal(err)
	}
	saveStatusFixture(t, root, s.RunID, s)
	var out bytes.Buffer
	err = profilesCommand([]string{"install", "-replace", "claude", "codex"}, &out)
	if err == nil || !strings.Contains(err.Error(), s.RunID) || !strings.Contains(out.String(), "refused km-codex") {
		t.Fatalf("%s %v", &out, err)
	}
	data, _ := os.ReadFile(path)
	if string(data) != original {
		t.Fatal("live profile replaced")
	}
	noBaseInstaller(t, args)
	if _, err := os.Stat(filepath.Join(home, "agent-store/km-claude.md")); !os.IsNotExist(err) {
		t.Fatal("earlier pending profile written before guard")
	}
}

func TestProfilesInstallFailsWhenCaoExitsZeroButTheProfileIsNotWhatKmSent(t *testing.T) {
	_, _, _ = baseProfileInstall(t, "")
	t.Setenv("KM_TEST_REFUSE_INSTALL", "yes")
	var out bytes.Buffer
	err := profilesCommand([]string{"install", "claude"}, &out)
	if err == nil || !strings.Contains(err.Error(), "exited 0") || !strings.Contains(err.Error(), "already installed") || strings.Contains(out.String(), "installed km-") {
		t.Fatalf("%s %v", &out, err)
	}
}

func TestProfilesInstallWithNoAvailableAgentsAndNoNamesExplainsWhatToDo(t *testing.T) {
	_, args, _ := baseProfileInstall(t, "")
	var out bytes.Buffer
	err := profilesCommand([]string{"install"}, &out)
	if err == nil || !strings.Contains(err.Error(), "[agents] available") || !strings.Contains(err.Error(), "km profiles install claude codex") {
		t.Fatal(err)
	}
	noBaseInstaller(t, args)
}

func TestProfilesInstallRejectsAnUnknownAgentNamingTheValidOnes(t *testing.T) {
	_, args, _ := baseProfileInstall(t, "")
	var out bytes.Buffer
	err := profilesCommand([]string{"install", "unknown"}, &out)
	if err == nil {
		t.Fatal("unknown accepted")
	}
	for _, name := range loop.BuiltInAgents().Names() {
		if !strings.Contains(err.Error(), name) {
			t.Fatal(err)
		}
	}
	noBaseInstaller(t, args)
}

func TestProfilesWithoutAnInstallVerbPrintsUsageAndExitsTwo(t *testing.T) {
	for _, args := range [][]string{{"profiles"}, {"profiles", "unknown"}} {
		var out bytes.Buffer
		if code := execute(args, &out); code != 2 || !strings.Contains(out.String(), "km profiles install") {
			t.Fatalf("exit %d: %s", code, &out)
		}
	}
}

func TestAMissingBuiltInBaseErrorNamesKmProfilesInstall(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/agents/profiles" {
			fmt.Fprint(w, `[]`)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	p := loop.DefaultPolicy()
	err := ensureProfiles(context.Background(), cao.NewClient(srv.URL), &p, map[string][]loop.Chair{"implement": {{Provider: "claude_code", Profile: "km-claude"}}})
	if err == nil || !strings.Contains(err.Error(), "km profiles install claude") {
		t.Fatal(err)
	}
}

func TestARefusedInstallIsReportedOnOneLineEvenWhenCaoPrintsMultipleLines(t *testing.T) {
	_, _, _ = baseProfileInstall(t, "")
	t.Setenv("KM_TEST_REFUSE_INSTALL", "yes")
	var out bytes.Buffer
	if err := profilesCommand([]string{"install", "claude"}, &out); err == nil {
		t.Fatal("refusal succeeded")
	}
	if strings.Count(out.String(), "\n") != 1 || !strings.HasPrefix(out.String(), "refused km-claude:") {
		t.Fatalf("profile report is not one line: %q", out.String())
	}
}
