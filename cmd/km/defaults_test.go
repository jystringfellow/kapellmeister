package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jystringfellow/kapellmeister/internal/loop"
)

func setBaseConfig(t *testing.T, f baseFixture, branch, body string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "seed")
	safetyGit(t, f.repo, "clone", "-q", "-b", branch, f.origin, dir)
	safetyGit(t, dir, "config", "user.name", "Test")
	safetyGit(t, dir, "config", "user.email", "test@example.invalid")
	writeFile(t, filepath.Join(dir, loop.ConfigFile), body)
	safetyGit(t, dir, "add", "--force", loop.ConfigFile)
	safetyGit(t, dir, "-c", "commit.gpgsign=false", "commit", "-qm", "repository defaults fixture")
	commit := safetyGit(t, dir, "rev-parse", "HEAD")
	safetyGit(t, f.origin, "fetch", "-q", dir, "HEAD")
	safetyGit(t, f.origin, "update-ref", "refs/heads/"+branch, commit)
	return commit
}

const baseConfig = "[test]\nverify = 'true'\n[run]\npreflight_timeout = '0'\nreply_grace = '1ms'\nrounds = 2\n[sizing]\nmode = 'off'\n"

func TestRunWithoutBaseStartsFromAnOriginWhoseDefaultBranchIsNotMain(t *testing.T) {
	for _, branch := range []string{"develop", "master", "release/current"} {
		t.Run(branch, func(t *testing.T) {
			f := runBaseFixture(t, false)
			safetyGit(t, f.origin, "update-ref", "refs/heads/"+branch, f.stack)
			safetyGit(t, f.origin, "symbolic-ref", "HEAD", "refs/heads/"+branch)
			repo := filepath.Join(t.TempDir(), "clone")
			safetyGit(t, f.repo, "clone", "-q", f.origin, repo)
			f.repo = repo
			f.args[1] = repo
			requireRunBase(t, f, branch, f.stack)
		})
	}
}

func TestRunNotesAndFallsBackToMainWhenGitDoesNotRecordOriginsDefaultBranch(t *testing.T) {
	f := runBaseFixture(t, false)
	safetyGit(t, f.repo, "symbolic-ref", "--delete", "refs/remotes/origin/HEAD")
	out := requireRunBase(t, f, "main", f.main)
	if !strings.Contains(out, "note: git does not record origin's default branch") || !strings.Contains(out, "starting from main") {
		t.Fatalf("missing fallback note: %s", out)
	}
}

func TestRepositoryConfigRunBaseChoosesTheBaseAndDashBaseWinsOverIt(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		t.Run(fmt.Sprint(explicit), func(t *testing.T) {
			f := runBaseFixture(t, false)
			main := setBaseConfig(t, f, "main", strings.Replace(baseConfig, "[run]", "[run]\nbase = 'stack'", 1))
			f.main = main
			if explicit {
				requireRunBase(t, f, "main", main, "-base", "main")
			} else {
				requireRunBase(t, f, "stack", f.stack)
			}
		})
	}
}

func TestAnInvalidRunBaseInTheRepositoryConfigIsRefusedNamingTheKeyBeforeAnyChairStarts(t *testing.T) {
	for _, branch := range []string{"", "-oops", "bad..branch"} {
		t.Run(fmt.Sprintf("%q", branch), func(t *testing.T) {
			f := runBaseFixture(t, false)
			setBaseConfig(t, f, "main", strings.Replace(baseConfig, "[run]", "[run]\nbase = '"+branch+"'", 1))
			err := runCommand(f.args)
			if err == nil || !strings.Contains(err.Error(), "run.base") || !strings.Contains(err.Error(), loop.ConfigFile) {
				t.Fatalf("invalid config base = %v", err)
			}
			requireNoBaseRun(t, f)
		})
	}
}

func TestRunLeavesTheClonesRemoteTrackingBranchesAlone(t *testing.T) {
	f := runBaseFixture(t, false)
	safetyGit(t, f.origin, "update-ref", "refs/heads/gone", f.stack)
	safetyGit(t, f.repo, "fetch", "-q", "origin")
	safetyGit(t, f.origin, "update-ref", "-d", "refs/heads/gone")
	before := safetyGit(t, f.repo, "for-each-ref", "--format=%(refname) %(objectname)")
	head := safetyGit(t, f.repo, "rev-parse", "HEAD")
	status := safetyGit(t, f.repo, "status", "--porcelain")
	requireRunBase(t, f, "main", f.main)
	after := safetyGit(t, f.repo, "for-each-ref", "--format=%(refname) %(objectname)")
	if before != after {
		t.Errorf("clone refs changed:\nbefore %s\nafter %s", before, after)
	}
	if safetyGit(t, f.repo, "rev-parse", "HEAD") != head || safetyGit(t, f.repo, "status", "--porcelain") != status {
		t.Error("clone checkout changed")
	}
}

func TestRunMakesItsWorktreeUnderTheConfiguredDirectoryAndTheSystemTempByDefault(t *testing.T) {
	for _, kind := range []string{"default", "absolute", "home"} {
		t.Run(kind, func(t *testing.T) {
			f := runBaseFixture(t, false)
			parent := os.TempDir()
			if kind != "default" {
				parent = filepath.Join(t.TempDir(), "worktrees")
				value := parent
				if kind == "home" {
					home := t.TempDir()
					t.Setenv("HOME", home)
					parent = filepath.Join(home, "worktrees")
					value = "~/worktrees"
				}
				writeFile(t, loop.UserConfigPath(loop.Home()), fmt.Sprintf("[run]\nworktree_dir = %q\n", value))
			}
			requireRunBase(t, f, "main", f.main)
			_, result := onlyRunResult(t)
			want, err := filepath.EvalSymlinks(parent)
			if err != nil {
				t.Fatal(err)
			}
			wt := result["worktree"].(string)
			if filepath.Dir(wt) != want {
				t.Errorf("worktree %s, want parent %s", wt, want)
			}
		})
	}
}

