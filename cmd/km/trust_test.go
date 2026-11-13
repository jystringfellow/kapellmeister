package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jystringfellow/kapellmeister/internal/cao"
	"github.com/jystringfellow/kapellmeister/internal/loop"
)

func TestKmConfigShowsTrustFolder(t *testing.T) {
	for _, on := range []bool{false, true} {
		t.Run(fmt.Sprint(on), func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("KAPELLMEISTER_HOME", home)
			writeFile(t, loop.UserConfigPath(home), fmt.Sprintf("[agents]\navailable=['codex']\n[run]\ntrust_folder = %t\n", on))
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`[]`)) }))
			defer srv.Close()
			out, err := captureStdout(t, func() error { return configCommand([]string{"-worktree", t.TempDir(), "-cao", srv.URL}) })
			want := "trust folder off"
			if on {
				want = "trust folder on"
			}
			if err != nil || !strings.Contains(out, want) {
				t.Fatal(out, err)
			}
		})
	}
}
func TestPreflightWithTrustFolderOnRecordsCodexAndOffDoesNot(t *testing.T) {
	for _, on := range []bool{false, true} {
		t.Run(fmt.Sprint(on), func(t *testing.T) {
			root := tuneFixture(t)
			home := t.TempDir()
			t.Setenv("KAPELLMEISTER_HOME", home)
			codexHome := t.TempDir()
			t.Setenv("CODEX_HOME", codexHome)
			writeFile(t, loop.UserConfigPath(home), fmt.Sprintf("[agents]\navailable=['codex']\n[run]\ntrust_folder=%t\n", on))
			srv := (&tuneCAO{mode: "timeout"}).serve(t)
			out, _ := captureStdout(t, func() error {
				return preflightCommand([]string{"-worktree", root, "-agents", "codex", "-cao", srv.URL, "-timeout", "1s"})
			})
			data, err := os.ReadFile(filepath.Join(codexHome, "config.toml"))
			if on {
				if err != nil || !strings.Contains(string(data), "trust_level = \"trusted\"") || strings.Count(out, "trust: recorded") != 1 {
					t.Fatal(string(data), out, err)
				}
			} else if !os.IsNotExist(err) || strings.Contains(out, "trust:") {
				t.Fatal(string(data), out, err)
			}
		})
	}
}
func TestDisabledTrustFolderLeavesTheLaunchHookUnset(t *testing.T) {
	if newClient("", loop.DefaultSettings(), os.Stdout).BeforeLaunch != nil {
		t.Fatal("hook set while off")
	}
}
func TestTuneReportsASecondAttemptThatPasses(t *testing.T) {
	dir := t.TempDir()
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST" && r.URL.Path == "/sessions":
			fmt.Fprint(w, `{"id":"t","session_name":"s"}`)
		case r.URL.Path == "/terminals/run-step":
			calls++
			if calls == 1 {
				w.WriteHeader(504)
				fmt.Fprint(w, preflightTimeoutBody)
			} else {
				writeFile(t, filepath.Join(dir, loop.RunDirRel("preflight"), "0-codex.txt"), "ready")
				fmt.Fprint(w, `{"terminal_id":"t","status":"completed"}`)
			}
		default:
			fmt.Fprint(w, `{}`)
		}
	}))
	defer srv.Close()
	var out strings.Builder
	if err := tuneSeat(context.Background(), cao.NewClient(srv.URL), dir, time.Second, &out, loop.Chair{Provider: "codex"}); err != nil || !strings.Contains(out.String(), "second attempt passed") {
		t.Fatal(out.String(), err)
	}
}

func TestRunWithTrustFolderOnRecordsCodexAndOffDoesNot(t *testing.T) {
	for _, on := range []bool{false, true} {
		t.Run(fmt.Sprint(on), func(t *testing.T) {
			f := &runCAO{}
			_, args := runSessionArgs(t, f)
			codexHome := t.TempDir()
			t.Setenv("CODEX_HOME", codexHome)
			before := "# keep this byte-for-byte\nmodel='gpt-6-luna'\n"
			writeFile(t, filepath.Join(codexHome, "config.toml"), before)
			writeFile(t, loop.UserConfigPath(loop.Home()), fmt.Sprintf("[run]\ntrust_folder=%t\n[tiers.codex]\nsmall={effort='inherit'}\nmedium={effort='inherit'}\nlarge={effort='inherit'}\n", on))
			for i, arg := range args {
				if strings.HasPrefix(arg, "p:") {
					args[i] = strings.ReplaceAll(arg, "p:", "codex:")
				}
			}
			out, err := captureStdout(t, func() error { return runCommand(args) })
			if err != nil {
				t.Fatal(out, err)
			}
			data, err := os.ReadFile(filepath.Join(codexHome, "config.toml"))
			if err != nil {
				t.Fatal(err)
			}
			if on {
				if !strings.HasPrefix(string(data), before) || strings.Count(string(data), "trust_level") != 1 || strings.Count(out, "trust: recorded") != 1 {
					t.Fatal(string(data), out)
				}
			} else if string(data) != before || strings.Contains(out, "trust: recorded") {
				t.Fatal(string(data), out)
			}
		})
	}
}
