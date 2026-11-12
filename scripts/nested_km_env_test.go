package scripts

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func shells(t *testing.T) []string {
	t.Helper()
	found := []string{"sh"}
	for _, shell := range []string{"dash", "bash"} {
		if _, err := exec.LookPath(shell); err == nil {
			found = append(found, shell)
		}
	}
	return found
}

func shortScratch(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "km-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func nestedKmEnv(t *testing.T, shell string, args ...string) (string, string, error) {
	t.Helper()
	script, err := filepath.Abs("nested-km-env")
	if err != nil {
		t.Fatal(err)
	}
	c := exec.Command(shell, append([]string{script}, args...)...)
	var stdout, stderr bytes.Buffer
	c.Stdout, c.Stderr = &stdout, &stderr
	err = c.Run()
	return strings.TrimSuffix(stdout.String(), "\n"), stderr.String(), err
}

func sourcedEnv(t *testing.T, shell, path string) map[string]string {
	t.Helper()
	c := exec.Command(shell, "-c", `. "$1" || exit; env`, "chair", path)
	c.Env = append(os.Environ(), "TMUX=/conductor/socket,1,0", "TMUX_PANE=%9", "CAO_TERMINAL_ID=conductor", "CAO_HOME_DIR=/conductor/home", "CAO_API_HOST=0.0.0.0", "CAO_API_PORT=9889", "CAO_API_BASE_URL=http://127.0.0.1:9889", "KAPELLMEISTER_HOME=/conductor/km", "KAPELLMEISTER_STATE_DIR=/conductor/state", "TMUX_TMPDIR=/conductor/tmux")
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("source: %v: %s", err, out)
	}
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
	return values
}

func TestTheEnvFileMovesKmCaoAndTmuxUnderTheScratchDirectoryAndClearsWhatTheChairInherited(t *testing.T) {
	for _, shell := range shells(t) {
		t.Run(shell, func(t *testing.T) {
			scratch := shortScratch(t)
			path, stderr, err := nestedKmEnv(t, shell, scratch, "http://127.0.0.1:19889")
			if err != nil {
				t.Fatalf("%v: %s", err, stderr)
			}
			if path != filepath.Join(scratch, "n", "env.sh") {
				t.Fatalf("printed %q", path)
			}
			values := sourcedEnv(t, shell, path)
			directories := map[string]string{"KAPELLMEISTER_HOME": "km-home", "KAPELLMEISTER_STATE_DIR": "km-state", "CAO_HOME_DIR": "cao-home", "TMUX_TMPDIR": "t"}
			for key, name := range directories {
				if values[key] != filepath.Join(scratch, "n", name) {
					t.Errorf("%s = %q", key, values[key])
				}
				if info, err := os.Stat(values[key]); err != nil || !info.IsDir() {
					t.Errorf("%s directory: %v", key, err)
				}
			}
			for _, key := range []string{"TMUX", "TMUX_PANE", "CAO_TERMINAL_ID"} {
				if value, ok := values[key]; ok {
					t.Errorf("inherited %s=%s", key, value)
				}
			}
			port, err := strconv.Atoi(values["CAO_API_PORT"])
			if err != nil || port < 1 || port > 65535 || port == 9889 || port == 19889 {
				t.Fatalf("CAO_API_PORT = %q", values["CAO_API_PORT"])
			}
			if values["CAO_API_HOST"] != "127.0.0.1" || values["CAO_API_BASE_URL"] != fmt.Sprintf("http://127.0.0.1:%d", port) {
				t.Fatalf("CAO env = %v", values)
			}
			if info, err := os.Stat(values["TMUX_TMPDIR"]); err != nil || info.Mode().Perm() != 0700 {
				t.Fatalf("tmux permissions: %v %v", info, err)
			}
		})
	}
}

