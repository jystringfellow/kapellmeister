package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jystringfellow/kapellmeister/internal/loop"
)

type baseFixture struct {
	repo, origin, main, stack, tmp string
	args                           []string
	cao                            *runCAO
}

func runBaseFixture(t *testing.T, singleBranch bool) baseFixture {
	t.Helper()
	t.Setenv(loop.HomeEnv, t.TempDir())
	t.Setenv(loop.StateEnv, t.TempDir())
	t.Setenv(loop.CAOURLEnv, "")
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	seed := t.TempDir()
	safetyGit(t, seed, "init", "-q", "-b", "main")
	safetyGit(t, seed, "config", "user.name", "Test")
	safetyGit(t, seed, "config", "user.email", "test@example.invalid")
	writeFile(t, filepath.Join(seed, ".gitignore"), ".kapellmeister/\n")
	writeFile(t, filepath.Join(seed, loop.ConfigFile), "[test]\nverify = 'true'\n[run]\npreflight_timeout = '0'\nreply_grace = '1ms'\nrounds = 2\n[sizing]\nmode = 'off'\n")
	safetyGit(t, seed, "add", ".gitignore")
	safetyGit(t, seed, "add", "--force", loop.ConfigFile)
	safetyGit(t, seed, "-c", "commit.gpgsign=false", "commit", "-qm", "main fixture")
	main := safetyGit(t, seed, "rev-parse", "HEAD")
	writeFile(t, filepath.Join(seed, "stacked.txt"), "stacked work\n")
	safetyGit(t, seed, "add", "stacked.txt")
	safetyGit(t, seed, "-c", "commit.gpgsign=false", "commit", "-qm", "stack fixture")
	stack := safetyGit(t, seed, "rev-parse", "HEAD")
	origin := filepath.Join(t.TempDir(), "origin.git")
	safetyGit(t, seed, "clone", "-q", "--bare", seed, origin)
	safetyGit(t, origin, "update-ref", "refs/heads/stack", stack)
	safetyGit(t, origin, "update-ref", "refs/heads/main", main)
	repo := filepath.Join(t.TempDir(), "repo")
	clone := []string{"clone", "-q", "-b", "main"}
	if singleBranch {
		clone = append(clone, "--single-branch")
	}
	safetyGit(t, seed, append(clone, origin, repo)...)
	f := &runCAO{}
	args := []string{"-repo", repo, "-task", "probe", "-cao", f.serve(t).URL}
	args = append(args, runRoleArgs()...)
	return baseFixture{repo: repo, origin: origin, main: main, stack: stack, tmp: tmp, args: args, cao: f}
}

func requireRunBase(t *testing.T, f baseFixture, branch, commit string, extra ...string) {
	t.Helper()
	out, err := captureStdout(t, func() error { return runCommand(append(f.args, extra...)) })
	if err != nil {
		t.Fatalf("run: %v\n%s", err, out)
	}
	_, result := onlyRunResult(t)
	wt, _ := result["worktree"].(string)
	if head := safetyGit(t, wt, "rev-parse", "HEAD"); head != commit {
		t.Errorf("HEAD = %s, want %s", head, commit)
	}
	if result["base_branch"] != branch || result["base"] != commit {
		t.Errorf("recorded base = %v @ %v, want %s @ %s", result["base_branch"], result["base"], branch, commit)
	}
	if !strings.Contains(out, "  base      "+branch+" @ "+commit+"\n") {
		t.Errorf("opening output lacks base: %s", out)
	}
	_, err = os.Stat(filepath.Join(wt, "stacked.txt"))
	if commit == f.main && !os.IsNotExist(err) {
		t.Errorf("default worktree contains stacked work: %v", err)
	} else if commit != f.main && err != nil {
		t.Errorf("stacked work missing: %v", err)
	}
	if branch != "main" && commit == f.main {
		t.Error("explicit base resolved to the default branch")
	}
}

func TestRunWithoutBaseStartsTheWorktreeFromTheRemotesDefaultBranch(t *testing.T) {
	f := runBaseFixture(t, false)
	requireRunBase(t, f, "main", f.main)
}

func TestRunWithBaseStartsTheWorktreeFromThatBranchesTipAndNotTheDefaults(t *testing.T) {
	f := runBaseFixture(t, false)
	commit := safetyGit(t, f.origin, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "-c", "commit.gpgsign=false", "commit-tree", f.stack+"^{tree}", "-p", f.stack, "-m", "updated stack tip")
	safetyGit(t, f.origin, "update-ref", "refs/heads/stack", commit)
	requireRunBase(t, f, "stack", commit, "-base", "stack")
}

func TestRunWithBaseFetchesABranchPushedAfterTheClone(t *testing.T) {
	for _, single := range []bool{false, true} {
		t.Run(fmt.Sprintf("single_branch_%t", single), func(t *testing.T) {
			f := runBaseFixture(t, single)
			safetyGit(t, f.origin, "update-ref", "refs/heads/late", f.stack)
			requireRunBase(t, f, "late", f.stack, "-base", "late")
		})
	}
}

