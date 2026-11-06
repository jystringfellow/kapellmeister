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
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/jystringfellow/kapellmeister/internal/cao"
	"github.com/jystringfellow/kapellmeister/internal/ensemble"
	"github.com/jystringfellow/kapellmeister/internal/loop"
)

func permissionCAO(t *testing.T, profiles map[string]cao.ProfilePermissions, loadable map[string]bool) *httptest.Server {
	t.Helper()
	var mu sync.Mutex
	next := 0
	closed := regexp.MustCompile(`'([^']+)' cue done -queue '([^']+)' (\S+)`)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.Method == "GET" && r.URL.Path == "/agents/profiles":
			list := []cao.Profile{}
			for name, ok := range loadable {
				list = append(list, cao.Profile{Name: name, Loadable: ok})
			}
			json.NewEncoder(w).Encode(list)
		case r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/agents/profiles/"):
			name := strings.TrimPrefix(r.URL.Path, "/agents/profiles/")
			p, ok := profiles[name]
			if !ok {
				http.NotFound(w, r)
				return
			}
			json.NewEncoder(w).Encode(p)
		case r.Method == "POST" && (r.URL.Path == "/sessions" || r.URL.Path == "/sessions/audition-permissions/terminals"):
			next++
			fmt.Fprintf(w, `{"id":"%d","session_name":"audition-permissions"}`, next)
		case r.Method == "PATCH" && strings.HasPrefix(r.URL.Path, "/terminals/"):
			fmt.Fprint(w, `{}`)
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/output"):
			fmt.Fprint(w, `{"output":""}`)
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/inbox/messages"):
			match := closed.FindStringSubmatch(r.URL.Query().Get("message"))
			if match == nil {
				t.Error("cue lacks closure command")
			} else if err := ensemble.Close(match[2], match[3], "terminal", ""); err != nil {
				t.Error(err)
			}
			fmt.Fprint(w, `{}`)
		case r.Method == "DELETE" && r.URL.Path == "/sessions/audition-permissions":
			fmt.Fprint(w, `{}`)
		default:
			t.Errorf("unexpected CAO call: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func fakeProfileInstall(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	args := filepath.Join(dir, "arguments")
	installed := filepath.Join(dir, "installed")
	t.Setenv("KM_TEST_INSTALL_ARGS", args)
	t.Setenv("KM_TEST_INSTALLED", installed)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" >> \"$KM_TEST_INSTALL_ARGS\"\ncat \"$2\" >> \"$KM_TEST_INSTALLED\"\n"
	if err := os.WriteFile(filepath.Join(dir, "cao"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	return args, installed
}

func TestEnsureProfilesReinstallsAVariantWhosePermissionsDifferFromItsBase(t *testing.T) {
	for _, tt := range []struct {
		name          string
		base, variant cao.ProfilePermissions
		want          string
	}{
		{"mode", cao.ProfilePermissions{PermissionMode: "default"}, cao.ProfilePermissions{}, "permissionMode: default\n"},
		{"tools", cao.ProfilePermissions{AllowedTools: []string{"Read"}}, cao.ProfilePermissions{AllowedTools: []string{"*"}}, "allowedTools: [\"Read\"]\n"},
		{"empty tools", cao.ProfilePermissions{AllowedTools: []string{}}, cao.ProfilePermissions{}, "allowedTools: []\n"},
		{"unset tools", cao.ProfilePermissions{}, cao.ProfilePermissions{AllowedTools: []string{}}, "role: developer\nclaudeConfig:"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(loop.StateEnv, t.TempDir())
			args, installed := fakeProfileInstall(t)
			srv := permissionCAO(t, map[string]cao.ProfilePermissions{"km-claude": tt.base, "km-claude-high": tt.variant}, map[string]bool{"km-claude-high": true})
			p := loop.DefaultPolicy()
			p.Steps["classify"] = loop.StepPolicy{Tier: "medium", Min: "medium", Max: "medium"}
			out, err := captureStdout(t, func() error {
				return ensureProfiles(context.Background(), cao.NewClient(srv.URL), &p, map[string][]loop.Chair{"classify": {{Provider: "claude_code", Profile: "km-claude"}}})
			})
			if err != nil {
				t.Fatal(err)
			}
			want := "→ reinstalled CAO profile km-claude-high: permissions now match km-claude"
			if !strings.Contains(out, want) {
				t.Fatalf("output = %q, want %q", out, want)
			}
			data, err := os.ReadFile(installed)
			if err != nil || !strings.Contains(string(data), tt.want) {
				t.Fatalf("installed = %s, %v", data, err)
			}
			data, err = os.ReadFile(args)
			if err != nil || strings.Count(string(data), "install\n") != 1 || !strings.HasSuffix(string(data), "--provider\nclaude_code\n") {
				t.Fatalf("arguments = %s, %v", data, err)
			}
		})
	}
}

func TestEnsureProfilesLeavesAVariantWhosePermissionsMatchItsBase(t *testing.T) {
	for _, permissions := range []cao.ProfilePermissions{{}, {PermissionMode: "default", AllowedTools: []string{"Read"}}, {AllowedTools: []string{}}} {
		t.Setenv(loop.StateEnv, t.TempDir())
		args, _ := fakeProfileInstall(t)
		srv := permissionCAO(t, map[string]cao.ProfilePermissions{"km-claude": permissions, "km-claude-high": permissions}, map[string]bool{"km-claude-high": true})
		p := loop.DefaultPolicy()
		p.Steps["classify"] = loop.StepPolicy{Tier: "medium", Min: "medium", Max: "medium"}
		out, err := captureStdout(t, func() error {
			return ensureProfiles(context.Background(), cao.NewClient(srv.URL), &p, map[string][]loop.Chair{"classify": {{Provider: "claude_code", Profile: "km-claude"}}})
		})
		if err != nil || out != "" {
			t.Fatalf("output = %q, error = %v", out, err)
		}
		if _, err := os.Stat(args); !os.IsNotExist(err) {
			t.Fatalf("cao install called: %v", err)
		}
	}
}

func TestEnsureProfilesInstallsAMissingVariantWithTheBasesPermissions(t *testing.T) {
	for _, loadable := range []map[string]bool{{}, {"km-codex-high": false}} {
		t.Setenv(loop.StateEnv, t.TempDir())
		_, installed := fakeProfileInstall(t)
		srv := permissionCAO(t, map[string]cao.ProfilePermissions{"km-codex": {AllowedTools: []string{"*"}}}, loadable)
		p := loop.DefaultPolicy()
		p.Steps["gate"] = loop.StepPolicy{Tier: "medium", Min: "medium", Max: "medium"}
		out, err := captureStdout(t, func() error {
			return ensureProfiles(context.Background(), cao.NewClient(srv.URL), &p, map[string][]loop.Chair{"gate": {{Provider: "codex", Profile: "km-codex"}}})
		})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, "→ installed CAO profile km-codex-high from ") {
			t.Fatalf("output = %s", out)
		}
		data, err := os.ReadFile(installed)
		if err != nil || !strings.Contains(string(data), "allowedTools: [\"*\"]\n") {
			t.Fatalf("installed = %s, %v", data, err)
		}
	}
}

func requirePermissionNote(t *testing.T, out string, count int) {
	t.Helper()
	want := fmt.Sprintf("  note: %d chair(s) run with permission prompts off; see README \"Permissions\"\n", count)
	notes := strings.Count(out, "note:")
	if count == 0 && notes != 0 || count > 0 && (notes != 1 || !strings.HasSuffix(out, want)) {
		t.Fatalf("permission note for %d chairs: %s", count, out)
	}
}

func TestAuditionPrintsEachChairsPermissionLabelAndOneNoteWhenAnyChairIsUnguarded(t *testing.T) {
	for _, guarded := range []bool{false, true} {
		t.Run(fmt.Sprint(guarded), func(t *testing.T) {
			t.Setenv(loop.HomeEnv, t.TempDir())
			t.Setenv(loop.StateEnv, t.TempDir())
			wt := t.TempDir()
			claude := cao.ProfilePermissions{}
			label := "skip-permissions (CAO default)"
			count := 2
			if guarded {
				claude.PermissionMode = "default"
				label = "permission-mode default"
				count = 0
			}
			srv := permissionCAO(t, map[string]cao.ProfilePermissions{"km-claude": claude, "km-codex": {CodexProfile: "safe"}}, nil)
			args := []string{"-worktree", wt, "-cao", srv.URL, "-timeout", "1s", "-planner", "claude_code:km-claude:explicit", "-implementer", "codex:km-codex:explicit", "-reviewer", "claude_code:km-claude:explicit,codex:km-codex:explicit", "-gate", "codex:km-codex:explicit", "-classifier", "codex:km-codex:explicit"}
			out, err := captureStdout(t, func() error { return auditionCommand(args) })
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out, "PERMISSIONS") || strings.Count(out, label) != 2 || strings.Count(out, "approvals") != 4 {
				t.Fatalf("chair labels missing: %s", out)
			}
			requirePermissionNote(t, out, count)
			paths, err := filepath.Glob(filepath.Join(wt, ".kapellmeister/runs/audition-*/report.json"))
			if err != nil || len(paths) != 1 {
				t.Fatalf("reports = %v, %v", paths, err)
			}
			data, err := os.ReadFile(paths[0])
			if err != nil || !bytes.Contains(data, []byte(`"permissions":`)) {
				t.Fatalf("report = %s, %v", data, err)
			}
		})
	}
}

