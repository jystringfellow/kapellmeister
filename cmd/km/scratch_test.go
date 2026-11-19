package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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

func TestRunAndPlayRemoveScratchAfterAnApprovedRunAndKeepItSayingWhereAfterAnyOtherOutcome(t *testing.T) {
	for _, command := range []string{"run", "play"} {
		for _, approved := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/approved %t", command, approved), func(t *testing.T) {
				leave := func(string, string) {
					dirs, _ := filepath.Glob(filepath.Join(loop.StateDir(), "scratch", "*"))
					for _, dir := range dirs {
						writeFile(t, filepath.Join(dir, "evidence.txt"), "a tester's copy\n")
					}
				}
				var err error
				var said bytes.Buffer
				if command == "run" {
					f := &runCAO{fake: playCAO{afterReply: leave, changes: !approved}}
					wt, args := runSessionArgs(t, f)
					writeFile(t, filepath.Join(wt, loop.ConfigFile), "[test]\nverify = 'true'\n[run]\npreflight_timeout = '0'\nreply_grace = '1ms'\nrounds = 2\n[sizing]\nmode = 'off'\n")
					_, err = captureStdout(t, func() error { return runAt(nil, args, os.Stdout, &said, nil) })
				} else {
					s := safetyStanding(t)
					_, err = capturePlay(t, []string{s.Session, "-cao", (&playCAO{afterReply: leave, changes: !approved}).serve(t).URL})
				}
				if (err == nil) != approved {
					t.Fatalf("approved %t: error = %v", approved, err)
				}
				if approved {
					requireNoScratchLeft(t)
					if strings.Contains(said.String(), "scratch") {
						t.Errorf("an approved run mentioned scratch: %s", &said)
					}
					return
				}
				dir, _ := onlyRunResult(t)
				scratch := filepath.Join(loop.StateDir(), "scratch", filepath.Base(dir))
				if data, readErr := os.ReadFile(filepath.Join(scratch, "evidence.txt")); readErr != nil || string(data) != "a tester's copy\n" {
					t.Errorf("scratch after a run that was not approved = %q, %v", data, readErr)
				}
				if command == "run" && !strings.Contains(said.String(), "km: scratch was kept at "+scratch+"\n") {
					t.Errorf("the run did not say where scratch was kept: %s", &said)
				}
			})
		}
	}
}

func TestScratchIsKeptOnlyWhenTheRunWasNotApprovedAndSomethingIsInIt(t *testing.T) {
	for _, c := range []struct {
		name     string
		approved bool
		content  bool
		kept     bool
	}{
		{"approved with content", true, true, false},
		{"not approved with content", false, true, true},
		{"not approved and empty", false, false, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			state := t.TempDir()
			scratch, err := prepareScratch(state, "run-1")
			if err != nil {
				t.Fatal(err)
			}
			if c.content {
				writeFile(t, filepath.Join(scratch, "copy", "file.txt"), "evidence\n")
			}
			var said bytes.Buffer
			settleScratch(&said, state, "run-1", c.approved)
			_, statErr := os.Stat(scratch)
			if kept := statErr == nil; kept != c.kept {
				t.Errorf("kept = %t, want %t (%v)", kept, c.kept, statErr)
			}
			want := ""
			if c.kept {
				want = "km: scratch was kept at " + scratch + "\n"
			}
			if said.String() != want {
				t.Errorf("said %q, want %q", &said, want)
			}
		})
	}
}

func TestARunIDThatIsNotASinglePathElementIsRefusedAndNothingOutsideItsScratchIsRemoved(t *testing.T) {
	for _, id := range []string{"", ".", "..", "../runs", "a/b", "/abs"} {
		t.Run("id "+id, func(t *testing.T) {
			state := t.TempDir()
			t.Setenv(loop.StateEnv, state)
			t.Setenv(loop.HomeEnv, t.TempDir())
			t.Setenv(loop.CAOURLEnv, "http://127.0.0.1:1")
			other := filepath.Join(state, "scratch", "other-run", "copy.txt")
			record := filepath.Join(state, "runs", "earlier-run", "result.json")
			writeFile(t, other, "another run's copy\n")
			writeFile(t, record, "{}\n")
			survive := func(when string) {
				t.Helper()
				for _, path := range []string{other, record} {
					if _, err := os.Stat(path); err != nil {
						t.Errorf("%s: %v", when, err)
					}
				}
			}
			if _, err := prepareScratch(state, id); err == nil || !strings.Contains(err.Error(), "run id") {
				t.Errorf("prepareScratch accepted run id %q: %v", id, err)
			}
			survive("after preparing scratch")
			var said bytes.Buffer
			settleScratch(&said, state, id, true)
			if !strings.Contains(said.String(), "run id") {
				t.Errorf("removal accepted run id %q: %s", id, &said)
			}
			survive("after removing scratch")
			if err := loop.RemoveScratch(state, id); err == nil {
				t.Errorf("RemoveScratch accepted run id %q", id)
			}
			survive("after RemoveScratch")
			launch := filepath.Join(t.TempDir(), "launch.json")
			data, err := json.Marshal(detachedLaunchRecord{RunID: id, Worktree: state, Checkout: state, Task: "task"})
			if err != nil {
				t.Fatal(err)
			}
			writeFile(t, launch, string(data))
			for _, tune := range []bool{false, true} {
				var out, errout bytes.Buffer
				if tune {
					err = tuneVerify(context.Background(), []string{"-detached-launch", launch}, &out, &errout)
				} else {
					err = runAt(context.Background(), []string{"-detached-launch", launch}, &out, &errout, nil)
				}
				if err == nil || !strings.Contains(err.Error(), "detached launch") || !strings.Contains(err.Error(), "run id") {
					t.Errorf("tune %t: a launch file with run id %q gave %v", tune, id, err)
				}
				survive("after the detached launch")
			}
		})
	}
}

func TestAnUnreadableScratchIsKeptAndNamedWhenTheRunWasNotApproved(t *testing.T) {
	state := t.TempDir()
	scratch, err := prepareScratch(state, "run-1")
	if err != nil {
		t.Fatal(err)
	}
	kept := filepath.Join(scratch, "evidence.txt")
	if err := os.WriteFile(kept, []byte("kept"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(scratch, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(scratch, 0o700) })
	var said bytes.Buffer
	settleScratch(&said, state, "run-1", false)
	if err := os.Chmod(scratch, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(kept); err != nil || said.String() != "km: scratch was kept at "+scratch+"\n" {
		t.Fatal(err, said.String())
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
