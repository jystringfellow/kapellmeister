package loop

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func freeSpace(t *testing.T, free func(dir string) (uint64, error)) {
	t.Helper()
	original := FreeBytes
	FreeBytes = free
	t.Cleanup(func() { FreeBytes = original })
}

func TestATestersScratchCopiesAreRemovedAfterEachTestStepAndItsPidsFileIsKept(t *testing.T) {
	e, f := testerEngine(t)
	scratch := e.Config.ScratchDir
	copyDir := filepath.Join(scratch, "copy", "module")
	tests := 0
	f.actions["test"] = func(s Step) {
		tests++
		if entries, _ := os.ReadDir(filepath.Join(scratch, "copy")); len(entries) != 0 {
			t.Errorf("round %d began with the previous round's copies: %v", tests, entries)
		}
		if err := os.MkdirAll(copyDir, 0755); err != nil {
			t.Error(err)
		}
		if err := os.WriteFile(filepath.Join(copyDir, "file.txt"), []byte("copied"), 0444); err != nil {
			t.Error(err)
		}
		if err := os.Chmod(copyDir, 0555); err != nil {
			t.Error(err)
		}
		if err := os.WriteFile(filepath.Join(scratch, "pids"), []byte("999999999\n"), 0600); err != nil {
			t.Error(err)
		}
		f.write(s.ReplyPath, "test reply")
	}
	t.Cleanup(func() { _ = os.Chmod(copyDir, 0755) })
	f.verdicts["test"] = []string{"CHANGES", "APPROVE"}
	if res := run(t, e); res.Outcome != Approved || tests != 2 {
		t.Fatalf("result after %d test steps: %+v", tests, res)
	}
	entries, err := os.ReadDir(scratch)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "pids" {
		t.Errorf("scratch holds %v, want only pids", entries)
	}
}

func TestATestersScratchCopiesAreRemovedWhenTheTestStepFails(t *testing.T) {
	e, f := testerEngine(t)
	scratch := e.Config.ScratchDir
	f.actions["test"] = func(s Step) {
		if err := os.MkdirAll(filepath.Join(scratch, "copy"), 0755); err != nil {
			t.Error(err)
		}
	}
	f.errFor = func(s Step) error {
		if s.Name == "test" {
			return context.DeadlineExceeded
		}
		return nil
	}
	if res := run(t, e); res.Outcome != Untested {
		t.Fatalf("result: %+v", res)
	}
	if entries, _ := os.ReadDir(scratch); len(entries) != 0 {
		t.Errorf("scratch holds %v after a timed-out test step", entries)
	}
}

func TestARunThatFallsLowOnDiskBetweenRoundsFailsNamingTheDirectoryFreeSpaceAndRequirement(t *testing.T) {
	e, f, _ := newEngine(t, passing)
	e.Config.MinFreeMB = 100
	free := uint64(500 << 20)
	freeSpace(t, func(string) (uint64, error) { return free, nil })
	f.actions["gate"] = func(Step) { free = 20 << 20 }
	f.verdicts["gate"] = []string{"CHANGES", "APPROVE"}
	res := run(t, e)
	if res.Outcome != Failed || res.Rounds != 1 {
		t.Fatalf("result: %+v", res)
	}
	for _, want := range []string{"low disk", e.Config.Worktree, "20 MB free", "100 MB", "min_free_mb"} {
		if !strings.Contains(res.Reason, want) {
			t.Errorf("reason lacks %q: %s", want, res.Reason)
		}
	}
	for _, name := range f.names() {
		if name == "implement[2]" {
			t.Errorf("a step started after disk ran low: %v", f.names())
		}
	}
}

func TestLowDiskNamesTheFirstDirectoryBelowTheRequirementAndIsOffAtZero(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "not", "yet", "made")
	var asked []string
	freeSpace(t, func(d string) (uint64, error) {
		asked = append(asked, d)
		return 5 << 20, nil
	})
	err := LowDisk(64, missing)
	want := "low disk: " + dir + " has 5 MB free and this run needs 64 MB ([run] min_free_mb); free some space or lower the setting"
	if err == nil || err.Error() != want {
		t.Fatalf("error = %v, want %s", err, want)
	}
	if len(asked) != 1 || asked[0] != dir {
		t.Errorf("asked about %v, want the nearest existing directory %s", asked, dir)
	}
	if err := LowDisk(0, dir); err != nil {
		t.Errorf("0 must turn the check off: %v", err)
	}
	if err := LowDisk(5, dir); err != nil {
		t.Errorf("exactly the required space is enough: %v", err)
	}
}

func TestFreeSpaceThatCannotBeMeasuredDoesNotStopARun(t *testing.T) {
	freeSpace(t, func(string) (uint64, error) { return 0, os.ErrPermission })
	if err := LowDisk(64, t.TempDir()); err != nil {
		t.Errorf("unmeasurable free space stopped the run: %v", err)
	}
}

func TestMinFreeMBDefaultsToOneGibibyteAndCanBeSetOrTurnedOff(t *testing.T) {
	if got := DefaultSettings().MinFreeMB; got != 1024 {
		t.Errorf("default = %d", got)
	}
	for _, c := range []struct {
		body string
		want int
		fail string
	}{
		{"[run]\nmin_free_mb = 0\n", 0, ""},
		{"[run]\nmin_free_mb = 4096\n", 4096, ""},
		{"[run]\nmin_free_mb = -1\n", 0, "run.min_free_mb: must be 0 (off) or more"},
	} {
		home, wt := t.TempDir(), t.TempDir()
		if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(c.body), 0644); err != nil {
			t.Fatal(err)
		}
		s, err := LoadSettings(home, wt)
		if c.fail != "" {
			if err == nil || !strings.Contains(err.Error(), c.fail) {
				t.Errorf("%q: error = %v", c.body, err)
			}
			continue
		}
		if err != nil || s.MinFreeMB != c.want {
			t.Errorf("%q: got %d, %v", c.body, s.MinFreeMB, err)
		}
	}
}
