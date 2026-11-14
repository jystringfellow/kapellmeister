package loop

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func snapshotGit(t *testing.T, e *Engine, args ...string) string {
	t.Helper()
	out, err := e.gitOutput(args...)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func snapshotWork(t *testing.T, e *Engine, f *fakeStepper) {
	t.Helper()
	for _, name := range []string{"main.go", "deleted", "staged", "executable", "type"} {
		f.write(name, "base\n")
	}
	snapshotGit(t, e, "add", "--all")
	snapshotGit(t, e, "-c", "user.name=t", "-c", "user.email=t@example.invalid", "commit", "-qm", "files")
	f.actions["implement"] = func(s Step) {
		f.write("main.go", "work under review\n")
		f.write("staged", "staged only\n")
		snapshotGit(t, e, "add", "staged")
		if err := os.Remove(filepath.Join(f.dir, "deleted")); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(filepath.Join(f.dir, "executable"), 0755); err != nil {
			t.Fatal(err)
		}
		f.write("new.go", "new work\n")
		f.write(s.ReplyPath, "implementation")
	}
}

func wipeSnapshotWork(t *testing.T, e *Engine) {
	t.Helper()
	snapshotGit(t, e, "reset", "--hard")
	snapshotGit(t, e, "clean", "-fd")
}

func assertSnapshotRetry(t *testing.T, part string, twice bool) {
	t.Helper()
	e, f := testerEngine(t)
	snapshotWork(t, e, f)
	var progress bytes.Buffer
	e.Progress = &progress
	var before string
	calls := 0
	f.actions[part] = func(s Step) {
		calls++
		if calls == 1 {
			if s.Retry != 0 || strings.Contains(s.Prompt, "your previous attempt") {
				t.Errorf("first prompt contains retry warning: %s", s.Prompt)
			}
			before = snapshotGit(t, e, "status", "--porcelain=v1", "--untracked-files=all")
		}
		if calls == 1 || twice {
			wipeSnapshotWork(t, e)
		} else {
			if got := snapshotGit(t, e, "status", "--porcelain=v1", "--untracked-files=all"); got != before {
				t.Errorf("state after restore:\n%s\nwant:\n%s", got, before)
			}
			if !strings.Contains(s.Prompt, "your previous attempt at this step changed the worktree") {
				t.Errorf("missing retry note: %s", s.Prompt)
			}
		}
		f.write(s.ReplyPath, "approved")
	}
	res := run(t, e)
	want := Approved
	if twice {
		want = Failed
	}
	if res.Outcome != want || calls != 2 {
		t.Fatalf("outcome=%+v calls=%d\n%s", res, calls, &progress)
	}
	if got := snapshotGit(t, e, "status", "--porcelain=v1", "--untracked-files=all"); got != before {
		t.Errorf("final state:\n%s\nwant:\n%s", got, before)
	}
	for path, want := range map[string]string{"main.go": "work under review\n", "new.go": "new work\n", "staged": "staged only\n"} {
		data, err := os.ReadFile(filepath.Join(f.dir, path))
		if err != nil || string(data) != want {
			t.Errorf("%s=%q %v", path, data, err)
		}
	}
	if _, err := os.Stat(filepath.Join(f.dir, "deleted")); !os.IsNotExist(err) {
		t.Errorf("deleted resurrected: %v", err)
	}
	info, err := os.Stat(filepath.Join(f.dir, "executable"))
	if err != nil || info.Mode().Perm() != 0755 {
		t.Errorf("mode=%v %v", info, err)
	}
	if !strings.Contains(progress.String(), "restored the work from the snapshot; running "+part+" again, told not to change the worktree") || !strings.Contains(progress.String(), "(retry)") {
		t.Errorf("missing progress: %s", &progress)
	}
	if part == "gate" {
		want := "  gate chair p:x changed the worktree: deleted, executable, main.go, new.go, staged"
		if !strings.Contains(progress.String(), want) {
			t.Errorf("missing change line %q: %s", want, &progress)
		}
	}
	t.Log(progress.String())
}

func TestAReviewerThatResetsAndCleansTheWorktreeIsRestoredAndRetriedOnce(t *testing.T) {
	assertSnapshotRetry(t, "review", false)
}
func TestTheGateThatResetsAndCleansTheWorktreeIsRestoredAndRetriedOnce(t *testing.T) {
	assertSnapshotRetry(t, "gate", false)
}
func TestTheTesterThatResetsAndCleansTheWorktreeIsRestoredAndRetriedOnce(t *testing.T) {
	assertSnapshotRetry(t, "test", false)
}
func TestASecondViolationRestoresAgainAndFailsTheRunWithTheWorkIntact(t *testing.T) {
	for _, part := range []string{"review", "gate", "test"} {
		t.Run(part, func(t *testing.T) { assertSnapshotRetry(t, part, true) })
	}
}

func TestALessonsStepThatWipesTheWorktreeIsRestoredRetriedAndNeverFailsTheRun(t *testing.T) {
	e, f, _ := newEngine(t, passing)
	snapshotWork(t, e, f)
	e.Config.Lessons = true
	calls := 0
	f.actions["lessons"] = func(s Step) {
		calls++
		if calls == 1 {
			wipeSnapshotWork(t, e)
		}
		f.write(s.ReplyPath, "[]")
	}
	res := run(t, e)
	if res.Outcome != Approved || calls != 2 {
		t.Fatalf("%+v calls=%d lessons=%+v", res, calls, res.Lessons)
	}
}

func TestAFailedRunLeavesTheWorktreeInTheSnapshottedStateNotTheDamagedOne(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		t.Run(fmt.Sprint(cancelled), func(t *testing.T) {
			e, f := testerEngine(t)
			snapshotWork(t, e, f)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var before string
			f.actions["test"] = func(s Step) {
				before = snapshotGit(t, e, "status", "--porcelain=v1")
				wipeSnapshotWork(t, e)
				f.write(s.ReplyPath, "test")
				if cancelled {
					cancel()
				}
			}
			f.errFor = func(s Step) error {
				if s.Name == "test" {
					return context.DeadlineExceeded
				}
				return nil
			}
			res, err := e.Run(ctx)
			if err != nil || res.Outcome != Failed || len(testerSteps(f, "test")) != 1 {
				t.Fatalf("%+v %v", res, err)
			}
			if got := snapshotGit(t, e, "status", "--porcelain=v1"); got != before {
				t.Errorf("damaged state: %s want %s", got, before)
			}
		})
	}
}

