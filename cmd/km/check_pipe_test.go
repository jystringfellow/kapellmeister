package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestCheckCommandsRefuseANamedPipeWithoutBlocking(t *testing.T) {
	for _, kind := range []string{"ensemble", "score"} {
		t.Run(kind, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "sample.toml")
			if err := syscall.Mkfifo(path, 0600); err != nil {
				t.Fatal(err)
			}
			done := make(chan string, 1)
			go func() {
				var out, diag bytes.Buffer
				_ = checkCommand(kind, []string{path}, nil, &out, &diag, "")
				done <- out.String()
			}()
			select {
			case out := <-done:
				if !strings.Contains(out, path+": named pipe is not a regular file") {
					t.Fatal(out)
				}
			case <-time.After(200 * time.Millisecond):
				t.Fatal("check blocked on named pipe")
			}
		})
	}
}