func TestTheFinalSummaryNamesTheWorktreeForEveryOutcomeThatLeavesChanges(t *testing.T) {
	for _, tc := range []struct {
		outcome string
		diff    string
		moved   bool
		wt      string
		want    bool
	}{
		{loop.Approved, "", false, "/scratch/worktree", true},
		{loop.RoundsExhausted, " change | 1 +\n", false, "/scratch/worktree", true},
		{loop.Failed, " change | 1 +\n", false, "/scratch/worktree", true},
		{loop.Untested, " change | 1 +\n", false, "/scratch/worktree", true},
		{loop.Failed, "", true, "/scratch/worktree", true},
		{loop.Failed, "", false, "/scratch/worktree", false},
		{loop.Approved, " change | 1 +\n", false, "", false},
	} {
		t.Run(fmt.Sprintf("%s_%s_%t_%t", tc.outcome, tc.diff, tc.moved, tc.want), func(t *testing.T) {
			var out bytes.Buffer
			_ = finishRun(&out, ensembleSummary{Outcome: tc.outcome, DiffStat: tc.diff, HeadMoved: tc.moved, Worktree: tc.wt})
			lines := strings.Split(strings.TrimPrefix(out.String(), "\n"), "\n")
			has := len(lines) > 1 && lines[1] == "  changes   "+tc.wt
			if has != tc.want {
				t.Errorf("summary = %q, want worktree line = %t", out.String(), tc.want)
			}
		})
	}
}

func TestARunThatErrorsAfterTheWorktreeWasMadeSaysWhereItIs(t *testing.T) {
	f := runBaseFixture(t, false)
	setBaseConfig(t, f, "stack", "[run]\nrounds = 0\n")
	var runErr error
	stderr := captureRunStderr(t, func() {
		_, runErr = captureStdout(t, func() error { return runCommand(append(f.args, "-base", "stack")) })
	})
	dirs, err := filepath.Glob(filepath.Join(f.tmp, "wt-km-*"))
	if err != nil || len(dirs) != 1 {
		t.Fatalf("worktrees %v, error %v", dirs, err)
	}
	wt, err := filepath.EvalSymlinks(dirs[0])
	if err != nil {
		t.Fatal(err)
	}
	if runErr == nil || !strings.Contains(stderr, "km: the worktree was kept at "+wt) {
		t.Fatalf("error = %v, stderr = %s", runErr, stderr)
	}
}

func TestADefaultRunMakesNoLessonsStep(t *testing.T) {
	f := runBaseFixture(t, false)
	requireRunBase(t, f, "main", f.main)
	dir, result := onlyRunResult(t)
	if _, ok := result["lessons"]; ok {
		t.Error("default run recorded lessons")
	}
	if _, err := os.Stat(filepath.Join(dir, "lessons.json")); !os.IsNotExist(err) {
		t.Errorf("lessons file: %v", err)
	}
	for _, step := range recordedSteps(t, dir) {
		if step.Step == "lessons" {
			t.Error("default run made a lessons step")
		}
	}
}

func TestARunUsingThisRepositorysConfigMakesALessonsStep(t *testing.T) {
	f := runBaseFixture(t, false)
	data, err := os.ReadFile(filepath.Join("../..", loop.ConfigFile))
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(f.repo, loop.ConfigFile), string(data))
	args := append(f.args, "-worktree", f.repo, "-verify", "true", "-preflight-timeout", "0", "-reply-grace", "1ms", "-sizing", "off")
	out, err := captureStdout(t, func() error { return runCommand(args) })
	if err != nil {
		t.Fatalf("run = %v\n%s", err, out)
	}
	dir, result := onlyRunResult(t)
	if _, ok := result["lessons"]; !ok {
		t.Error("repository run omitted lessons result")
	}
	found := false
	for _, step := range recordedSteps(t, dir) {
		if step.Step == "lessons" {
			found = true
		}
	}
	if !found {
		t.Error("repository run made no lessons step")
	}
}

func TestAStaleOriginsDefaultBranchFailsWithAHintToChooseABase(t *testing.T) {
	f := runBaseFixture(t, false)
	safetyGit(t, f.repo, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/gone")
	err := runCommand(f.args)
	if err == nil || !strings.Contains(err.Error(), `branch "gone" does not exist on remote "origin"`) || !strings.Contains(err.Error(), "-base") {
		t.Fatalf("error = %v", err)
	}
	requireNoBaseRun(t, f)
}

func TestTheBaseConfigIsReadFromTheDefaultCommitAndIsNotReadAgainFromTheChosenBranch(t *testing.T) {
	f := runBaseFixture(t, false)
	stack := setBaseConfig(t, f, "stack", strings.Replace(baseConfig, "[run]", "[run]\nbase = 'missing'", 1))
	setBaseConfig(t, f, "main", strings.Replace(baseConfig, "[run]", "[run]\nbase = 'stack'", 1))
	writeFile(t, filepath.Join(f.repo, loop.ConfigFile), strings.Replace(baseConfig, "[run]", "[run]\nbase = 'missing'", 1))
	requireRunBase(t, f, "stack", stack)
}