func TestRestoreBringsBackNewUntrackedFilesDeletionsStagedOnlyChangesModeChangesSymlinksAndTypeChangesExactly(t *testing.T) {
	for _, kind := range []string{"untracked", "staged and unstaged", "intent to add", "symlink", "file to directory", "directory to file", "missing index"} {
		t.Run(kind, func(t *testing.T) {
			e, f, _ := newEngine(t, passing)
			snapshotWork(t, e, f)
			if err := PrepareRunDir(f.dir, e.Config.RunID); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(filepath.Join(f.dir, "directory"), 0755); err != nil {
				t.Fatal(err)
			}
			f.write("directory/child", "base")
			snapshotGit(t, e, "add", "--all")
			snapshotGit(t, e, "-c", "user.name=t", "-c", "user.email=t@example.invalid", "commit", "-qm", "directory")
			f.actions["implement"](Step{ReplyPath: e.rel("implement-1.md")})
			switch kind {
			case "staged and unstaged":
				f.write("staged", "unstaged over staged\n")
			case "intent to add":
				snapshotGit(t, e, "add", "--intent-to-add", "new.go")
			case "symlink":
				if err := os.Symlink("main.go", filepath.Join(f.dir, "link")); err != nil {
					t.Fatal(err)
				}
			case "file to directory":
				if err := os.Remove(filepath.Join(f.dir, "type")); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(filepath.Join(f.dir, "type"), 0755); err != nil {
					t.Fatal(err)
				}
				f.write("type/child", "new directory child")
			case "directory to file":
				if err := os.RemoveAll(filepath.Join(f.dir, "directory")); err != nil {
					t.Fatal(err)
				}
				f.write("directory", "replacement file")
			case "missing index":
				path := strings.TrimSpace(snapshotGit(t, e, "rev-parse", "--path-format=absolute", "--git-path", "index"))
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			}
			s, err := e.takeSnapshot()
			if err != nil {
				t.Fatal(err)
			}
			defer e.cleanupSnapshot()
			wipeSnapshotWork(t, e)
			if err := e.restoreSnapshot(s); err != nil {
				t.Fatal(err)
			}
			state, err := e.treeState()
			if err != nil || state != s.State {
				t.Fatalf("state=%s want=%s error=%v", state, s.State, err)
			}
			index, err := os.ReadFile(s.IndexPath)
			// Git may refresh index stat entries while inspecting the restored files.
			if s.IndexExists {
				if err != nil || len(index) == 0 {
					t.Fatalf("index missing: %v", err)
				}
			} else if !os.IsNotExist(err) {
				t.Fatalf("absent index restored as present: %v", err)
			}
			if kind == "symlink" {
				target, err := os.Readlink(filepath.Join(f.dir, "link"))
				if err != nil || target != "main.go" {
					t.Fatalf("link=%s %v", target, err)
				}
			}
		})
	}
}

