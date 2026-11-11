package main

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jystringfellow/kapellmeister/internal/loop"
)

func shortTesterScratch(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "km-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func sourcedTesterEnv(t *testing.T, path string) (map[string]string, error, string) {
	t.Helper()
	c := exec.Command("sh", "-c", `. "$1" || exit; env`, "tester", path)
	c.Env = append(os.Environ(), "TMUX=/conductor/socket,1,0", "TMUX_PANE=%9", "CAO_TERMINAL_ID=conductor", "CAO_HOME_DIR=/conductor/home", "CAO_API_PORT=9889", "CAO_API_BASE_URL=http://127.0.0.1:9889", "KAPELLMEISTER_HOME=/conductor/km", "KAPELLMEISTER_STATE_DIR=/conductor/state")
	out, err := c.CombinedOutput()
	values := map[string]string{}
	for _, line := range strings.Split(string(out), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if ok {
			switch key {
			case "KAPELLMEISTER_HOME", "KAPELLMEISTER_STATE_DIR", "CAO_HOME_DIR", "TMUX_TMPDIR", "CAO_API_HOST", "CAO_API_PORT", "CAO_API_BASE_URL", "TMUX", "TMUX_PANE", "CAO_TERMINAL_ID":
				values[key] = value
			}
		}
	}
	return values, err, string(out)
}

func TestTheTesterEnvFileMovesKmCaoAndTmuxUnderTheScratchDirectory(t *testing.T) {
	scratch := shortTesterScratch(t)
	path, err := writeTesterEnv(scratch, "http://127.0.0.1:19889")
	if err != nil {
		t.Fatal(err)
	}
	values, err, out := sourcedTesterEnv(t, path)
	if err != nil {
		t.Fatalf("source: %v: %s", err, out)
	}
	for key, suffix := range map[string]string{"KAPELLMEISTER_HOME": "km-home", "KAPELLMEISTER_STATE_DIR": "km-state", "CAO_HOME_DIR": "cao-home", "TMUX_TMPDIR": "tmux"} {
		if values[key] != filepath.Join(scratch, suffix) {
			t.Errorf("%s = %q", key, values[key])
		}
	}
	for _, key := range []string{"TMUX", "TMUX_PANE", "CAO_TERMINAL_ID"} {
		if _, ok := values[key]; ok {
			t.Errorf("inherited %s", key)
		}
	}
	if values["CAO_API_HOST"] != "127.0.0.1" || values["CAO_API_PORT"] == "9889" || values["CAO_API_PORT"] == "19889" || values["CAO_API_PORT"] == "" || values["CAO_API_BASE_URL"] != "http://127.0.0.1:"+values["CAO_API_PORT"] {
		t.Fatalf("CAO env = %v", values)
	}
	if _, _, err := net.SplitHostPort(strings.TrimPrefix(values["CAO_API_BASE_URL"], "http://")); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"TMUX_TMPDIR", "KAPELLMEISTER_HOME", "KAPELLMEISTER_STATE_DIR"} {
		if info, err := os.Stat(values[key]); err != nil || !info.IsDir() {
			t.Fatalf("%s directory: %v", key, err)
		}
	}
	if info, err := os.Stat(values["TMUX_TMPDIR"]); err != nil || info.Mode().Perm() != 0700 {
		t.Fatalf("tmux permissions: %v %v", info, err)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0644 {
		t.Fatalf("env permissions: %v %v", info, err)
	}
}

func TestTheTesterEnvFileQuotesScratchPathsWithSpacesAndQuotes(t *testing.T) {
	scratch := filepath.Join(shortTesterScratch(t), "a ' quoted $path")
	if err := os.MkdirAll(scratch, 0700); err != nil {
		t.Fatal(err)
	}
	path, err := writeTesterEnv(scratch, "http://localhost:9889")
	if err != nil {
		t.Fatal(err)
	}
	values, err, out := sourcedTesterEnv(t, path)
	if err != nil {
		t.Fatalf("source: %v: %s", err, out)
	}
	if values["CAO_HOME_DIR"] != filepath.Join(scratch, "cao-home") {
		t.Fatal(values)
	}
}

func TestTheTesterEnvFileRefusesToBeSourcedWhenTheTmuxSocketPathWouldBeTooLong(t *testing.T) {
	scratch := filepath.Join(shortTesterScratch(t), strings.Repeat("x", 110))
	if err := os.MkdirAll(scratch, 0700); err != nil {
		t.Fatal(err)
	}
	path, err := writeTesterEnv(scratch, "http://localhost:9889")
	if err != nil {
		t.Fatal(err)
	}
	c := exec.Command("sh", "-c", `TMUX=conductor; KAPELLMEISTER_HOME=conductor; . "$1"; result=$?; printf '\n%s:%s:%s\n' "$result" "$TMUX" "$KAPELLMEISTER_HOME"`, "tester", path)
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("shell: %v: %s", err, out)
	}
	if !strings.Contains(string(out), "1:conductor:conductor") || !strings.Contains(string(out), "shorter KAPELLMEISTER_STATE_DIR") {
		t.Fatalf("refusal = %s", out)
	}
}

func TestRunAndPlayWriteTheTesterEnvFileOnlyWhenATesterIsSeated(t *testing.T) {
	for _, command := range []string{"run", "play"} {
		for _, tester := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/%t", command, tester), func(t *testing.T) {
				var err error
				var url string
				if command == "run" {
					f := &runCAO{}
					_, args := runSessionArgs(t, f)
					if !tester {
						for i := 0; i < len(args); i++ {
							if args[i] == "-tester" {
								args = append(args[:i], args[i+2:]...)
								break
							}
						}
					}
					for i, arg := range args {
						if arg == "-cao" {
							url = args[i+1]
						}
					}
					_, err = captureStdout(t, func() error { return runCommand(args) })
				} else {
					s := playStanding(t)
					if tester {
						s = safetyStanding(t)
					}
					url = (&playCAO{}).serve(t).URL
					_, err = capturePlay(t, []string{s.Session, "-cao", url})
				}
				if err != nil {
					t.Fatal(err)
				}
				dir, _ := onlyRunResult(t)
				scratch := filepath.Join(loop.StateDir(), "scratch", filepath.Base(dir))
				path := filepath.Join(scratch, "env.sh")
				_, statErr := os.Stat(path)
				if tester && statErr != nil || !tester && !os.IsNotExist(statErr) {
					t.Fatalf("tester %t env file: %v", tester, statErr)
				}
				data, err := os.ReadFile(filepath.Join(dir, "config.json"))
				if err != nil {
					t.Fatal(err)
				}
				if tester && (!strings.Contains(string(data), path) || !strings.Contains(string(data), url)) {
					t.Fatalf("config lacks environment: %s", data)
				}
			})
		}
	}
}