func requireNoBaseRun(t *testing.T, f baseFixture) {
	t.Helper()
	if len(f.cao.created) != 0 || len(f.cao.fake.requests) != 0 || len(f.cao.fake.checked) != 0 || len(f.cao.fake.unexpected) != 0 {
		t.Error("CAO contacted before base refusal")
	}
	if trees := safetyGit(t, f.repo, "worktree", "list", "--porcelain"); strings.Count(trees, "worktree ") != 1 {
		t.Errorf("worktrees left behind: %s", trees)
	}
	if dirs, err := filepath.Glob(filepath.Join(f.tmp, "wt-km-*")); err != nil || len(dirs) != 0 {
		t.Errorf("temporary worktrees left behind: %v, %v", dirs, err)
	}
	if _, err := os.Stat(filepath.Join(loop.StateDir(), "runs")); !os.IsNotExist(err) {
		t.Errorf("run state created before refusal: %v", err)
	}
}

func TestRunWithAMissingBaseFailsNamingTheBranchAndRemoteBeforeAnyChairStarts(t *testing.T) {
	for _, branch := range []string{"nope", "origin/main", "tag-only", "tail"} {
		t.Run(branch, func(t *testing.T) {
			f := runBaseFixture(t, false)
			safetyGit(t, f.origin, "update-ref", "refs/tags/tag-only", f.stack)
			safetyGit(t, f.origin, "update-ref", "refs/heads/refs/heads/tail", f.stack)
			err := runCommand(append(f.args, "-base", branch))
			if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("branch %q does not exist on remote %q", branch, "origin")) {
				t.Errorf("missing branch refusal = %v", err)
			}
			requireNoBaseRun(t, f)
		})
	}
}

func TestRunRefusesABaseNameGitWouldNotAcceptOrCouldReadAsAnOption(t *testing.T) {
	for _, branch := range []string{"-x", "--upload-pack=touch x", "-", "", "a\x00b", "a\nb", "a\x1bb", "a\u200bb", "a\u202eb", "a\u00a0b", "a\u2028b", "a\ue000b", "a b", "a..b", "a~b", "a:b", "x.lock", "@", "HEAD", ".x", "x/.y", "x/", "x.", "x//y", "x@{y", "x\\y", "x?y", "x[y", "x*y", "x^y"} {
		t.Run(fmt.Sprintf("%q", branch), func(t *testing.T) {
			err := runCommand([]string{"-task", "probe", "-repo", "/nonexistent-km-repository", "-base=" + branch})
			if err == nil || !strings.HasPrefix(err.Error(), "-base ") || !strings.Contains(err.Error(), fmt.Sprintf("%q", branch)) {
				t.Fatalf("invalid base refusal = %v", err)
			}
			if strings.ContainsAny(err.Error(), "\x00\n\x1b") {
				t.Errorf("raw control characters in error: %q", err.Error())
			}
		})
	}
}

func TestValidBaseBranchAcceptsOrdinaryBranchNames(t *testing.T) {
	for _, branch := range []string{"main", "feature/x", "release-1.2", "stack_2", "ünï"} {
		t.Run(branch, func(t *testing.T) {
			f := runBaseFixture(t, true)
			if branch != "main" {
				safetyGit(t, f.origin, "update-ref", "refs/heads/"+branch, f.stack)
			}
			commit := f.stack
			if branch == "main" {
				commit = f.main
			}
			requireRunBase(t, f, branch, commit, "-base", branch)
		})
	}
}

func TestRunRefusesBaseTogetherWithWorktree(t *testing.T) {
	for _, worktree := range []string{".", ""} {
		t.Run(fmt.Sprintf("%q", worktree), func(t *testing.T) {
			f := runBaseFixture(t, false)
			if worktree != "" {
				worktree = f.repo
			}
			err := runCommand(append(f.args, "-base", "stack", "-worktree="+worktree))
			if err == nil || !strings.Contains(err.Error(), "-base and -worktree do not go together") {
				t.Errorf("conflicting flags = %v", err)
			}
			requireNoBaseRun(t, f)
		})
	}
}

func TestAResultRecordsTheBaseBranchOnlyWhenTheWorktreeWasBuiltFromARepo(t *testing.T) {
	s := playStanding(t)
	result := recordedResult(t, recordRun(t, s.Worktree, "", plainChairs()))
	if value, ok := result["base_branch"]; ok {
		t.Errorf("worktree base branch = %v, want omitted", value)
	}
}

func TestAResultWrittenBeforeBaseBranchesWereRecordedStillReads(t *testing.T) {
	data := []byte(`{"run_id":"old","worktree":"/old/worktree","outcome":"approved","rounds":1,"base":"0123456789012345678901234567890123456789","diff_stat":"old diff\n"}`)
	var summary ensembleSummary
	var result loop.Result
	if err := json.Unmarshal(data, &summary); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	if summary.Result != result || summary.Base != "0123456789012345678901234567890123456789" {
		t.Fatalf("old record changed: %+v, %+v", summary, result)
	}
	var out bytes.Buffer
	if err := finishRun(&out, summary); err != nil || !strings.Contains(out.String(), "approved after 1 round(s)") || !strings.HasSuffix(out.String(), "old diff\n") {
		t.Fatalf("old result output = %q, %v", out.String(), err)
	}
}

func TestAFailedWorktreeCreationLeavesNoTemporaryDirectory(t *testing.T) {
	f := runBaseFixture(t, false)
	if _, err := addWorktree(f.repo, "missing-commit"); err == nil {
		t.Fatal("missing commit accepted")
	}
	requireNoBaseRun(t, f)
}
