package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"
)

type proofReport struct {
	Worktree, Command, Status, Reason string
	ExitStatus                        int `json:"exit_status"`
	Runs                              []struct {
		Name, Status, Output string
		ExitCode             int   `json:"exit_code"`
		DurationMS           int64 `json:"duration_ms"`
	}
	Breaks []struct {
		Patch, Status, Output, Reason string
		ExitCode                      int `json:"exit_code"`
	}
}

func proofGit(t *testing.T, root string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
	cmd.Env = nil
	for _, value := range os.Environ() {
		name, _, _ := strings.Cut(value, "=")
		if name != "GIT_DIR" && name != "GIT_WORK_TREE" && name != "GIT_INDEX_FILE" && name != "GIT_ALTERNATE_OBJECT_DIRECTORIES" {
			cmd.Env = append(cmd.Env, value)
		}
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %s: %v", args, out, err)
	}
	return string(out)
}

func proofFixture(t *testing.T, command string) string {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	root := initFixture(t, map[string]string{"file": "ok\n", "other": "old\n", ".gitignore": "ignored\n"})
	proofGit(t, root, "init", "-q")
	proofGit(t, root, "add", ".")
	proofGit(t, root, "-c", "user.name=t", "-c", "user.email=t@example.invalid", "-c", "maintenance.auto=false", "-c", "gc.auto=0", "commit", "-qm", "fixture")
	if command != "" {
		proofWrite(t, root, ".kapellmeister/config.toml", "[test]\nverify = "+fmt.Sprintf("%q", command)+"\n", 0644)
	}
	return root
}

func proofWrite(t *testing.T, root, name, body string, mode os.FileMode) {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
}

func proofCall(t *testing.T, root string, args ...string) (int, string) {
	t.Helper()
	output, err := os.CreateTemp(t.TempDir(), "output-")
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	old := os.Stdout
	os.Stdout = output
	defer func() { os.Stdout = old }()
	var stderr bytes.Buffer
	code := execute(append([]string{"prove", "-worktree", root}, args...), &stderr)
	data, err := os.ReadFile(output.Name())
	if err != nil {
		t.Fatal(err)
	}
	return code, string(data) + stderr.String()
}

func proofJSON(t *testing.T, root string, args ...string) proofReport {
	t.Helper()
	code, out := proofCall(t, root, append(args, "-json")...)
	var report proofReport
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("exit %d: %s: %v", code, out, err)
	}
	if code != report.ExitStatus {
		t.Fatal(code, report)
	}
	return report
}

