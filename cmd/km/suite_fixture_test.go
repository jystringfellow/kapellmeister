package main

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jystringfellow/kapellmeister/internal/cao"
	"github.com/jystringfellow/kapellmeister/internal/initrepo"
	"github.com/jystringfellow/kapellmeister/internal/loop"
)

const suiteSlowSleep = "0.5"

func suiteFixture(t *testing.T) string {
	t.Helper()
	root := tuneFixture(t)
	proofWrite(t, root, "answer.sh", "answer() { echo 42; }\nlegacy() { echo 0; }\n", 0644)
	proofWrite(t, root, "suites/fast.sh", ". ./answer.sh\ntest \"$(answer)\" = 42\n", 0644)
	proofWrite(t, root, "suites/slow.sh", "sleep "+suiteSlowSleep+"\n. ./answer.sh\ntest \"$(answer)\" -eq 42\n", 0644)
	proofWrite(t, root, "suites/network.sh", "test -n \"$SUITE_NETWORK\"\n", 0644)
	proofWrite(t, root, "suites/known.sh", ". ./answer.sh\ntest \"$(legacy)\" = 1\n", 0644)
	proofWrite(t, root, "suites/flaky.sh", "if test -e \"$SUITE_FLAKY_STATE\"; then exit 0; fi\n: >\"$SUITE_FLAKY_STATE\"\nexit 1\n", 0644)
	var ci strings.Builder
	ci.WriteString("jobs:\n  test:\n    steps:\n")
	for _, suite := range []string{"fast", "slow", "network", "known", "flaky"} {
		fmt.Fprintf(&ci, "      - run: sh suites/%s.sh\n", suite)
	}
	proofWrite(t, root, ".github/workflows/ci.yml", ci.String(), 0644)
	proofGit(t, root, "add", "answer.sh", "suites", ".github")
	proofGit(t, root, "-c", "user.name=t", "-c", "user.email=t@example.invalid", "commit", "-qm", "suites")
	return root
}

func suiteScript(suites ...string) string {
	script, _ := initrepo.RenderVerify("true")
	var steps strings.Builder
	for _, suite := range suites {
		fmt.Fprintf(&steps, "run '%s' 'sh suites/%s.sh'\n", suite, suite)
	}
	return strings.Replace(script, "run 'verify' 'true'\n", steps.String(), 1)
}

func suiteConfig(extra string) string {
	return initrepo.RenderConfig(initrepo.ScriptFile, "") + extra
}

func suiteWriter(config string, suites ...string) func(*testing.T, cao.StepRequest, string) string {
	return func(t *testing.T, req cao.StepRequest, patchDir string) string {
		t.Helper()
		proofWrite(t, req.WorkingDirectory, initrepo.ScriptFile, suiteScript(suites...), 0755)
		proofWrite(t, req.WorkingDirectory, loop.ConfigFile, suiteConfig(config), 0644)
		proofWrite(t, patchDir, "source.patch", "diff --git a/answer.sh b/answer.sh\n--- a/answer.sh\n+++ b/answer.sh\n@@ -1,2 +1,2 @@\n-answer() { echo 42; }\n+answer() { echo 0; }\n legacy() { echo 0; }\n", 0644)
		proofWrite(t, patchDir, "test.patch", "diff --git a/suites/fast.sh b/suites/fast.sh\n--- a/suites/fast.sh\n+++ b/suites/fast.sh\n@@ -1,2 +1,2 @@\n . ./answer.sh\n-test \"$(answer)\" = 42\n+test \"$(answer)\" = 0\n", 0644)
		return fmt.Sprintf(`{"command":%q,"left_out":[],"patches":[{"file":%q,"kind":"source"},{"file":%q,"kind":"test"}]}`, "sh suites/"+strings.Join(suites, ".sh && sh suites/")+".sh", filepath.Join(patchDir, "source.patch"), filepath.Join(patchDir, "test.patch"))
	}
}
