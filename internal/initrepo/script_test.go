package initrepo

import (
	"context"
	"github.com/jystringfellow/kapellmeister/internal/loop"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTheStubIsRecognisedByItsMarkerAndNotByItsWording(t *testing.T) {
	stub := RenderStub()
	if !IsStub(stub) || !IsStub(StubMarker+"\necho different message\n") {
		t.Fatal(stub)
	}
	for _, script := range []string{strings.ReplaceAll(stub, StubMarker, ""), "# " + StubMarker, StubMarker + " extra", "echo '" + StubMarker + "'"} {
		if IsStub(script) {
			t.Fatal(script)
		}
	}
}

func TestTheStubRunsNoCommandExitsNonZeroAndSaysWhatAVerifyMustDo(t *testing.T) {
	script := RenderStub()
	for i, line := range strings.Split(script, "\n") {
		if i > 0 && strings.HasPrefix(strings.TrimSpace(line), "#") {
			t.Fatal(line)
		}
	}
	cmd := exec.Command("sh", "-n")
	cmd.Stdin = strings.NewReader(script)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatal(string(out), err)
	}
	path, logs := scriptFixture(t, script)
	out, err := exec.Command("sh", path).CombinedOutput()
	if err == nil {
		t.Fatal("stub passed", string(out))
	}
	for _, want := range []string{"not been written", "pass on a clean checkout", "fail when the code or a test is broken", "finish in reasonable time", "km prove"} {
		if !strings.Contains(string(out), want) {
			t.Fatal(string(out), want)
		}
	}
	entries, err := os.ReadDir(logs)
	if err != nil || len(entries) != 0 {
		t.Fatal(entries, err)
	}
	if strings.Contains(script, "run 'verify'") {
		t.Fatal("stub runs a command")
	}
}

func scriptFixture(t *testing.T, script string) (string, string) {
	t.Helper()
	root := fixture(t, map[string]string{"scripts/verify": script})
	logs := t.TempDir()
	t.Setenv("TMPDIR", logs)
	return filepath.Join(root, "scripts/verify"), logs
}

func TestTheVerifyScriptWrapsAnyCommandIncludingOneWithSingleQuotesAndRunsFromAnyWorkingDirectory(t *testing.T) {
	script, err := RenderVerify("printf '%s\\n' 'it works'; test -f scripts/verify")
	if err != nil {
		t.Fatal(err)
	}
	path, logs := scriptFixture(t, script)
	cmd := exec.Command("sh", path)
	cmd.Dir = t.TempDir()
	out, err := cmd.CombinedOutput()
	if err != nil || string(out) != "verify ok: verify\n" {
		t.Fatal(string(out), err)
	}
	entries, err := os.ReadDir(logs)
	if err != nil || len(entries) != 0 {
		t.Fatal(entries, err)
	}
	if _, err := RenderVerify("echo\x00"); err == nil {
		t.Fatal("accepted NUL")
	}
}

func TestTheVerifyScriptBoundsAFailureNamesTheStepAndPointsAtTheFullLog(t *testing.T) {
	script, err := RenderVerify(`i=0; while [ "$i" -lt 500 ]; do echo "$i failure output"; i=$((i+1)); done; exit 7`)
	if err != nil {
		t.Fatal(err)
	}
	path, logs := scriptFixture(t, script)
	out, err := exec.Command("sh", path).CombinedOutput()
	if err == nil || err.(*exec.ExitError).ExitCode() != 7 || !strings.Contains(string(out), "step: verify (exit 7)") || strings.Count(string(out), "\n") > 45 {
		t.Fatal(string(out), err)
	}
	_, log, _ := strings.Cut(string(out), "full log: ")
	log = strings.TrimSpace(log)
	if !strings.HasPrefix(log, logs+string(os.PathSeparator)) {
		t.Fatal(log)
	}
	full, err := os.ReadFile(log)
	if err != nil || !strings.HasPrefix(string(full), "0 failure output") || !strings.Contains(string(full), "499 failure output") {
		t.Fatal(string(full), err)
	}
}

func TestTheGeneratedConfigDecodesAndResolvesToTheRequestedCommands(t *testing.T) {
	for _, verify := range []string{"scripts/verify", `printf '%s' "a\\b"`} {
		root := fixture(t, map[string]string{loop.ConfigFile: RenderConfig(verify, "go test {args}")})
		v, c, err := loop.ResolveHooks(root, "")
		if err != nil || v != verify || c != "go test {args}" {
			t.Fatal(v, c, err)
		}
	}
}

func TestTheVerifyRunnerKeepsOnlyABoundedTailOfAnyOutput(t *testing.T) {
	r := Run(context.Background(), t.TempDir(), `i=0; while [ "$i" -lt 500 ]; do printf '%0500d\n' "$i"; i=$((i+1)); done; exit 7`, time.Second*5)
	if r.Status != "failed" || r.ExitCode != 7 || len(r.Output) > 40*301 || strings.Count(r.Output, "\n") > 40 || strings.Contains(r.Output, "0000499") == true {
		t.Fatalf("%+v", r)
	}
	for _, line := range strings.Split(strings.TrimSuffix(r.Output, "\n"), "\n") {
		if len(line) > 300 {
			t.Fatal(len(line))
		}
	}
	r = Run(context.Background(), t.TempDir(), "printf ok", time.Second)
	if r.Status != "passed" || r.Output != "ok" || r.ExitCode != 0 {
		t.Fatal(r)
	}
}

func TestTheVerifyRunnerKillsTheWholeProcessGroupWhenTheTimeoutExpires(t *testing.T) {
	root := t.TempDir()
	start := time.Now()
	r := Run(context.Background(), root, `(sleep 1; echo survived > survived) & wait`, 50*time.Millisecond)
	if r.Status != "timed_out" || time.Since(start) > time.Second {
		t.Fatal(r, time.Since(start))
	}
	time.Sleep(1100 * time.Millisecond)
	if _, err := os.Stat(filepath.Join(root, "survived")); !os.IsNotExist(err) {
		t.Fatal("descendant survived", err)
	}
}

func TestTheVerifyRunnerRetainsTheLastFortyLinesRatherThanTheFirstForty(t *testing.T) {
	r := Run(context.Background(), t.TempDir(), `i=0; while [ "$i" -lt 500 ]; do echo "line-$i"; i=$((i+1)); done; exit 3`, time.Second)
	if !strings.HasPrefix(r.Output, "line-460\n") || !strings.HasSuffix(r.Output, "line-499\n") || strings.Count(r.Output, "\n") != 40 {
		t.Fatal(r)
	}
}

func TestTheVerifyRunnerStopsOrphansThatKeepItsOutputPipeOpen(t *testing.T) {
	root := t.TempDir()
	start := time.Now()
	r := Run(context.Background(), root, `(sleep 1; echo survived > survived) &`, time.Second*5)
	if r.Status != "failed" || time.Since(start) > time.Second {
		t.Fatal(r)
	}
	time.Sleep(1100 * time.Millisecond)
	if _, err := os.Stat(filepath.Join(root, "survived")); !os.IsNotExist(err) {
		t.Fatal("orphan survived", err)
	}
}