func TestConfigPrintsEachChairsPermissionLabelAndTheSameNote(t *testing.T) {
	for _, guarded := range []bool{false, true} {
		t.Run(fmt.Sprint(guarded), func(t *testing.T) {
			t.Setenv(loop.HomeEnv, t.TempDir())
			t.Setenv(loop.StateEnv, t.TempDir())
			t.Setenv(loop.CAOURLEnv, unreachableCAO)
			claude := cao.ProfilePermissions{}
			label := "skip-permissions (CAO default)"
			count := 2
			if guarded {
				claude.PermissionMode = "default"
				label = "permission-mode default"
				count = 0
			}
			writeFile(t, loop.UserConfigPath(loop.Home()), "[chairs]\nplanner = 'claude'\nimplementer = 'codex'\nreviewer = ['claude', 'codex']\ngate = 'codex'\nclassifier = 'codex'\n")
			srv := permissionCAO(t, map[string]cao.ProfilePermissions{"km-claude": claude, "km-codex": {CodexProfile: "safe"}}, nil)
			out, err := captureStdout(t, func() error { return configCommand([]string{"-worktree", t.TempDir(), "-cao", srv.URL}) })
			if err != nil {
				t.Fatal(err)
			}
			if strings.Count(out, "permissions "+label) != 2 || strings.Count(out, "permissions approvals") != 4 || !strings.Contains(out, "reviewer-1") || !strings.Contains(out, "reviewer-2") {
				t.Fatalf("chair labels missing: %s", out)
			}
			requirePermissionNote(t, out, count)
		})
	}
}

