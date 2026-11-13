package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunAndTuneVerifyHelpDoNotListInternalHandOffFlags(t *testing.T) {
	for _, tune := range []bool{false, true} {
		t.Run(map[bool]string{false: "run", true: "tune"}[tune], func(t *testing.T) {
			var out, errout bytes.Buffer
			var err error
			if tune {
				err = tuneVerify(context.Background(), []string{"-h"}, &out, &errout)
			} else {
				err = runAt(context.Background(), []string{"-h"}, &out, &errout, nil)
			}
			if !errors.Is(err, flag.ErrHelp) {
				t.Fatalf("%v", err)
			}
			if strings.Contains(errout.String(), "detached-launch") {
				t.Fatalf("internal flag visible: %s", &errout)
			}
			if !strings.Contains(errout.String(), "-detach") {
				t.Fatalf("public flag missing: %s", &errout)
			}
		})
	}
}

func TestHiddenHandOffFlagsStillParse(t *testing.T) {
	for _, tune := range []bool{false, true} {
		t.Run(map[bool]string{false: "run", true: "tune"}[tune], func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "missing-launch.json")
			var out, errout bytes.Buffer
			var err error
			if tune {
				err = tuneVerify(context.Background(), []string{"-detached-launch", path}, &out, &errout)
			} else {
				err = runAt(context.Background(), []string{"-detached-launch", path}, &out, &errout, nil)
			}
			if !errors.Is(err, os.ErrNotExist) || !strings.Contains(err.Error(), path) {
				t.Fatalf("%v; %s", err, &errout)
			}
		})
	}
}
