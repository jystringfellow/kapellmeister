package main

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func writeTesterEnv(scratch, conductorURL string) (string, error) {
	path := filepath.Join(scratch, "env.sh")
	socket := filepath.Join(scratch, "tmux", fmt.Sprintf("tmux-%d", os.Getuid()), "default")
	if len(socket) > 100 {
		message := "tester scratch path is too long for a tmux socket; use a shorter KAPELLMEISTER_STATE_DIR"
		fmt.Fprintln(os.Stderr, "km: "+message)
		return path, os.WriteFile(path, []byte("printf '%s\\n' "+testerShellQuote(message)+" >&2\nreturn 1\n"), 0644)
	}
	conductorPort := "9889"
	if parsed, err := url.Parse(conductorURL); err == nil && parsed.Port() != "" {
		conductorPort = parsed.Port()
	}
	var port string
	for {
		listener, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			return "", err
		}
		port = strconv.Itoa(listener.Addr().(*net.TCPAddr).Port)
		if err := listener.Close(); err != nil {
			return "", err
		}
		if port != "9889" && port != conductorPort {
			break
		}
	}
	var source strings.Builder
	source.WriteString("unset TMUX TMUX_PANE CAO_TERMINAL_ID\n")
	for _, value := range []struct{ key, value string }{
		{"KAPELLMEISTER_HOME", filepath.Join(scratch, "km-home")},
		{"KAPELLMEISTER_STATE_DIR", filepath.Join(scratch, "km-state")},
		{"CAO_HOME_DIR", filepath.Join(scratch, "cao-home")},
		{"TMUX_TMPDIR", filepath.Join(scratch, "tmux")},
		{"CAO_API_HOST", "127.0.0.1"},
		{"CAO_API_PORT", port},
		{"CAO_API_BASE_URL", "http://127.0.0.1:" + port},
	} {
		fmt.Fprintf(&source, "export %s=%s\n", value.key, testerShellQuote(value.value))
	}
	source.WriteString("mkdir -p \"$TMUX_TMPDIR\" \"$KAPELLMEISTER_HOME\" \"$KAPELLMEISTER_STATE_DIR\" \"$CAO_HOME_DIR\" || return 1\nchmod 700 \"$TMUX_TMPDIR\" || return 1\n")
	return path, os.WriteFile(path, []byte(source.String()), 0644)
}

func testerShellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}
