package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jystringfellow/kapellmeister/internal/loop"
	"golang.org/x/sys/unix"
)

func TestMain(m *testing.M) {
	if os.Getenv("KM_TEST_AS_KM") == "1" {
		if os.Getenv("KM_TEST_REFUSE_RECURSION") == "1" && strings.Contains(strings.Join(os.Args[1:], " "), "-detached-launch") {
			for _, arg := range os.Args[1:] {
				if arg == "-detached-launch" {
					break
				}
				if arg == "--" {
					fmt.Fprintln(os.Stderr, "detached hand-off must be parsed before the flag terminator")
					os.Exit(1)
				}
			}
		}
		os.Exit(execute(os.Args[1:], os.Stderr))
	}
	os.Exit(m.Run())
}

func kmSubprocess(args ...string) *exec.Cmd {
	cmd := exec.Command(os.Args[0], args...)
	cmd.Env = append(os.Environ(), "KM_TEST_AS_KM=1")
	return cmd
}

func eventually(t *testing.T, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for !check() {
		if time.Now().After(deadline) {
			t.Fatal("condition did not become true")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func launchDetached(t *testing.T, args []string) (map[string]string, *exec.Cmd) {
	t.Helper()
	cmd := kmSubprocess(append([]string{"run", "-detach"}, args...)...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("launcher: %v\n%s\n%s", err, out, &stderr)
	}
	handle := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		fields := strings.SplitN(strings.TrimSpace(line), " ", 2)
		if len(fields) != 2 {
			t.Fatalf("bad handle %q", out)
		}
		handle[fields[0]] = strings.TrimSpace(fields[1])
	}
	if len(handle) != 4 || handle["run"] == "" || handle["output"] == "" || handle["worktree"] == "" {
		t.Fatalf("bad handle: %s", out)
	}
	pid, err := strconv.Atoi(handle["pid"])
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(handle["output"])
	t.Cleanup(func() {
		data, _ := os.ReadFile(filepath.Join(dir, "current.json"))
		var s runState
		if json.Unmarshal(data, &s) == nil && runLiveness(s) == "alive" {
			_ = syscall.Kill(pid, syscall.SIGTERM)
			eventually(t, func() bool { _, err := os.Stat(filepath.Join(dir, "result.json")); return err == nil })
		}
	})
	return handle, cmd
}

func waitSubprocess(t *testing.T, args ...string) (string, string, int) {
	t.Helper()
	cmd := kmSubprocess(append([]string{"wait"}, args...)...)
	var out, diag bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &diag
	err := cmd.Run()
	code := 0
	if err != nil {
		if e, ok := err.(*exec.ExitError); ok {
			code = e.ExitCode()
		} else {
			t.Fatal(err)
		}
	}
	return out.String(), diag.String(), code
}

func readDetachedSummary(t *testing.T, h map[string]string) ensembleSummary {
	t.Helper()
	path := filepath.Join(filepath.Dir(h["output"]), "result.json")
	var sum ensembleSummary
	eventually(t, func() bool { data, err := os.ReadFile(path); return err == nil && json.Unmarshal(data, &sum) == nil })
	return sum
}

func TestDetachPrintsTheRunIdPidWorktreeAndOutputPathAndReturnsWhileTheRunIsStillGoing(t *testing.T) {
	hold := make(chan struct{})
	defer close(hold)
	f := &runCAO{hold: hold}
	wt, args := runSessionArgs(t, f)
	h, launcher := launchDetached(t, args)
	pid, _ := strconv.Atoi(h["pid"])
	sid, err := unix.Getsid(pid)
	if err != nil || sid != pid || sid == launcher.Process.Pid {
		t.Fatalf("session %d pid %d: %v", sid, pid, err)
	}
	if h["worktree"] != wt {
		t.Fatal(h)
	}
	path := filepath.Join(filepath.Dir(h["output"]), "current.json")
	data, err := os.ReadFile(path)
	var state runState
	if err != nil || json.Unmarshal(data, &state) != nil || state.PID != pid || runLiveness(state) != "alive" {
		t.Fatalf("state %s: %v", data, err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(path), "result.json")); !os.IsNotExist(err) {
		t.Fatalf("result present: %v", err)
	}
	rows := statusRows(t)
	if len(rows) != 1 || rows[0].RunID != h["run"] || rows[0].Liveness != "alive" {
		t.Fatal(rows)
	}
	eventually(t, func() bool {
		data, _ := os.ReadFile(h["output"])
		return strings.Contains(string(data), "km run "+h["run"])
	})
}

func TestADetachedRunFinishesAfterTheCallerHasExitedAndWaitPrintsItsSummaryAndStatus(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(fmt.Sprint(failed), func(t *testing.T) {
			hold := make(chan struct{})
			f := &runCAO{hold: hold}
			if failed {
				f.fake.failPart = "planner"
			}
			_, args := runSessionArgs(t, f)
			taskFile := filepath.Join(t.TempDir(), "task.md")
			writeFile(t, taskFile, "original task from file")
			args = append(args, "-task=", "-task-file", taskFile)
			h, launcher := launchDetached(t, args)
			if !launcher.ProcessState.Exited() {
				t.Fatal("launcher did not exit")
			}
			if err := os.Remove(taskFile); err != nil {
				t.Fatal(err)
			}
			close(hold)
			sum := readDetachedSummary(t, h)
			var want bytes.Buffer
			_ = finishRun(&want, sum)
			out, diag, code := waitSubprocess(t, h["run"], "-timeout", "10s")
			expected := 0
			if failed {
				expected = 1
			}
			if code != expected || out != want.String() || diag != "" {
				t.Fatalf("code=%d out=%s diag=%s want=%s", code, out, diag, &want)
			}
			if (sum.Outcome == loop.Approved) == failed {
				t.Fatal(sum)
			}

			jsonOut, jsonDiag, jsonCode := waitSubprocess(t, h["run"], "-json")
			var doc waitResult
			if err := json.Unmarshal([]byte(jsonOut), &doc); err != nil || jsonCode != expected || jsonDiag != "" || doc.State != "finished" || doc.Result == nil || doc.Result.Outcome != sum.Outcome || doc.Result.Reason != sum.Reason {
				t.Fatalf("JSON wait: %d %s %s", jsonCode, jsonOut, jsonDiag)
			}
			eventually(t, func() bool {
				data, _ := os.ReadFile(h["output"])
				return strings.HasSuffix(string(data), want.String())
			})
			f.fake.mu.Lock()
			defer f.fake.mu.Unlock()
			if len(f.fake.requests) == 0 || !strings.Contains(f.fake.requests[0].Prompt, "original task from file") {
				t.Fatal("task text lost")
			}
		})
	}
}

func TestADetachedRunStopsOnSIGTERMToItsPidAndTearsTheEnsembleDown(t *testing.T) {
	hold := make(chan struct{})
	defer close(hold)
	f := &runCAO{hold: hold}
	_, args := runSessionArgs(t, f)
	h, _ := launchDetached(t, args)
	eventually(t, func() bool { f.mu.Lock(); defer f.mu.Unlock(); return len(f.created) > 0 })
	pid, _ := strconv.Atoi(h["pid"])
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	sum := readDetachedSummary(t, h)
	if sum.Outcome != loop.Failed || !strings.Contains(sum.Reason, "SIGTERM") {
		t.Fatal(sum)
	}
	eventually(t, func() bool { f.mu.Lock(); defer f.mu.Unlock(); return len(f.deleted) == 1 })
	_, _, code := waitSubprocess(t, h["run"])
	if code != 1 {
		t.Fatal(code)
	}
}

func TestADetachedRunThatFailsAfterReturningIsRecordedFailedForWait(t *testing.T) {
	f := &runCAO{createFails: true}
	_, args := runSessionArgs(t, f)
	h, _ := launchDetached(t, args)
	sum := readDetachedSummary(t, h)
	if sum.Outcome != loop.Failed || !strings.Contains(sum.Reason, "podium refused") {
		t.Fatal(sum)
	}
	out, _, code := waitSubprocess(t, h["run"])
	if code != 1 || !strings.Contains(out, "podium refused") {
		t.Fatalf("%d %s", code, out)
	}
	if rows := statusRows(t); len(rows) != 0 {
		t.Fatal(rows)
	}
}

func TestARefusalStaysInTheForegroundWithTodaysMessageAndStatus(t *testing.T) {
	f := &runCAO{}
	_, args := runSessionArgs(t, f)
	for _, extra := range [][]string{{"-rounds", "0"}, {"-ensemble", "missing"}, {"-base", "main"}} {
		var outputs []string
		for _, detach := range []bool{false, true} {
			a := append(append([]string{"run"}, args...), extra...)
			if detach {
				a = append(a, "-detach")
			}
			out, err := kmSubprocess(a...).CombinedOutput()
			if e, ok := err.(*exec.ExitError); !ok || e.ExitCode() != 1 {
				t.Fatalf("%v: %v %s", a, err, out)
			}
			outputs = append(outputs, string(out))
		}
		if outputs[0] != outputs[1] {
			t.Fatalf("refusal changed: %q / %q", outputs[0], outputs[1])
		}
	}
	if paths, _ := filepath.Glob(filepath.Join(loop.StateDir(), "runs", "*", "launch.json")); len(paths) > 0 {
		t.Fatal(paths)
	}
}

func TestRunWithoutDetachWritesNoOutputLogOrLaunchFileAndExitsAsBefore(t *testing.T) {
	_, args := runSessionArgs(t, &runCAO{})
	out, err := kmSubprocess(append([]string{"run"}, args...)...).CombinedOutput()
	if err != nil || !strings.Contains(string(out), "approved after 1 round(s)") {
		t.Fatalf("%v %s", err, out)
	}
	paths, _ := filepath.Glob(filepath.Join(loop.StateDir(), "runs", "*"))
	if len(paths) != 1 {
		t.Fatal(paths)
	}
	for _, name := range []string{"output.log", "launch.json"} {
		if _, err := os.Stat(filepath.Join(paths[0], name)); !os.IsNotExist(err) {
			t.Fatalf("%s: %v", name, err)
		}
	}
}

func TestKmUsageListsDetachAndWait(t *testing.T) {
	if !strings.Contains(usage, "-detach") || !strings.Contains(usage, "km wait RUN") {
		t.Fatal(usage)
	}
}

func TestADetachedRunKeepsTheLaunchersWholeOutputAndMakesOnlyOneWorktree(t *testing.T) {
	f := runBaseFixture(t, false)
	safetyGit(t, f.repo, "symbolic-ref", "--delete", "refs/remotes/origin/HEAD")
	h, _ := launchDetached(t, f.args)
	sum := readDetachedSummary(t, h)
	if sum.Outcome != loop.Approved || sum.Base != f.main || sum.BaseBranch != "main" {
		t.Fatal(sum)
	}
	if count := strings.Count(safetyGit(t, f.repo, "worktree", "list", "--porcelain"), "worktree "); count != 2 {
		t.Fatalf("worktrees=%d", count)
	}
	var final bytes.Buffer
	_ = finishRun(&final, sum)
	eventually(t, func() bool {
		data, _ := os.ReadFile(h["output"])
		return strings.HasSuffix(string(data), final.String())
	})
	data, _ := os.ReadFile(h["output"])
	if !strings.Contains(strings.Split(string(data), "\n")[0], "main") || strings.Count(string(data), "config ") != 1 || strings.Count(string(data), "km run "+h["run"]) != 1 {
		t.Fatalf("output: %s", data)
	}
}

func TestDetachKeepsMissingChairsAndProfileSafetyRefusalsInTheForeground(t *testing.T) {
	t.Run("missing chairs", func(t *testing.T) {
		t.Setenv(loop.HomeEnv, t.TempDir())
		t.Setenv(loop.StateEnv, t.TempDir())
		var previous string
		for _, detach := range []bool{false, true} {
			args := []string{"run", "-repo", "/does-not-exist", "-task", "probe"}
			if detach {
				args = append(args, "-detach")
			}
			data, err := kmSubprocess(args...).CombinedOutput()
			if e, ok := err.(*exec.ExitError); !ok || e.ExitCode() != 1 || !strings.Contains(string(data), "no chair") {
				t.Fatalf("%v %s", err, data)
			}
			if detach && previous != string(data) {
				t.Fatalf("refusal changed: %q / %q", previous, data)
			}
			previous = string(data)
		}
	})
	t.Run("profile safety", func(t *testing.T) {
		_, args := runSessionArgs(t, &runCAO{})
		caoHome := t.TempDir()
		t.Setenv("CAO_HOME_DIR", caoHome)
		srv := safetyProfiles(t, caoHome, 403)
		args = append(args, "-gate", "claude", "-cao", srv.URL)
		var previous string
		for _, detach := range []bool{false, true} {
			a := append([]string{"run"}, args...)
			if detach {
				a = append(a, "-detach")
			}
			data, err := kmSubprocess(a...).CombinedOutput()
			if e, ok := err.(*exec.ExitError); !ok || e.ExitCode() != 1 || !strings.Contains(string(data), "cannot establish that the CAO") {
				t.Fatalf("%v %s", err, data)
			}
			if detach && previous != string(data) {
				t.Fatalf("refusal changed: %q / %q", previous, data)
			}
			previous = string(data)
		}
		if paths, _ := filepath.Glob(filepath.Join(loop.StateDir(), "runs", "*", "launch.json")); len(paths) != 0 {
			t.Fatal(paths)
		}
	})
}

func TestDetachReturnsBeforeBaselineVerificationFinishes(t *testing.T) {
	_, args := runSessionArgs(t, &runCAO{})
	marker := filepath.Join(t.TempDir(), "verify-started")
	release := filepath.Join(t.TempDir(), "verify-release")
	verify := "touch '" + marker + "'; while [ ! -e '" + release + "' ]; do sleep 0.1; done"
	args = append(args, "-verify", verify)
	h, _ := launchDetached(t, args)
	eventually(t, func() bool { _, err := os.Stat(marker); return err == nil })
	if _, err := os.Stat(filepath.Join(filepath.Dir(h["output"]), "result.json")); !os.IsNotExist(err) {
		t.Fatalf("baseline already finished: %v", err)
	}
	writeFile(t, release, "")
	if sum := readDetachedSummary(t, h); sum.Outcome != loop.Approved {
		t.Fatal(sum)
	}
}

func TestDetachStillRunsWhenTheArgumentsEndWithTheFlagTerminator(t *testing.T) {
	t.Setenv("KM_TEST_REFUSE_RECURSION", "1")
	_, args := runSessionArgs(t, &runCAO{})
	h, _ := launchDetached(t, append(args, "--"))
	if sum := readDetachedSummary(t, h); sum.Outcome != loop.Approved {
		t.Fatal(sum)
	}
}
