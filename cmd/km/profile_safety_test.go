package main

import (
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
)

func profileProbe(w http.ResponseWriter, r *http.Request, home string) bool {
	if r.Method == "POST" && r.URL.Path == "/agents/profiles" {
		var p struct{ Name, Content string }
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			http.Error(w, err.Error(), 400)
			return true
		}
		dir := filepath.Join(home, "agent-store")
		if err := os.MkdirAll(dir, 0700); err != nil {
			http.Error(w, err.Error(), 500)
			return true
		}
		if err := os.WriteFile(filepath.Join(dir, p.Name+".md"), []byte(p.Content), 0600); err != nil {
			http.Error(w, err.Error(), 500)
			return true
		}
		w.WriteHeader(201)
		fmt.Fprint(w, `{}`)
		return true
	}
	if r.Method == "DELETE" && strings.HasPrefix(r.URL.Path, "/agents/profiles/km-probe-") {
		if err := os.Remove(filepath.Join(home, "agent-store", strings.TrimPrefix(r.URL.Path, "/agents/profiles/")+".md")); err != nil {
			http.Error(w, err.Error(), 500)
			return true
		}
		w.WriteHeader(204)
		return true
	}
	return false
}

func safetyProfiles(t *testing.T, serverHome string, probeStatus int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			if probeStatus != 0 {
				http.Error(w, "probe unavailable", probeStatus)
				return
			}
			if profileProbe(w, r, serverHome) {
				return
			}
		}
		if r.URL.Path == "/agents/profiles" {
			fmt.Fprint(w, `[]`)
			return
		}
		fmt.Fprint(w, `{"allowedTools":[]}`)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func installSafetyProfiles(url string) error {
	p := loop.DefaultPolicy()
	p.Steps["gate"] = loop.StepPolicy{Tier: "medium", Min: "medium", Max: "medium"}
	return ensureProfiles(context.Background(), cao.NewClient(url), &p, map[string][]loop.Chair{"gate": {{Provider: "claude_code", Profile: "km-claude"}}})
}

func TestAProfileReplacementIsRefusedNamingTheProfileAndTheLiveRunThatMayDependOnIt(t *testing.T) {
	t.Setenv(loop.StateEnv, t.TempDir())
	args, _ := fakeProfileInstall(t)
	home := os.Getenv("CAO_HOME_DIR")
	writeFile(t, filepath.Join(home, "agent-store/km-claude-high.md"), "keep original\n")
	s := statusFixture(t, filepath.Join(loop.StateDir(), "runs"), "conducting")
	child := statusChild(t)
	s.PID = child.Process.Pid
	var err error
	s.ProcessStart, err = processStart(s.PID)
	if err != nil {
		t.Fatal(err)
	}
	saveStatusFixture(t, filepath.Join(loop.StateDir(), "runs"), s.RunID, s)
	err = installSafetyProfiles(safetyProfiles(t, home, 0).URL)
	if err == nil || !strings.Contains(err.Error(), "km-claude-high") || !strings.Contains(err.Error(), s.RunID) {
		t.Fatalf("refusal = %v", err)
	}
	if _, err := os.Stat(args); !os.IsNotExist(err) {
		t.Fatalf("installer was called: %v", err)
	}
	if _, err := os.Stat(filepath.Join(loop.StateDir(), "profiles")); !os.IsNotExist(err) {
		t.Fatalf("variant was written: %v", err)
	}
	if data, _ := os.ReadFile(filepath.Join(home, "agent-store/km-claude-high.md")); string(data) != "keep original\n" {
		t.Fatalf("changed original: %q", data)
	}
}