func TestTheEnvFileQuotesScratchPathsWithSpacesQuotesAndDollars(t *testing.T) {
	for _, shell := range shells(t) {
		t.Run(shell, func(t *testing.T) {
			scratch := filepath.Join(shortScratch(t), "a ' q \"$HOME` \\")
			path, stderr, err := nestedKmEnv(t, shell, scratch, "http://localhost:9889")
			if err != nil {
				t.Fatalf("%v: %s", err, stderr)
			}
			values := sourcedEnv(t, shell, path)
			for key, name := range map[string]string{"KAPELLMEISTER_HOME": "km-home", "KAPELLMEISTER_STATE_DIR": "km-state", "CAO_HOME_DIR": "cao-home", "TMUX_TMPDIR": "t"} {
				if values[key] != filepath.Join(scratch, "n", name) {
					t.Errorf("%s = %q", key, values[key])
				}
			}
		})
	}
}

func TestATmuxSocketPathOver100BytesIsRefusedAndNoEnvFileIsWritten(t *testing.T) {
	for _, shell := range shells(t) {
		t.Run(shell, func(t *testing.T) {
			root := shortScratch(t)
			fits := len(filepath.Join(root, "x", "n", "t", fmt.Sprintf("tmux-%d", os.Getuid()), "default"))
			longest := filepath.Join(root, strings.Repeat("x", 1+100-fits))
			if _, stderr, err := nestedKmEnv(t, shell, longest); err != nil {
				t.Fatalf("a 100-byte socket path was refused: %v: %s", err, stderr)
			}
			tooLong := longest + "x"
			stdout, stderr, err := nestedKmEnv(t, shell, tooLong)
			if err == nil || stdout != "" || !strings.Contains(stderr, "101 bytes") || !strings.Contains(stderr, "shorter scratch directory") {
				t.Fatalf("refusal: err %v, stdout %q, stderr %q", err, stdout, stderr)
			}
			if _, err := os.Stat(filepath.Join(tooLong, "n", "env.sh")); !os.IsNotExist(err) {
				t.Fatalf("env file: %v", err)
			}
		})
	}
}

func TestSocketPathLengthIsCountedInBytesNotCharacters(t *testing.T) {
	root := shortScratch(t)
	fits := len(filepath.Join(root, "n", "t", fmt.Sprintf("tmux-%d", os.Getuid()), "default"))
	scratch := filepath.Join(root, strings.Repeat("é", (102-fits)/2))
	c := exec.Command("sh", "nested-km-env", scratch)
	c.Env = append(os.Environ(), "LC_ALL=en_US.UTF-8")
	if out, err := c.CombinedOutput(); err == nil || !strings.Contains(string(out), "shorter scratch directory") {
		t.Fatalf("err %v: %s", err, out)
	}
}

func TestARefusalRemovesAnEnvFileLeftByAnEarlierCall(t *testing.T) {
	scratch := shortScratch(t)
	path, stderr, err := nestedKmEnv(t, "sh", scratch)
	if err != nil {
		t.Fatalf("%v: %s", err, stderr)
	}
	tools := t.TempDir()
	for _, tool := range []string{"sh", "rm", "id", "wc", "sed", "mkdir", "mv"} {
		found, err := exec.LookPath(tool)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(found, filepath.Join(tools, tool)); err != nil {
			t.Fatal(err)
		}
	}
	c := exec.Command(filepath.Join(tools, "sh"), "nested-km-env", scratch)
	c.Env = []string{"PATH=" + tools}
	out, err := c.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "cannot bind a free port on 127.0.0.1 with python3 or perl") {
		t.Fatalf("err %v: %s", err, out)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("env file: %v", err)
	}
}

func TestARelativeScratchDirectoryIsResolvedAgainstTheWorkingDirectory(t *testing.T) {
	scratch := shortScratch(t)
	script, err := filepath.Abs("nested-km-env")
	if err != nil {
		t.Fatal(err)
	}
	c := exec.Command("sh", script, "relative/")
	c.Dir = scratch
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	resolved, err := filepath.EvalSymlinks(scratch)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(out)); got != filepath.Join(resolved, "relative", "n", "env.sh") && got != filepath.Join(scratch, "relative", "n", "env.sh") {
		t.Fatalf("printed %q", got)
	}
}

func TestAMissingScratchDirectoryArgumentIsAUsageError(t *testing.T) {
	stdout, stderr, err := nestedKmEnv(t, "sh")
	if err == nil || stdout != "" || !strings.Contains(stderr, "usage: nested-km-env <scratch directory> [conductor CAO URL]") {
		t.Fatalf("err %v, stdout %q, stderr %q", err, stdout, stderr)
	}
}
