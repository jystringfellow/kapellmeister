package initrepo

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jystringfellow/kapellmeister/internal/loop"
)

func TestTheGeneratedScriptHasNoCommentsBesideTheShebangAndParsesWithSh(t *testing.T) {
	for _, files := range []map[string]string{{"go.mod": ""}, {"a.sln": ""}, {"Cargo.toml": ""}, {"package.json": `{"scripts":{"test":"test"}}`}, {"pytest.ini": ""}, {"Makefile": "test:\n"}, {"go.mod": "", "Cargo.toml": "", "pytest.ini": ""}} {
		d := detection(t, files)
		script, err := RenderScript(d.Steps)
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(script, "\n") {
			if i > 0 && strings.HasPrefix(strings.TrimSpace(line), "#") {
				t.Fatal(line)
			}
		}
		cmd := exec.Command("sh", "-n")
		cmd.Stdin = strings.NewReader(script)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s: %v", out, err)
		}
		labels := map[string]bool{}
		for _, step := range d.Steps {
			if labels[step.Label] {
				t.Fatalf("duplicate %s", step.Label)
			}
			labels[step.Label] = true
		}
	}
}

func TestAStepCommandWithASingleQuoteIsRefused(t *testing.T) {
	if _, err := RenderScript([]Step{{Label: "bad", Command: "echo 'bad'"}}); err == nil {
		t.Fatal("accepted a single quote")
	}
}

func TestTheGeneratedConfigDecodesAndResolvesToTheDraftedCommands(t *testing.T) {
	for _, verify := range []string{"scripts/verify", `printf '%s' "a\\b"`} {
		root := fixture(t, map[string]string{loop.ConfigFile: RenderConfig(verify, "go test {args}")})
		v, c, err := loop.ResolveHooks(root, "")
		if err != nil || v != verify || c != "go test {args}" {
			t.Fatalf("%q %q %v", v, c, err)
		}
	}
}

func goScript(t *testing.T, source string) (string, string) {
	t.Helper()
	root := fixture(t, map[string]string{"go.mod": "module fixture\n\ngo 1.21\n", "main_test.go": source})
	d, err := Detect(root)
	if err != nil {
		t.Fatal(err)
	}
	script, err := RenderScript(d.Steps)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "scripts/verify")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	logs := t.TempDir()
	t.Setenv("TMPDIR", logs)
	return path, logs
}

func TestTheGeneratedScriptPassesOnAPassingGoModuleAndRunsFromAnyWorkingDirectory(t *testing.T) {
	path, logs := goScript(t, "package fixture\n\nimport \"testing\"\n\nfunc TestPass(t *testing.T) {}\n")
	cmd := exec.Command("sh", path)
	cmd.Dir = t.TempDir()
	out, err := cmd.CombinedOutput()
	if err != nil || string(out) != "verify ok: gofmt, go vet, go build, go test\n" {
		t.Fatalf("%s: %v", out, err)
	}
	entries, err := os.ReadDir(logs)
	if err != nil || len(entries) != 0 {
		t.Fatalf("logs: %v %v", entries, err)
	}
}

func TestTheGeneratedScriptBoundsAFailingTestsOutputNamesTheStepAndPointsAtTheFullLog(t *testing.T) {
	path, logs := goScript(t, "package fixture\n\nimport (\n\t\"fmt\"\n\t\"testing\"\n)\n\nfunc TestFail(t *testing.T) {\n\tfor i := 0; i < 500; i++ {\n\t\tfmt.Println(i, \"failure output\")\n\t}\n\tt.Fail()\n}\n")
	out, err := exec.Command("sh", path).CombinedOutput()
	if err == nil || err.(*exec.ExitError).ExitCode() != 1 || !strings.Contains(string(out), "step: go test (exit 1)") || len(strings.Split(string(out), "\n")) > 45 {
		t.Fatalf("%s: %v", out, err)
	}
	_, log, _ := strings.Cut(string(out), "full log: ")
	log = strings.TrimSpace(log)
	if !strings.HasPrefix(log, logs+string(os.PathSeparator)) {
		t.Fatal(log)
	}
	full, err := os.ReadFile(log)
	if err != nil || !strings.Contains(string(full), "0 failure output") || !strings.Contains(string(full), "499 failure output") {
		t.Fatalf("log: %s %v", full, err)
	}
}

func TestTheGeneratedScriptNamesTheGofmtStepWhenAFileIsUnformatted(t *testing.T) {
	path, _ := goScript(t, "package fixture\nimport \"testing\"\nfunc TestPass(t *testing.T){}\n")
	out, err := exec.Command("sh", path).CombinedOutput()
	if err == nil || !strings.Contains(string(out), "step: gofmt") {
		t.Fatalf("%s: %v", out, err)
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