func proofPatch(t *testing.T, name, from, to string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "break.patch")
	patch := fmt.Sprintf("diff --git a/%s b/%s\n--- a/%s\n+++ b/%s\n@@ -1 +1 @@\n-%s\n+%s\n", name, name, name, name, from, to)
	if err := os.WriteFile(path, []byte(patch), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestProveRefusesAVerifyThatIsStillTheStub(t *testing.T) {
	root := proofFixture(t, "")
	if out, err := initCall(t, root); err != nil {
		t.Fatal(out, err)
	}
	code, out := proofCall(t, root)
	if code != 10 || !strings.Contains(out, "unfinished") {
		t.Fatal(code, out)
	}
}

func TestProveRefusesWhenVerifyIsNotConfigured(t *testing.T) {
	r := proofJSON(t, proofFixture(t, ""))
	if r.ExitStatus != 10 || r.Status != "refused" || !strings.Contains(r.Reason, "not configured") {
		t.Fatal(r)
	}
}

func TestProveRefusesWhenTheDirectoryIsNotACheckoutOrHasNoCommit(t *testing.T) {
	for _, initialized := range []bool{false, true} {
		root := t.TempDir()
		if initialized {
			proofGit(t, root, "init", "-q")
		}
		proofWrite(t, root, ".kapellmeister/config.toml", "[test]\nverify = 'true'\n", 0644)
		r := proofJSON(t, root)
		if r.ExitStatus != 10 || r.Status != "refused" {
			t.Fatal(r)
		}
	}
	root := proofFixture(t, "true")
	sub := filepath.Join(root, "sub")
	if err := os.Mkdir(sub, 0755); err != nil {
		t.Fatal(err)
	}
	if r := proofJSON(t, sub); r.ExitStatus != 10 {
		t.Fatal(r)
	}
}

func TestProvePassesTwiceOnCleanCopiesAndReportsEachDuration(t *testing.T) {
	root := proofFixture(t, "test ! -e ran && touch ran && sleep 0.01 && grep -q ok file")
	r := proofJSON(t, root)
	if r.ExitStatus != 11 || len(r.Runs) != 2 {
		t.Fatal(r)
	}
	for i, run := range r.Runs {
		if run.Status != "passed" || run.DurationMS <= 0 || run.Name != fmt.Sprintf("clean run %d", i+1) {
			t.Fatal(run)
		}
	}
}

func TestProveReportsAVerifyThatPassesOnlyOnceAsFlaky(t *testing.T) {
	state := filepath.Join(t.TempDir(), "state")
	t.Setenv("KM_PROOF_STATE", state)
	root := proofFixture(t, `if test -e "$KM_PROOF_STATE"; then exit 7; fi; touch "$KM_PROOF_STATE"`)
	r := proofJSON(t, root, "-break", proofPatch(t, "file", "ok", "bad"))
	if r.ExitStatus != 12 || r.Status != "flaky" || len(r.Runs) != 2 || r.Runs[0].ExitCode != 0 || r.Runs[1].ExitCode != 7 || r.Breaks[0].Status != "skipped" {
		t.Fatal(r)
	}
}

func TestProveFailsAVerifyThatFailsOnACleanCopyAndBoundsItsOutput(t *testing.T) {
	root := proofFixture(t, `i=0; while [ "$i" -lt 500 ]; do echo "line-$i"; i=$((i+1)); done; exit 8`)
	r := proofJSON(t, root)
	if r.ExitStatus != 12 || r.Status != "failed" || len(r.Runs) != 2 {
		t.Fatal(r)
	}
	for _, run := range r.Runs {
		if run.ExitCode != 8 || strings.Count(run.Output, "\n") != 40 || !strings.HasPrefix(run.Output, "line-460\n") {
			t.Fatal(run)
		}
	}
}

func TestProveProvesWhenEveryBreakPatchMakesVerifyFail(t *testing.T) {
	root := proofFixture(t, "grep -q '^ok$' file")
	r := proofJSON(t, root, "-break", proofPatch(t, "file", "ok", "broken"), "-break", proofPatch(t, "file", "ok", "also broken"))
	if r.ExitStatus != 0 || r.Status != "proved" || len(r.Breaks) != 2 {
		t.Fatal(r)
	}
	for _, b := range r.Breaks {
		if b.Status != "breaks" || b.ExitCode == 0 {
			t.Fatal(b)
		}
	}
}

func TestProveReportsABreakPatchThatLeavesVerifyPassingAndExitsWith13(t *testing.T) {
	root := proofFixture(t, "grep -q ok file")
	patch := proofPatch(t, "other", "old", "new")
	r := proofJSON(t, root, "-break", patch, "-break", proofPatch(t, "file", "ok", "bad"))
	if r.ExitStatus != 13 || r.Status != "survived" || len(r.Breaks) != 2 || r.Breaks[0].Status != "survives" || r.Breaks[1].Status != "breaks" || !strings.Contains(r.Reason, patch) {
		t.Fatal(r)
	}
}

func TestProveRefusesAPatchThatDoesNotApplyAndNeverCountsItAsAPass(t *testing.T) {
	root := proofFixture(t, "true")
	for _, patch := range []string{proofPatch(t, "file", "absent", "bad"), filepath.Join(t.TempDir(), "missing")} {
		r := proofJSON(t, root, "-break", patch)
		if r.ExitStatus != 10 || r.Status != "refused" || !strings.Contains(r.Reason, patch) {
			t.Fatal(r)
		}
	}
	empty := filepath.Join(t.TempDir(), "empty.patch")
	if err := os.WriteFile(empty, nil, 0644); err != nil {
		t.Fatal(err)
	}
	if r := proofJSON(t, root, "-break", empty); r.ExitStatus != 10 {
		t.Fatal(r)
	}
}

func TestProveRefusesAPatchThatTouchesKapellmeisterOrScriptsVerify(t *testing.T) {
	patches := []string{
		"diff --git a/scripts/verify b/scripts/verify\n--- a/scripts/verify\n+++ b/scripts/verify\n@@ -1 +1 @@\n-true\n+false\n",
		"diff --git a/.kapellmeister/config.toml b/.kapellmeister/config.toml\n--- a/.kapellmeister/config.toml\n+++ b/.kapellmeister/config.toml\n@@ -1,2 +1,2 @@\n [test]\n-verify = 'scripts/verify'\n+verify = 'false'\n",
		"diff --git a/.kapellmeister/new b/.kapellmeister/new\nnew file mode 100644\n--- /dev/null\n+++ b/.kapellmeister/new\n@@ -0,0 +1 @@\n+new\n",
		"diff --git a/.kapellmeister/old b/.kapellmeister/old\ndeleted file mode 100644\n--- a/.kapellmeister/old\n+++ /dev/null\n@@ -1 +0,0 @@\n-old\n",
		"diff --git a/other b/.kapellmeister/renamed\nsimilarity index 100%\nrename from other\nrename to .kapellmeister/renamed\n",
		"diff --git a/scripts/verify b/scripts/verify\nold mode 100755\nnew mode 100644\n",
	}
	for i, patch := range patches {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			root := proofFixture(t, "")
			proofWrite(t, root, "scripts/verify", "true\n", 0755)
			proofWrite(t, root, ".kapellmeister/config.toml", "[test]\nverify = 'scripts/verify'\n", 0644)
			proofWrite(t, root, ".kapellmeister/old", "old\n", 0644)
			proofGit(t, root, "add", ".")
			proofGit(t, root, "-c", "user.name=t", "-c", "user.email=t@example.invalid", "commit", "-qm", "setup")
			if i == 5 {
				proofGit(t, root, "checkout", "--detach", "-q")
			}
			name := filepath.Join(t.TempDir(), "setup.patch")
			if err := os.WriteFile(name, []byte(patch), 0644); err != nil {
				t.Fatal(err)
			}
			r := proofJSON(t, root, "-break", name)
			if r.ExitStatus != 10 || !strings.Contains(r.Reason, "setup") || len(r.Breaks) != 1 || r.Breaks[0].Status != "refused" {
				t.Fatal(r)
			}
		})
	}
}

