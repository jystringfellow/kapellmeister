package main

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jystringfellow/kapellmeister/internal/cao"
	"github.com/jystringfellow/kapellmeister/internal/initrepo"
	"github.com/jystringfellow/kapellmeister/internal/loop"
)

const suiteSlowSleep = "0.5"

const suiteUnit = `. ./answer.sh
test_answer() { test "$(answer)" = 42; }
test_legacy() { test "$(legacy)" = 1; }
status=0
for t in ${*:-test_answer test_legacy}; do
 case " $SUITE_SKIP " in *" $t "*) continue ;; esac
 if $t; then echo "ok $t"; else echo "FAIL $t"; status=1; fi
done
exit $status
`

const suiteSourceBreak = "diff --git a/answer.sh b/answer.sh\n--- a/answer.sh\n+++ b/answer.sh\n@@ -1,2 +1,2 @@\n-answer() { echo 42; }\n+answer() { echo 0; }\n legacy() { echo 0; }\n"
const suiteTestBreak = "diff --git a/suites/fast.sh b/suites/fast.sh\n--- a/suites/fast.sh\n+++ b/suites/fast.sh\n@@ -1,2 +1,2 @@\n . ./answer.sh\n-test \"$(answer)\" = 42\n+test \"$(answer)\" = 0\n"
const suiteSlowOnlyBreak = "diff --git a/answer.sh b/answer.sh\n--- a/answer.sh\n+++ b/answer.sh\n@@ -2,2 +2,2 @@\n legacy() { echo 0; }\n-total() { echo 84; }\n+total() { echo 0; }\n"

const suiteHelperEnv = "KM_SUITE_HELPER"

func suiteHelper(mode string) int {
	fail := func(format string, args ...any) int {
		fmt.Fprintf(os.Stderr, format+"\n", args...)
		return 7
	}
	needsNetwork := func() error {
		for _, name := range []string{"http_proxy", "HTTP_PROXY"} {
			proxy, err := url.Parse(os.Getenv(name))
			if os.Getenv(name) == "" || err != nil {
				continue
			}
			conn, err := net.DialTimeout("tcp", proxy.Host, time.Second)
			if err != nil {
				return fmt.Errorf("proxy %s: %w", proxy.Host, err)
			}
			conn.Close()
		}
		conn, err := net.DialTimeout("tcp", "192.0.2.1:9", 300*time.Millisecond)
		if err == nil {
			conn.Close()
			return nil
		}
		if errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EACCES) || errors.Is(err, syscall.ENETUNREACH) {
			return err
		}
		return nil
	}
	switch mode {
	case "network":
		if err := needsNetwork(); err != nil {
			return fail("no route to a non-loopback address: %v", err)
		}
		return 0
	case "hang":
		if needsNetwork() != nil {
			time.Sleep(time.Minute)
		}
		return 0
	case "loopback":
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return fail("loopback listen: %v", err)
		}
		defer listener.Close()
		go func() {
			if conn, err := listener.Accept(); err == nil {
				conn.Write([]byte("ok"))
				conn.Close()
			}
		}()
		conn, err := net.DialTimeout("tcp", listener.Addr().String(), 2*time.Second)
		if err != nil {
			return fail("loopback dial: %v", err)
		}
		defer conn.Close()
		reply := make([]byte, 2)
		if _, err := conn.Read(reply); err != nil || string(reply) != "ok" {
			return fail("loopback read: %v", err)
		}
		return 0
	}
	return fail("unknown helper mode %q", mode)
}

func suiteHelperScript(mode string) string {
	return suiteHelperEnv + "=" + mode + " \"$(cat suites/helper.path)\"\n"
}