func TestAProfileReplacementIsAllowedWhenNoOtherRecordedRunIsAlive(t *testing.T) {
	for _, kind := range []string{"self", "finished", "abandoned", "unknown", "missing"} {
		t.Run(kind, func(t *testing.T) {
			t.Setenv(loop.StateEnv, t.TempDir())
			args, _ := fakeProfileInstall(t)
			home := os.Getenv("CAO_HOME_DIR")
			writeFile(t, filepath.Join(home, "agent-store/km-claude-high.md"), "old\n")
			if kind != "missing" {
				root := filepath.Join(loop.StateDir(), "runs")
				s := statusFixture(t, root, "other")
				if kind != "self" {
					child := statusChild(t)
					s.PID = child.Process.Pid
					s.ProcessStart, _ = processStart(s.PID)
					if kind == "abandoned" {
						_ = child.Process.Kill()
						_ = child.Wait()
					}
				}
				if kind == "unknown" {
					s.Host = "another host"
				}
				saveStatusFixture(t, root, s.RunID, s)
				if kind == "finished" {
					writeFile(t, filepath.Join(root, s.RunID, "result.json"), `{"run_id":"other","outcome":"approved"}`)
				}
			}
			if err := installSafetyProfiles(safetyProfiles(t, home, 0).URL); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(args); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestAProfileThatDoesNotExistYetOrIsUnchangedIsInstalledWhileARunIsAlive(t *testing.T) {
	for _, same := range []bool{false, true} {
		t.Run(fmt.Sprint(same), func(t *testing.T) {
			t.Setenv(loop.StateEnv, t.TempDir())
			args, _ := fakeProfileInstall(t)
			home := os.Getenv("CAO_HOME_DIR")
			s := statusFixture(t, filepath.Join(loop.StateDir(), "runs"), "other")
			child := statusChild(t)
			s.PID = child.Process.Pid
			s.ProcessStart, _ = processStart(s.PID)
			saveStatusFixture(t, filepath.Join(loop.StateDir(), "runs"), s.RunID, s)
			if same {
				source, err := loop.EffortProfileSource("claude_code", "km-claude", "high", loop.Permissions{AllowedTools: []string{}})
				if err != nil {
					t.Fatal(err)
				}
				writeFile(t, filepath.Join(home, "agent-store/km-claude-high.md"), source)
			}
			if err := installSafetyProfiles(safetyProfiles(t, home, 0).URL); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(args); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestInstallingIsRefusedWhenTheCAOServerDoesNotWriteIntoTheCAOHomeCaoInstallWritesTo(t *testing.T) {
	t.Setenv(loop.StateEnv, t.TempDir())
	args, _ := fakeProfileInstall(t)
	serverHome := t.TempDir()
	srv := safetyProfiles(t, serverHome, 0)
	err := installSafetyProfiles(srv.URL)
	if err == nil || !strings.Contains(err.Error(), srv.URL) || !strings.Contains(err.Error(), os.Getenv("CAO_HOME_DIR")) {
		t.Fatalf("refusal = %v", err)
	}
	if _, err := os.Stat(args); !os.IsNotExist(err) {
		t.Fatalf("installer called: %v", err)
	}
	if _, err := os.Stat(filepath.Join(loop.StateDir(), "profiles")); !os.IsNotExist(err) {
		t.Fatalf("variant written: %v", err)
	}
	probes, _ := filepath.Glob(filepath.Join(serverHome, "agent-store/km-probe-*.md"))
	if len(probes) != 0 {
		t.Fatalf("probe remains: %v", probes)
	}
}

func TestInstallingIsRefusedWhenTheProfileProbeRequestFails(t *testing.T) {
	for _, code := range []int{400, 403, 404, 409} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			t.Setenv(loop.StateEnv, t.TempDir())
			args, _ := fakeProfileInstall(t)
			err := installSafetyProfiles(safetyProfiles(t, os.Getenv("CAO_HOME_DIR"), code).URL)
			if err == nil || !strings.Contains(err.Error(), "cannot establish") {
				t.Fatalf("refusal = %v", err)
			}
			if _, err := os.Stat(args); !os.IsNotExist(err) {
				t.Fatalf("installer called: %v", err)
			}
		})
	}
}

func TestEveryPendingProfileIsCheckedBeforeAnyVariantIsWrittenOrInstalled(t *testing.T) {
	t.Setenv(loop.StateEnv, t.TempDir())
	args, _ := fakeProfileInstall(t)
	home := os.Getenv("CAO_HOME_DIR")
	s := statusFixture(t, filepath.Join(loop.StateDir(), "runs"), "conducting")
	child := statusChild(t)
	s.PID = child.Process.Pid
	s.ProcessStart, _ = processStart(s.PID)
	saveStatusFixture(t, filepath.Join(loop.StateDir(), "runs"), s.RunID, s)
	writeFile(t, filepath.Join(home, "agent-store/km-claude-xhigh.md"), "keep\n")
	p := loop.DefaultPolicy()
	err := ensureProfiles(context.Background(), cao.NewClient(safetyProfiles(t, home, 0).URL), &p, map[string][]loop.Chair{"implement": {{Provider: "claude_code", Profile: "km-claude"}}})
	if err == nil || !strings.Contains(err.Error(), "km-claude-xhigh") {
		t.Fatalf("refusal = %v", err)
	}
	if _, err := os.Stat(args); !os.IsNotExist(err) {
		t.Fatalf("installer called: %v", err)
	}
	if _, err := os.Stat(filepath.Join(loop.StateDir(), "profiles")); !os.IsNotExist(err) {
		t.Fatalf("variant directory written: %v", err)
	}
}

func TestTheHomeProbeRunsOnceForPendingInstallsAndNeverWhenNothingNeedsInstalling(t *testing.T) {
	for _, needed := range []bool{false, true} {
		t.Run(fmt.Sprint(needed), func(t *testing.T) {
			t.Setenv(loop.StateEnv, t.TempDir())
			fakeProfileInstall(t)
			home := os.Getenv("CAO_HOME_DIR")
			creates, deletes := 0, 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "POST" {
					creates++
				}
				if r.Method == "DELETE" {
					deletes++
				}
				if profileProbe(w, r, home) {
					return
				}
				if r.URL.Path == "/agents/profiles" {
					if needed {
						fmt.Fprint(w, `[]`)
					} else {
						fmt.Fprint(w, `[{"name":"km-claude-medium","loadable":true},{"name":"km-claude-high","loadable":true},{"name":"km-claude-xhigh","loadable":true}]`)
					}
					return
				}
				fmt.Fprint(w, `{}`)
			}))
			defer srv.Close()
			p := loop.DefaultPolicy()
			if err := ensureProfiles(context.Background(), cao.NewClient(srv.URL), &p, map[string][]loop.Chair{"implement": {{Provider: "claude_code", Profile: "km-claude"}}}); err != nil {
				t.Fatal(err)
			}
			want := 0
			if needed {
				want = 1
			}
			if creates != want || deletes != want {
				t.Fatalf("probe creates/deletes = %d/%d, want %d", creates, deletes, want)
			}
			probes, _ := filepath.Glob(filepath.Join(home, "agent-store/km-probe-*.md"))
			if len(probes) != 0 {
				t.Fatal(probes)
			}
		})
	}
}

func TestAFailedProbeDeletionRefusesInstallationAndNamesTheRemainingProfile(t *testing.T) {
	t.Setenv(loop.StateEnv, t.TempDir())
	args, _ := fakeProfileInstall(t)
	home := os.Getenv("CAO_HOME_DIR")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "DELETE" {
			http.Error(w, "cannot remove", 500)
			return
		}
		if profileProbe(w, r, home) {
			return
		}
		if r.URL.Path == "/agents/profiles" {
			fmt.Fprint(w, `[]`)
			return
		}
		fmt.Fprint(w, `{}`)
	}))
	defer srv.Close()
	err := installSafetyProfiles(srv.URL)
	probes, _ := filepath.Glob(filepath.Join(home, "agent-store/km-probe-*.md"))
	if len(probes) != 1 || err == nil || !strings.Contains(err.Error(), strings.TrimSuffix(filepath.Base(probes[0]), ".md")) {
		t.Fatalf("error=%v probes=%v", err, probes)
	}
	if _, err := os.Stat(args); !os.IsNotExist(err) {
		t.Fatalf("installer called: %v", err)
	}
}