func TestProveWithoutABreakPatchPassesButIsNotProvedAndSaysABreakPatchIsNeeded(t *testing.T) {
	root := proofFixture(t, "true")
	code, out := proofCall(t, root)
	if code != 11 || !strings.Contains(out, "passes, not proved") || !strings.Contains(out, "-break PATCH") || !strings.Contains(out, "clean run 2: passed") {
		t.Fatal(code, out)
	}
}

func TestProveRunsVerifyInACopyWithoutUntrackedOrIgnoredFiles(t *testing.T) {
	for _, name := range []string{"untracked", "ignored"} {
		root := proofFixture(t, "test -f "+name)
		proofWrite(t, root, name, "present", 0644)
		r := proofJSON(t, root)
		if r.ExitStatus != 12 {
			t.Fatal(r)
		}
	}
}

func TestProveUsesTheUncommittedConfigAndVerifyScript(t *testing.T) {
	root := proofFixture(t, "false")
	proofWrite(t, root, "scripts/verify", "#!/bin/sh\ngrep -q ok file\n", 0755)
	proofWrite(t, root, ".kapellmeister/config.toml", "[test]\nverify = 'scripts/verify'\n", 0644)
	proofGit(t, root, "checkout", "--detach", "-q")
	if r := proofJSON(t, root, "-break", proofPatch(t, "file", "ok", "bad")); r.ExitStatus != 0 {
		t.Fatal(r)
	}
}

func TestProveLeavesTheCheckoutUnchangedAndRemovesItsScratchCopies(t *testing.T) {
	for _, command := range []string{"echo change > file; touch new-file; true", "false", "true"} {
		root := proofFixture(t, command)
		scratch := t.TempDir()
		t.Setenv("TMPDIR", scratch)
		before := initSnapshot(t, root)
		args := []string{}
		if command == "true" {
			args = []string{"-break", proofPatch(t, "file", "missing", "bad")}
		}
		proofJSON(t, root, args...)
		if !reflect.DeepEqual(before, initSnapshot(t, root)) {
			t.Fatal("checkout changed")
		}
		entries, err := os.ReadDir(scratch)
		if err != nil || len(entries) != 0 {
			t.Fatal(entries, err)
		}
	}
}

func TestProveTimesOutAHungVerifyWithItsOwnStatus(t *testing.T) {
	root := proofFixture(t, "sleep 30 & wait")
	scratch := t.TempDir()
	t.Setenv("TMPDIR", scratch)
	r := proofJSON(t, root, "-timeout", "30ms")
	if r.ExitStatus != 3 || r.Status != "timeout" || len(r.Runs) != 1 {
		t.Fatal(r)
	}
	entries, err := os.ReadDir(scratch)
	if err != nil || len(entries) != 0 {
		t.Fatal(entries, err)
	}
}