func TestAnIgnoredFileIsNotRestored(t *testing.T) {
	e, f, _ := newEngine(t, passing)
	snapshotWork(t, e, f)
	f.write(".gitignore", "ignored\n")
	snapshotGit(t, e, "add", ".gitignore")
	snapshotGit(t, e, "-c", "user.name=t", "-c", "user.email=t@example.invalid", "commit", "-qm", "ignore")
	f.actions["implement"](Step{ReplyPath: "summary"})
	f.write("ignored", "original")
	if err := PrepareRunDir(f.dir, e.Config.RunID); err != nil {
		t.Fatal(err)
	}
	s, err := e.takeSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	defer e.cleanupSnapshot()
	wipeSnapshotWork(t, e)
	f.write("ignored", "changed")
	if err := e.restoreSnapshot(s); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(f.dir, "ignored"))
	if err != nil || string(data) != "changed" {
		t.Fatalf("%s %v", data, err)
	}
}

func TestAGroupOfReviewersIsRestoredAndAllReviewersRerunAfterOneWipesTheWorktree(t *testing.T) {
	e, f, records := newEngine(t, passing)
	snapshotWork(t, e, f)
	twoReviewers(e)
	f.actions["review"] = func(s Step) {
		if s.ChairName == "reviewer-2" && s.Retry == 0 {
			wipeSnapshotWork(t, e)
		}
		if s.Retry == 1 {
			data, err := os.ReadFile(filepath.Join(f.dir, "new.go"))
			if err != nil || string(data) != "new work\n" {
				t.Errorf("work missing: %s %v", data, err)
			}
		}
		f.write(s.ReplyPath, "approved")
	}
	res := run(t, e)
	if res.Outcome != Approved {
		t.Fatal(res)
	}
	reviews := testerSteps(f, "review")
	if len(reviews) != 4 || len(recordsOf(t, records, "verdict-review")) != 4 {
		t.Fatalf("%v\n%s", f.names(), records)
	}
}

func TestTheRetryNeverSeesTheFirstAttemptsReplyOrVerdictFile(t *testing.T) {
	e, f, _ := newEngine(t, passing)
	f.actions["review"] = func(s Step) {
		verdict := strings.TrimSuffix(s.ReplyPath, ".md") + ".verdict"
		if s.Retry == 0 {
			f.write("extra", "changed")
			f.write(verdict, "stale")
		} else {
			for _, p := range []string{s.ReplyPath, verdict} {
				if _, err := os.Stat(filepath.Join(f.dir, p)); !os.IsNotExist(err) {
					t.Errorf("stale reply %s: %v", p, err)
				}
			}
		}
		f.write(s.ReplyPath, "APPROVE")
	}
	if res := run(t, e); res.Outcome != Approved {
		t.Fatal(res)
	}
}

func TestAStepThatMovesHeadIsNotRestoredOrRetried(t *testing.T) {
	e, f, _ := newEngine(t, passing)
	snapshotWork(t, e, f)
	var head string
	f.actions["gate"] = func(s Step) {
		snapshotGit(t, e, "add", "--all")
		snapshotGit(t, e, "-c", "user.name=t", "-c", "user.email=t@example.invalid", "commit", "-qm", "chair committed")
		head = snapshotGit(t, e, "rev-parse", "HEAD")
		f.write(s.ReplyPath, "APPROVE")
	}
	res := run(t, e)
	if res.Outcome != Failed || !strings.Contains(res.Reason, "HEAD moved") || len(testerSteps(f, "gate")) != 1 || len(res.Restores) != 0 || snapshotGit(t, e, "rev-parse", "HEAD") != head {
		t.Fatal(res)
	}
}