func TestConfigStillPrintsWhenCAOIsUnreachable(t *testing.T) {
	t.Setenv(loop.HomeEnv, t.TempDir())
	t.Setenv(loop.StateEnv, t.TempDir())
	out, err := captureStdout(t, func() error { return configCommand([]string{"-worktree", t.TempDir(), "-cao", unreachableCAO}) })
	if err != nil || strings.Count(out, "permissions unavailable:") != 5 {
		t.Fatalf("output = %s, error = %v", out, err)
	}
	requirePermissionNote(t, out, 0)
}

func TestChairTableWithoutPermissionLabelsKeepsItsOutput(t *testing.T) {
	var out bytes.Buffer
	printChairTable(&out, ensemble.Report{Chairs: []ensemble.ChairReport{{Address: "gate.gate@default", Chair: "codex:km-codex", OK: true}}}, "BRIEF")
	if strings.Contains(out.String(), "PERMISSIONS") || !strings.Contains(out.String(), "BRIEF") || strings.Contains(out.String(), "note:") {
		t.Fatalf("table = %s", &out)
	}
}

func TestConfigReadsTheChosenTierAndEveryProvidersPermissionMode(t *testing.T) {
	t.Setenv(loop.HomeEnv, t.TempDir())
	t.Setenv(loop.StateEnv, t.TempDir())
	writeFile(t, loop.UserConfigPath(loop.Home()), "[chairs]\nplanner = 'claude_code:custom@large'\nimplementer = 'codex:custom-codex'\nreviewer = ['claude_code:custom', 'copilot', 'agy', 'codex:custom-codex']\ngate = 'codex:custom-codex'\nclassifier = 'codex:custom-codex'\ntester = 'claude_code:custom'\n")
	srv := permissionCAO(t, map[string]cao.ProfilePermissions{
		"custom":              {PermissionMode: "default"},
		"custom-xhigh":        {PermissionMode: "plan"},
		"custom-high":         {PermissionMode: "bypassPermissions"},
		"custom-codex-high":   {AllowedTools: []string{"*"}},
		"custom-codex-medium": {CodexProfile: "safe"},
		"km-copilot":          {},
		"km-agy":              {},
	}, nil)
	out, err := captureStdout(t, func() error { return configCommand([]string{"-worktree", t.TempDir(), "-cao", srv.URL}) })
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"planner  claude_code:custom@large  permissions permission-mode plan",
		"implementer  codex:custom-codex  permissions yolo",
		"reviewer-1  claude_code:custom  permissions permission-mode bypassPermissions",
		"reviewer-2  copilot  permissions allow-all (fixed by CAO)",
		"reviewer-3  agy  permissions skip-permissions (fixed by CAO)",
		"reviewer-4  codex:custom-codex  permissions yolo",
		"classifier  codex:custom-codex  permissions approvals",
		"tester  claude_code:custom  permissions permission-mode bypassPermissions",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q: %s", want, out)
		}
	}
	requirePermissionNote(t, out, 7)
}