func TestProveNeedsNoAgentsCAOOrUserConfig(t *testing.T) {
	home := initFixture(t, map[string]string{"config.toml": "invalid TOML ["})
	t.Setenv("KAPELLMEISTER_HOME", home)
	t.Setenv("CAO_API_BASE_URL", "http://127.0.0.1:1")
	r := proofJSON(t, proofFixture(t, "true"))
	if r.ExitStatus != 11 {
		t.Fatal(r)
	}
}

func TestProveIsListedInTheUsage(t *testing.T) {
	if !strings.Contains(usage, "km prove [-worktree DIR]") {
		t.Fatal("prove missing from usage")
	}
}

func TestProveRejectsInvalidFlags(t *testing.T) {
	for _, args := range [][]string{{"-timeout", "0"}, {"-timeout", "-1s"}, {"extra"}, {"-unknown"}} {
		code, _ := proofCall(t, t.TempDir(), args...)
		if code != 1 {
			t.Fatal(args, code)
		}
	}
}

func TestProveRemovesGeneratedWrapperLogsAlongWithItsScratchCopies(t *testing.T) {
	root := proofFixture(t, "")
	if out, err := initCall(t, root, "-verify", "grep -q ok file"); err != nil {
		t.Fatal(out, err)
	}
	scratch := t.TempDir()
	t.Setenv("TMPDIR", scratch)
	r := proofJSON(t, root, "-break", proofPatch(t, "file", "ok", "bad"))
	if r.ExitStatus != 0 {
		t.Fatal(r)
	}
	entries, err := os.ReadDir(scratch)
	if err != nil || len(entries) != 0 {
		t.Fatal(entries, err)
	}
}