func TestRunFailsBeforeAReadOnlyStepWhenTheSnapshotCannotBeTaken(t *testing.T) {
	e, f, _ := newEngine(t, passing)
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "missing"))
	res := run(t, e)
	if res.Outcome != Failed || len(testerSteps(f, "review")) != 0 {
		t.Fatalf("%+v %v", res, f.names())
	}
}

func TestRestoreRecordsNameTheChairAndABoundedPathList(t *testing.T) {
	e, f, records := newEngine(t, passing)
	f.actions["implement"] = func(s Step) {
		for i := 0; i < 15; i++ {
			f.write(fmt.Sprintf("file-%02d", i), "work")
		}
		f.write("a\nnewline", "work")
		f.write(s.ReplyPath, "done")
	}
	f.actions["gate"] = func(s Step) {
		if s.Retry == 0 {
			wipeSnapshotWork(t, e)
		}
		f.write(s.ReplyPath, "APPROVE")
	}
	res := run(t, e)
	recs := recordsOf(t, records, "restore")
	if res.Outcome != Approved || len(recs) != 1 {
		t.Fatalf("%+v %v", res, recs)
	}
	r := recs[0]
	if r.Chair == nil || r.Chair.String() != "p:x" || !r.Restored || len(r.Paths) != 10 || r.PathsMore != 6 || r.Paths[0] != "a\\nnewline" {
		t.Fatalf("%+v", r)
	}
	retryRecords := recordsOf(t, records, "gate")
	if len(retryRecords) != 3 || retryRecords[0].Retry != 0 || !retryRecords[1].TreeChanged || retryRecords[2].Retry != 1 {
		t.Fatalf("%+v", retryRecords)
	}
	if len(res.Restores) != 1 || res.Restores[0].Outcome != "retried" {
		t.Fatal(res)
	}
}

func TestAReadOnlyStepThatLeavesTheWorktreeAloneRunsOnceWithNoRestoreRecordsAndNoExtraProgress(t *testing.T) {
	e, f, records := newEngine(t, passing)
	var progress bytes.Buffer
	e.Progress = &progress
	res := run(t, e)
	if res.Outcome != Approved || len(res.Restores) != 0 || len(recordsOf(t, records, "restore")) != 0 || strings.Contains(progress.String(), "snapshot") || strings.Contains(progress.String(), "retry") {
		t.Fatalf("%+v\n%s", res, &progress)
	}
	equal(t, f.names(), []string{"plan", "implement[1]", "review[1]", "classify-review[1]", "gate[1]", "classify-gate[1]"})
}

