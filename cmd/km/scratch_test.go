package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jystringfellow/kapellmeister/internal/loop"
)

func requireNoScratchLeft(t *testing.T) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(loop.StateDir(), "scratch"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("scratch directories left behind: %v", entries)
	}
}

func TestRunAndPlayRemoveTheirScratchDirectoryWhenTheRunEndsApprovedOrFailed(t *testing.T) {
	for _, command := range []string{"run", "play"} {
		for _, verify := range []string{"true", "false"} {
			t.Run(command+"/verify "+verify, func(t *testing.T) {
				var err error
				if command == "run" {
					f := &runCAO{}
					wt, args := runSessionArgs(t, f)
					writeFile(t, filepath.Join(wt, loop.ConfigFile), "[test]\nverify = '"+verify+"'\n[run]\npreflight_timeout = '0'\nreply_grace = '1ms'\nrounds = 2\n[sizing]\nmode = 'off'\n")
					_, err = captureStdout(t, func() error { return runCommand(args) })
				} else {
					s := safetyStanding(t)
					_, err = capturePlay(t, []string{s.Session, "-verify", verify, "-cao", (&playCAO{}).serve(t).URL})
				}
				if (err == nil) != (verify == "true") {
					t.Fatalf("verify %s: error = %v", verify, err)
				}
				requireNoScratchLeft(t)
			})
		}
	}
}

func TestTuneRemovesItsScratchDirectoryAndKeepsTheBreakPatchesInTheRunRecord(t *testing.T) {
	root := tuneFixture(t)
	f := &tuneCAO{}
	if code, out := tuneCall(t, "verify", "-worktree", root, "-cao", f.serve(t).URL); code != 0 {
		t.Fatal(code, out)
	}
	requireNoScratchLeft(t)
	patches, err := filepath.Glob(filepath.Join(loop.StateDir(), "runs", "*", "breaks", "*.patch"))
	if err != nil || len(patches) < 2 {
		t.Errorf("break patches in the run record = %v, %v", patches, err)
	}
}

func TestARunLowOnDiskStopsBeforeSeatingAnyChairAndNamesTheDirectoryFreeSpaceAndRequirement(t *testing.T) {
	low := func(t *testing.T) {
		original := loop.FreeBytes
		loop.FreeBytes = func(string) (uint64, error) { return 10 << 20, nil }
		t.Cleanup(func() { loop.FreeBytes = original })
	}
	requireLowDisk := func(t *testing.T, text string) {
		t.Helper()
		for _, want := range []string{"low disk: ", " has 10 MB free and this run needs 1024 MB ([run] min_free_mb); free some space or lower the setting"} {
			if !strings.Contains(text, want) {
				t.Errorf("message lacks %q: %s", want, text)
			}
		}
		requireNoScratchLeft(t)
		if _, err := os.Stat(filepath.Join(loop.StateDir(), "runs")); !os.IsNotExist(err) {
			t.Errorf("a run record was started: %v", err)
		}
	}
	t.Run("run", func(t *testing.T) {
		f := &runCAO{}
		_, args := runSessionArgs(t, f)
		low(t)
		_, err := captureStdout(t, func() error { return runCommand(args) })
		if err == nil {
			t.Fatal("run started with 10 MB free")
		}
		requireLowDisk(t, err.Error())
		if len(f.created) != 0 || len(f.fake.requests) != 0 {
			t.Errorf("chairs were seated: %+v %+v", f.created, f.fake.requests)
		}
	})
	t.Run("play", func(t *testing.T) {
		s := safetyStanding(t)
		fake := &playCAO{}
		low(t)
		_, err := capturePlay(t, []string{s.Session, "-cao", fake.serve(t).URL})
		if err == nil {
			t.Fatal("play started with 10 MB free")
		}
		requireLowDisk(t, err.Error())
		if len(fake.requests) != 0 {
			t.Errorf("chairs were cued: %+v", fake.requests)
		}
	})
	t.Run("tune", func(t *testing.T) {
		root := tuneFixture(t)
		f := &tuneCAO{}
		low(t)
		code, out := tuneCall(t, "verify", "-worktree", root, "-cao", f.serve(t).URL)
		if code == 0 {
			t.Fatal("tune started with 10 MB free")
		}
		requireLowDisk(t, out)
		if len(f.seated) != 0 || len(f.steps) != 0 {
			t.Errorf("chairs were seated: %v %v", f.seated, f.steps)
		}
	})
	t.Run("off at zero", func(t *testing.T) {
		f := &runCAO{}
		wt, args := runSessionArgs(t, f)
		writeFile(t, filepath.Join(wt, loop.ConfigFile), "[test]\nverify = 'true'\n[run]\nmin_free_mb = 0\npreflight_timeout = '0'\nreply_grace = '1ms'\nrounds = 2\n[sizing]\nmode = 'off'\n")
		low(t)
		if _, err := captureStdout(t, func() error { return runCommand(args) }); err != nil {
			t.Fatal(err)
		}
	})
}