func suiteFixture(t *testing.T) string {
	t.Helper()
	root := tuneFixture(t)
	helper, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	proofWrite(t, root, "answer.sh", "answer() { echo 42; }\nlegacy() { echo 0; }\ntotal() { echo 84; }\n", 0644)
	proofWrite(t, root, "suites/fast.sh", ". ./answer.sh\ntest \"$(answer)\" = 42\n", 0644)
	proofWrite(t, root, "suites/slow.sh", "sleep "+suiteSlowSleep+"\n. ./answer.sh\ntest \"$(answer)\" -eq 42 && test \"$(total)\" = 84\n", 0644)
	proofWrite(t, root, "suites/helper.path", helper+"\n", 0644)
	proofWrite(t, root, "suites/network.sh", suiteHelperScript("network"), 0644)
	proofWrite(t, root, "suites/loopback.sh", suiteHelperScript("loopback"), 0644)
	proofWrite(t, root, "suites/hang.sh", suiteHelperScript("hang"), 0644)
	proofWrite(t, root, "suites/once.sh", "if test -e \"$SUITE_ONCE_STATE\"; then exit 0; fi\n: >\"$SUITE_ONCE_STATE\"\necho 'unrelated failure'\nexit 1\n", 0644)
	proofWrite(t, root, "suites/broken.sh", "echo 'always fails'\nexit 3\n", 0644)
	proofWrite(t, root, "suites/unit.sh", suiteUnit, 0644)
	proofWrite(t, root, "suites/flaky.sh", "if test -e \"$SUITE_FLAKY_STATE\"; then exit 0; fi\n: >\"$SUITE_FLAKY_STATE\"\nexit 1\n", 0644)
	var ci strings.Builder
	ci.WriteString("jobs:\n  test:\n    steps:\n")
	for _, suite := range []string{"fast", "slow", "network", "unit", "flaky"} {
		fmt.Fprintf(&ci, "      - run: sh suites/%s.sh\n", suite)
	}
	proofWrite(t, root, ".github/workflows/ci.yml", ci.String(), 0644)
	proofGit(t, root, "add", "answer.sh", "suites", ".github")
	proofGit(t, root, "-c", "user.name=t", "-c", "user.email=t@example.invalid", "commit", "-qm", "suites")
	return root
}

func suiteSteps(steps string) string {
	script, _ := initrepo.RenderVerify("true")
	return strings.Replace(script, "run 'verify' 'true'\n", steps, 1)
}

func suiteScript(suites ...string) string {
	var steps strings.Builder
	for _, suite := range suites {
		fmt.Fprintf(&steps, "run '%s' 'sh suites/%s.sh'\n", suite, suite)
	}
	return suiteSteps(steps.String())
}

func suiteConfig(extra string) string {
	return initrepo.RenderConfig(initrepo.ScriptFile, "") + extra
}

func suiteReply(t *testing.T, patchDir, command string) string {
	t.Helper()
	proofWrite(t, patchDir, "source.patch", suiteSourceBreak, 0644)
	proofWrite(t, patchDir, "test.patch", suiteTestBreak, 0644)
	patches := fmt.Sprintf(`{"file":%q,"kind":"source"},{"file":%q,"kind":"test"}`, filepath.Join(patchDir, "source.patch"), filepath.Join(patchDir, "test.patch"))
	if strings.Contains(command, "suites/slow.sh") {
		proofWrite(t, patchDir, "slow.patch", suiteSlowOnlyBreak, 0644)
		patches += fmt.Sprintf(`,{"file":%q,"kind":"source"}`, filepath.Join(patchDir, "slow.patch"))
	}
	return fmt.Sprintf(`{"command":%q,"left_out":[],"patches":[%s]}`, command, patches)
}

func suiteWriter(config string, suites ...string) func(*testing.T, cao.StepRequest, string) string {
	return func(t *testing.T, req cao.StepRequest, patchDir string) string {
		t.Helper()
		proofWrite(t, req.WorkingDirectory, initrepo.ScriptFile, suiteScript(suites...), 0755)
		proofWrite(t, req.WorkingDirectory, loop.ConfigFile, suiteConfig(config), 0644)
		return suiteReply(t, patchDir, "sh suites/"+strings.Join(suites, ".sh && sh suites/")+".sh")
	}
}