func TestNothingIsLeftBehindInTheRepositoryAfterARun(t *testing.T) {
	for _, linked := range []bool{false, true} {
		for _, outcome := range []string{"approved", "failed", "cancelled"} {
			t.Run(fmt.Sprintf("%t/%s", linked, outcome), func(t *testing.T) {
				e, f, _ := newEngine(t, passing)
				snapshotWork(t, e, f)
				if linked {
					wt := filepath.Join(t.TempDir(), "wt")
					snapshotGit(t, e, "worktree", "add", "--detach", wt, "HEAD")
					e.Config.Worktree = wt
					f.dir = wt
				}
				scratch := t.TempDir()
				t.Setenv("TMPDIR", scratch)
				before := map[string]string{}
				verifyCalls := 0
				e.Verify = func(ctx context.Context, dir string) (VerifyResult, error) {
					verifyCalls++
					if verifyCalls == 2 {
						before["objects"] = snapshotRepoState(t, e, "objects")
					}
					return passing(ctx, dir)
				}
				for _, what := range []string{"refs", "stash", "objects", "head", "worktrees"} {
					before[what] = snapshotRepoState(t, e, what)
				}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				f.actions["gate"] = func(s Step) {
					if s.Retry == 0 || outcome == "failed" {
						wipeSnapshotWork(t, e)
					}
					if outcome == "cancelled" {
						cancel()
					}
					f.write(s.ReplyPath, "APPROVE")
				}
				res, err := e.Run(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if (res.Outcome == Approved) != (outcome == "approved") {
					t.Fatal(res)
				}
				for what, want := range before {
					if got := snapshotRepoState(t, e, what); got != want {
						t.Errorf("%s changed:\n%s\n%s", what, want, got)
					}
				}
				paths, err := filepath.Glob(filepath.Join(scratch, "km-snapshot-*"))
				if err != nil || len(paths) != 0 {
					t.Fatalf("scratch remains: %v %v", paths, err)
				}
				if _, err := os.Stat(filepath.Join(f.dir, e.rel("snapshot.json"))); !os.IsNotExist(err) {
					t.Fatalf("snapshot manifest remains: %v", err)
				}
			})
		}
	}
}

func snapshotRepoState(t *testing.T, e *Engine, what string) string {
	t.Helper()
	switch what {
	case "refs":
		return snapshotGit(t, e, "for-each-ref")
	case "stash":
		return snapshotGit(t, e, "stash", "list")
	case "head":
		return snapshotGit(t, e, "rev-parse", "HEAD")
	case "worktrees":
		return snapshotGit(t, e, "worktree", "list", "--porcelain")
	case "objects":
		root := strings.TrimSpace(snapshotGit(t, e, "rev-parse", "--path-format=absolute", "--git-path", "objects"))
		var paths []string
		err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !d.IsDir() {
				rel, err := filepath.Rel(root, path)
				if err != nil {
					return err
				}
				paths = append(paths, rel)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		return strings.Join(paths, "\n")
	}
	panic(what)
}

func TestAnUnconfirmedRestoreKeepsTheSnapshotAndNamesItsLocation(t *testing.T) {
	e, f, _ := newEngine(t, passing)
	snapshotWork(t, e, f)
	exclude := strings.TrimSpace(snapshotGit(t, e, "rev-parse", "--path-format=absolute", "--git-path", "info/exclude"))
	if err := os.WriteFile(exclude, []byte("new.go/ignored\n"), 0600); err != nil {
		t.Fatal(err)
	}
	f.actions["implement"] = func(s Step) {
		f.write("new.go", "work")
		f.write(s.ReplyPath, "done")
	}
	f.actions["gate"] = func(s Step) {
		wipeSnapshotWork(t, e)
		if err := os.Mkdir(filepath.Join(f.dir, "new.go"), 0755); err != nil {
			t.Fatal(err)
		}
		f.write("new.go/ignored", "keep ignored")
	}
	res := run(t, e)
	if res.Outcome != Failed || !strings.Contains(res.Reason, "restore could not be confirmed") || e.snapshotDir == "" || !strings.Contains(res.Reason, e.snapshotDir) {
		t.Fatalf("%+v", res)
	}
	if _, err := os.Stat(filepath.Join(e.snapshotDir, "saved-index")); err != nil {
		t.Fatal(err)
	}
	e.keepSnapshot = false
	e.cleanupSnapshot()
}

func TestSnapshotsNeverWriteRepositoryObjectsOrChangeTheIndex(t *testing.T) {
	for _, split := range []bool{false, true} {
		t.Run(fmt.Sprint(split), func(t *testing.T) {
			e, f, _ := newEngine(t, passing)
			snapshotWork(t, e, f)
			if err := PrepareRunDir(f.dir, e.Config.RunID); err != nil {
				t.Fatal(err)
			}
			if split {
				snapshotGit(t, e, "config", "core.splitIndex", "true")
				snapshotGit(t, e, "update-index", "--split-index")
			}
			f.actions["implement"](Step{ReplyPath: e.rel("implement-1.md")})
			path := strings.TrimSpace(snapshotGit(t, e, "rev-parse", "--path-format=absolute", "--git-path", "index"))
			if _, err := e.treeState(); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			objects := snapshotRepoState(t, e, "objects")
			gitDir := strings.TrimSpace(snapshotGit(t, e, "rev-parse", "--absolute-git-dir"))
			sharedBefore, err := filepath.Glob(filepath.Join(gitDir, "sharedindex.*"))
			if err != nil {
				t.Fatal(err)
			}
			s, err := e.takeSnapshot()
			if err != nil {
				t.Fatal(err)
			}
			defer e.cleanupSnapshot()
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("index changed: %v", err)
			}
			if snapshotRepoState(t, e, "objects") != objects {
				t.Fatal("snapshot wrote repository objects")
			}
			sharedAfter, err := filepath.Glob(filepath.Join(gitDir, "sharedindex.*"))
			if err != nil || strings.Join(sharedBefore, "\n") != strings.Join(sharedAfter, "\n") {
				t.Fatalf("shared index changed: %v -> %v (%v)", sharedBefore, sharedAfter, err)
			}
			wipeSnapshotWork(t, e)
			if err := e.restoreSnapshot(s); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestAReadOnlyStepCannotChangeOnlyUntrackedContentsOrOnlyTheStagedVersion(t *testing.T) {
	for _, staged := range []bool{false, true} {
		t.Run(fmt.Sprint(staged), func(t *testing.T) {
			e, f, _ := newEngine(t, passing)
			if err := PrepareRunDir(f.dir, e.Config.RunID); err != nil {
				t.Fatal(err)
			}
			f.write("new", "original")
			if staged {
				snapshotGit(t, e, "add", "new")
				f.write("new", "worktree")
			}
			calls := 0
			f.actions["gate"] = func(s Step) {
				calls++
				if calls == 1 {
					f.write("new", "changed")
					if staged {
						snapshotGit(t, e, "add", "new")
						f.write("new", "worktree")
					}
				}
				f.write(s.ReplyPath, "APPROVE")
			}
			_, err := e.readOnlySteps(context.Background(), []Step{{Name: "gate", Round: 1, Chair: e.Config.Chairs.Gate, ReplyPath: e.rel("gate-1.md")}})
			defer e.cleanupSnapshot()
			if err != nil || calls != 2 {
				t.Fatalf("calls=%d error=%v", calls, err)
			}
			if staged {
				if got := snapshotGit(t, e, "show", ":new"); got != "original" {
					t.Fatalf("index=%q", got)
				}
			} else {
				data, err := os.ReadFile(filepath.Join(f.dir, "new"))
				if err != nil || string(data) != "original" {
					t.Fatalf("%s %v", data, err)
				}
			}
		})
	}
}

func TestLessonsCannotOverwriteTheSavedWorkAfterAnUnconfirmedRestore(t *testing.T) {
	e, f := testerEngine(t)
	snapshotWork(t, e, f)
	e.Config.Lessons = true
	exclude := strings.TrimSpace(snapshotGit(t, e, "rev-parse", "--path-format=absolute", "--git-path", "info/exclude"))
	if err := os.WriteFile(exclude, []byte("new.go/ignored\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var manifest, index []byte
	f.actions["test"] = func(s Step) {
		var err error
		manifest, err = os.ReadFile(filepath.Join(e.snapshotDir, "snapshot.json"))
		if err != nil {
			t.Fatal(err)
		}
		index, err = os.ReadFile(filepath.Join(e.snapshotDir, "saved-index"))
		if err != nil {
			t.Fatal(err)
		}
		wipeSnapshotWork(t, e)
		if err := os.Mkdir(filepath.Join(f.dir, "new.go"), 0755); err != nil {
			t.Fatal(err)
		}
		f.write("new.go/ignored", "keep ignored")
		f.write(s.ReplyPath, "APPROVE")
	}
	res := run(t, e)
	defer func() { e.keepSnapshot = false; e.cleanupSnapshot() }()
	if res.Outcome != Failed || e.snapshotDir == "" {
		t.Fatal(res)
	}
	for path, want := range map[string][]byte{"snapshot.json": manifest, "saved-index": index} {
		got, err := os.ReadFile(filepath.Join(e.snapshotDir, path))
		if err != nil || !bytes.Equal(got, want) {
			t.Errorf("%s was overwritten: %v", path, err)
		}
	}
	if len(testerSteps(f, "lessons")) != 0 {
		t.Fatal("lessons ran on work that could not be restored")
	}
}

func TestASnapshotNeverLivesInsideTheWorktreeOrTheTestersScratch(t *testing.T) {
	for _, inWorktree := range []bool{false, true} {
		t.Run(fmt.Sprint(inWorktree), func(t *testing.T) {
			e, f, _ := newEngine(t, passing)
			root := e.Config.ScratchDir
			if inWorktree {
				root = f.dir
			}
			dir := filepath.Join(root, "tmp")
			if err := os.Mkdir(dir, 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("TMPDIR", dir)
			res := run(t, e)
			if res.Outcome != Failed || !strings.Contains(res.Reason, "snapshot must be outside") || len(testerSteps(f, "review")) != 0 {
				t.Fatalf("%+v %v", res, f.names())
			}
			paths, err := filepath.Glob(filepath.Join(dir, "km-snapshot-*"))
			if err != nil || len(paths) != 0 {
				t.Fatalf("leftovers: %v %v", paths, err)
			}
		})
	}
}