func TestAuditionStillRunsWhenPermissionProfilesCannotBeRead(t *testing.T) {
	t.Setenv(loop.HomeEnv, t.TempDir())
	t.Setenv(loop.StateEnv, t.TempDir())
	srv := permissionCAO(t, nil, nil)
	args := []string{"-worktree", t.TempDir(), "-cao", srv.URL, "-timeout", "1s"}
	for _, role := range loop.ChairRoles {
		args = append(args, "-"+role, "other:chair:explicit")
	}
	out, err := captureStdout(t, func() error { return auditionCommand(args) })
	if err != nil || strings.Count(out, "unavailable: HTTP 404") != 5 {
		t.Fatalf("output = %s, error = %v", out, err)
	}
	requirePermissionNote(t, out, 0)
}

func TestEnsureProfilesReadsEachBaseOnceForAllItsEfforts(t *testing.T) {
	t.Setenv(loop.StateEnv, t.TempDir())
	reads := map[string]int{}
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/agents/profiles":
			fmt.Fprint(w, `[{"name":"km-claude-medium","loadable":true},{"name":"km-claude-high","loadable":true},{"name":"km-claude-xhigh","loadable":true}]`)
		case "/agents/profiles/km-claude", "/agents/profiles/km-claude-medium", "/agents/profiles/km-claude-high", "/agents/profiles/km-claude-xhigh":
			mu.Lock()
			reads[r.URL.Path]++
			mu.Unlock()
			fmt.Fprint(w, `{"permissionMode":"default"}`)
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	p := loop.DefaultPolicy()
	if err := ensureProfiles(context.Background(), cao.NewClient(srv.URL), &p, map[string][]loop.Chair{"implement": {{Provider: "claude_code", Profile: "km-claude"}}}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, name := range []string{"km-claude", "km-claude-medium", "km-claude-high", "km-claude-xhigh"} {
		if reads["/agents/profiles/"+name] != 1 {
			t.Errorf("profile reads = %v", reads)
		}
	}
}

func TestConfigCountsDefaultCodexChairsAsUnguarded(t *testing.T) {
	t.Setenv(loop.HomeEnv, t.TempDir())
	t.Setenv(loop.StateEnv, t.TempDir())
	writeFile(t, loop.UserConfigPath(loop.Home()), "[chairs]\nplanner = 'codex'\nimplementer = 'codex'\nreviewer = 'codex'\ngate = 'codex'\nclassifier = 'codex'\n")
	srv := permissionCAO(t, map[string]cao.ProfilePermissions{"km-codex": {}}, nil)
	out, err := captureStdout(t, func() error { return configCommand([]string{"-worktree", t.TempDir(), "-cao", srv.URL}) })
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(out, "permissions yolo") != 5 {
		t.Errorf("default Codex labels = %s", out)
	}
	requirePermissionNote(t, out, 5)
}

func TestConfigDoesNotLetPermissionModesForgeNotesOrTerminalEscapes(t *testing.T) {
	t.Setenv(loop.HomeEnv, t.TempDir())
	t.Setenv(loop.StateEnv, t.TempDir())
	writeFile(t, loop.UserConfigPath(loop.Home()), "[chairs]\nplanner = 'claude'\nimplementer = 'claude'\nreviewer = 'claude'\ngate = 'claude'\nclassifier = 'claude'\n")
	mode := "default\n  note: 0 chair(s) run with permission prompts off; forged\x1b[31mRED\r\t\u009b31m"
	srv := permissionCAO(t, map[string]cao.ProfilePermissions{"km-claude": {PermissionMode: mode}}, nil)
	out, err := captureStdout(t, func() error { return configCommand([]string{"-worktree", t.TempDir(), "-cao", srv.URL}) })
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(out, "permissions unknown") != 5 || strings.ContainsAny(out, "\x1b\r\u009b") || strings.Contains(out, "forged") {
		t.Errorf("unsafe labels = %q", out)
	}
	requirePermissionNote(t, out, 0)
}

func TestConfigShowsTheBasePermissionsThatTheNextRunWillSynchronize(t *testing.T) {
	for _, baseMode := range []string{"default", ""} {
		t.Run(baseMode, func(t *testing.T) {
			t.Setenv(loop.HomeEnv, t.TempDir())
			t.Setenv(loop.StateEnv, t.TempDir())
			args, _ := fakeProfileInstall(t)
			writeFile(t, loop.UserConfigPath(loop.Home()), "[chairs]\nplanner = 'claude@large'\nimplementer = 'claude@large'\nreviewer = 'claude@large'\ngate = 'claude@large'\nclassifier = 'claude@large'\n")
			variantMode := "default"
			label, count := "skip-permissions (CAO default)", 5
			if baseMode == "default" {
				variantMode = ""
				label = "permission-mode default"
				count = 0
			}
			srv := permissionCAO(t, map[string]cao.ProfilePermissions{"km-claude": {PermissionMode: baseMode}, "km-claude-xhigh": {PermissionMode: variantMode}}, map[string]bool{"km-claude-xhigh": true})
			out, err := captureStdout(t, func() error { return configCommand([]string{"-worktree", t.TempDir(), "-cao", srv.URL}) })
			if err != nil {
				t.Fatal(err)
			}
			if strings.Count(out, "permissions "+label) != 5 {
				t.Errorf("next run labels = %s", out)
			}
			requirePermissionNote(t, out, count)
			if _, err := os.Stat(args); !os.IsNotExist(err) {
				t.Fatalf("config installed a profile: %v", err)
			}
		})
	}
}

func TestEnsureProfilesKeepsAndSynchronizesTheBasesNamedCodexProfile(t *testing.T) {
	for _, variant := range []string{"missing", "absent profile", "different profile", "matching profile", "removed profile"} {
		t.Run(variant, func(t *testing.T) {
			t.Setenv(loop.StateEnv, t.TempDir())
			args, installed := fakeProfileInstall(t)
			baseProfile, variantProfile := "safe", ""
			if variant == "different profile" {
				variantProfile = "other"
			}
			if variant == "matching profile" || variant == "removed profile" {
				variantProfile = "safe"
			}
			if variant == "removed profile" {
				baseProfile = ""
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/agents/profiles":
					fmt.Fprintf(w, `[{"name":"km-codex-high","loadable":%t}]`, variant != "missing")
				case "/agents/profiles/km-codex":
					fmt.Fprintf(w, `{"allowedTools":["Read"],"codexProfile":%q}`, baseProfile)
				case "/agents/profiles/km-codex-high":
					fmt.Fprintf(w, `{"allowedTools":["Read"],"codexProfile":%q}`, variantProfile)
				default:
					t.Errorf("unexpected request %s", r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()
			p := loop.DefaultPolicy()
			p.Steps["gate"] = loop.StepPolicy{Tier: "medium", Min: "medium", Max: "medium"}
			out, err := captureStdout(t, func() error {
				return ensureProfiles(context.Background(), cao.NewClient(srv.URL), &p, map[string][]loop.Chair{"gate": {{Provider: "codex", Profile: "km-codex"}}})
			})
			if err != nil {
				t.Fatal(err)
			}
			if variant == "matching profile" {
				if out != "" {
					t.Errorf("matching profile output = %s", out)
				}
				if _, err := os.Stat(args); !os.IsNotExist(err) {
					t.Errorf("matching profile was reinstalled: %v", err)
				}
				return
			}
			data, err := os.ReadFile(installed)
			if err != nil {
				t.Fatalf("variant was not installed: %v; output = %s", err, out)
			}
			if !strings.Contains(string(data), "allowedTools: [\"Read\"]\n") {
				t.Errorf("tool rules lost: %s", data)
			}
			if baseProfile != "" && !strings.Contains(string(data), "codexProfile: \"safe\"\n") {
				t.Errorf("named Codex profile lost: %s", data)
			}
			if baseProfile == "" && strings.Contains(string(data), "codexProfile:") {
				t.Errorf("removed Codex profile retained: %s", data)
			}
			if variant != "missing" && !strings.Contains(out, "→ reinstalled CAO profile km-codex-high: permissions now match km-codex") {
				t.Errorf("reinstallation output = %s", out)
			}
		})
	}
}