func TestProveRemovesItsScratchCopiesWhenInterrupted(t *testing.T) {
	started := filepath.Join(t.TempDir(), "started")
	t.Setenv("KM_PROOF_STARTED", started)
	root := proofFixture(t, `touch "$KM_PROOF_STARTED"; (sleep 1; touch "$KM_PROOF_STARTED.survived") & wait`)
	scratch := t.TempDir()
	t.Setenv("TMPDIR", scratch)
	cmd := kmSubprocess("prove", "-worktree", root, "-json")
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	finished := false
	t.Cleanup(func() {
		if !finished {
			_ = cmd.Process.Kill()
			<-done
		}
	})
	eventually(t, func() bool { _, err := os.Stat(started); return err == nil })
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		finished = true
		if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 1 || !strings.Contains(out.String(), "interrupted") {
			t.Fatal(err, out.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("prove did not stop")
	}
	entries, err := os.ReadDir(scratch)
	if err != nil || len(entries) != 0 {
		t.Fatal(entries, err)
	}
	time.Sleep(1100 * time.Millisecond)
	if _, err := os.Stat(started + ".survived"); !os.IsNotExist(err) {
		t.Fatal("verify child survived", err)
	}
}

func TestProveIgnoresInheritedGitPathsInBothCopyingAndVerification(t *testing.T) {
	root := proofFixture(t, `test "$(git rev-parse --show-toplevel)" = "$PWD" && git status --porcelain > status`)
	t.Setenv("GIT_DIR", filepath.Join(root, ".git"))
	t.Setenv("GIT_WORK_TREE", root)
	t.Setenv("GIT_INDEX_FILE", filepath.Join(root, ".git/index"))
	before := initSnapshot(t, root)
	r := proofJSON(t, root)
	if r.ExitStatus != 11 || !reflect.DeepEqual(before, initSnapshot(t, root)) {
		t.Fatal(r)
	}
}

func TestProveRefusesScratchCopiesInsideTheCheckout(t *testing.T) {
	root := proofFixture(t, "true")
	temp := filepath.Join(root, "temp")
	if err := os.Mkdir(temp, 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", temp)
	before := initSnapshot(t, root)
	r := proofJSON(t, root)
	if r.ExitStatus != 10 || !strings.Contains(r.Reason, "outside") || !reflect.DeepEqual(before, initSnapshot(t, root)) {
		t.Fatal(r)
	}
	entries, err := os.ReadDir(temp)
	if err != nil || len(entries) != 0 {
		t.Fatal(entries, err)
	}
}

func TestProveGivesEveryRunItsOwnTemporaryDirectory(t *testing.T) {
	root := proofFixture(t, `test ! -e "$TMPDIR/leftover" && touch "$TMPDIR/leftover"`)
	r := proofJSON(t, root)
	if r.ExitStatus != 11 {
		t.Fatal(r)
	}
}

func TestProveJSONCarriesTheSameFactsAsTheText(t *testing.T) {
	root := proofFixture(t, "grep -q ok file")
	patch := proofPatch(t, "file", "ok", "bad")
	code, text := proofCall(t, root, "-break", patch)
	r := proofJSON(t, root, "-break", patch)
	if code != r.ExitStatus || !strings.Contains(text, r.Reason) {
		t.Fatal(code, text, r)
	}
	for _, run := range r.Runs {
		if !strings.Contains(text, run.Name+": "+run.Status) {
			t.Fatal(text, run)
		}
	}
	for _, b := range r.Breaks {
		if !strings.Contains(text, "break "+b.Patch+": "+b.Status) || !strings.Contains(text, fmt.Sprintf("exit %d", b.ExitCode)) {
			t.Fatal(text, b)
		}
	}
}

func TestProveNeverCountsATimedOutBreakAsProved(t *testing.T) {
	root := proofFixture(t, "if grep -q ok file; then true; else sleep 30 & wait; fi")
	r := proofJSON(t, root, "-break", proofPatch(t, "file", "ok", "bad"), "-timeout", "100ms")
	if r.ExitStatus != 3 || r.Status != "timeout" || len(r.Breaks) != 1 || r.Breaks[0].Status != "timeout" {
		t.Fatal(r)
	}
}

func TestProveRefusalTakesPrecedenceOverAPatchThatSurvived(t *testing.T) {
	root := proofFixture(t, "true")
	r := proofJSON(t, root, "-break", proofPatch(t, "other", "old", "new"), "-break", proofPatch(t, "file", "missing", "bad"))
	if r.ExitStatus != 10 || r.Status != "refused" || r.Breaks[0].Status != "survives" || r.Breaks[1].Status != "refused" {
		t.Fatal(r)
	}
}

func TestProveAcceptsADetachedWorktreeAndLeavesItsGitMetadataUnchanged(t *testing.T) {
	root := proofFixture(t, "")
	worktree := filepath.Join(t.TempDir(), "worktree")
	proofGit(t, root, "worktree", "add", "--detach", worktree, "HEAD")
	proofWrite(t, worktree, ".kapellmeister/config.toml", "[test]\nverify = 'grep -q ok file'\n", 0644)
	sourceBefore, before := initSnapshot(t, root), initSnapshot(t, worktree)
	r := proofJSON(t, worktree, "-break", proofPatch(t, "file", "ok", "bad"))
	if r.ExitStatus != 0 || !reflect.DeepEqual(sourceBefore, initSnapshot(t, root)) || !reflect.DeepEqual(before, initSnapshot(t, worktree)) {
		t.Fatal(r)
	}
}

func TestProveRefusesReplacingTheVerifyDirectoryWithASymlink(t *testing.T) {
	root := proofFixture(t, "")
	proofWrite(t, root, "scripts/verify", "true\n", 0755)
	proofWrite(t, root, "replacement/verify", "true\n", 0755)
	proofWrite(t, root, ".kapellmeister/config.toml", "[test]\nverify = 'scripts/verify'\n", 0644)
	proofGit(t, root, "add", ".")
	proofGit(t, root, "-c", "user.name=t", "-c", "user.email=t@example.invalid", "commit", "-qm", "setup")
	patch := `diff --git a/scripts/verify b/scripts/verify
deleted file mode 100755
--- a/scripts/verify
+++ /dev/null
@@ -1 +0,0 @@
-true
diff --git a/scripts b/scripts
new file mode 120000
--- /dev/null
+++ b/scripts
@@ -0,0 +1 @@
+replacement
\ No newline at end of file
`
	path := filepath.Join(t.TempDir(), "symlink.patch")
	if err := os.WriteFile(path, []byte(patch), 0644); err != nil {
		t.Fatal(err)
	}
	r := proofJSON(t, root, "-break", path)
	if r.ExitStatus != 10 || r.Breaks[0].Status != "refused" {
		t.Fatal(r)
	}
}