func TestStatusAndTheProfileGuardAgreeOnWhichRunsAreAlive(t *testing.T) {
	for _, kind := range []string{"alive", "abandoned", "unknown"} {
		t.Run(kind, func(t *testing.T) {
			t.Setenv(loop.StateEnv, t.TempDir())
			home := t.TempDir()
			writeFile(t, filepath.Join(home, "agent-store/km-claude-high.md"), "original")
			root := filepath.Join(loop.StateDir(), "runs")
			s := statusFixture(t, root, "other")
			child := statusChild(t)
			s.PID = child.Process.Pid
			var err error
			s.ProcessStart, err = processStart(s.PID)
			if err != nil {
				t.Fatal(err)
			}
			if kind == "abandoned" {
				_ = child.Process.Kill()
				_ = child.Wait()
			}
			if kind == "unknown" {
				s.Host = "another host"
			}
			saveStatusFixture(t, root, s.RunID, s)
			rows := statusRows(t, root)
			if len(rows) != 1 || rows[0].Liveness != kind {
				t.Fatalf("status = %+v", rows)
			}
			refused := guardProfile(home, "km-claude-high", "replacement") != nil
			if refused != (rows[0].Liveness == "alive") {
				t.Fatalf("guard refusal = %t; status = %s", refused, rows[0].Liveness)
			}
		})
	}
}

func TestAConnectionFailureDuringTheHomeProbeRefusesInstallation(t *testing.T) {
	t.Setenv(loop.StateEnv, t.TempDir())
	args, _ := fakeProfileInstall(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	server.Close()
	err := proveCAOHome(context.Background(), cao.NewClient(server.URL), os.Getenv("CAO_HOME_DIR"), "km-claude-high")
	if err == nil || !strings.Contains(err.Error(), "cannot establish") || !strings.Contains(err.Error(), server.URL) {
		t.Fatalf("probe error = %v", err)
	}
	if _, err := os.Stat(args); !os.IsNotExist(err) {
		t.Fatalf("installer called: %v", err)
	}
}
