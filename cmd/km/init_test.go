package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/jystringfellow/kapellmeister/internal/initrepo"
)

func initFixture(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, content := range files {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}
func initCall(t *testing.T, root string, args ...string) (string, error) {
	t.Helper()
	var out, stderr bytes.Buffer
	err := initAt(append([]string{"-worktree", root}, args...), &out, &stderr)
	return out.String(), err
}
func initRead(t *testing.T, root, path string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, path))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
func initSnapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		out[rel] = info.Mode().String() + string(data)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestInitWritesTheConfigAndAStubInARepositoryWithNoRecognisedToolchain(t *testing.T) {
	root := t.TempDir()
	out, err := initCall(t, root)
	if err != nil || !strings.Contains(out, "unfinished") || len(initSnapshot(t, root)) != 2 {
		t.Fatal(out, err)
	}
	if got := initRead(t, root, ".kapellmeister/config.toml"); got != "[test]\nverify = \"scripts/verify\"\n" {
		t.Fatal(got)
	}
	script := initRead(t, root, "scripts/verify")
	if !strings.Contains(script, ": km-unfinished-verify\n") {
		t.Fatal(script)
	}
	info, err := os.Stat(filepath.Join(root, "scripts/verify"))
	if err != nil || info.Mode().Perm() != 0755 {
		t.Fatal(info, err)
	}
	before := initSnapshot(t, root)
	out, err = initCall(t, root)
	if err != nil || !strings.Contains(out, "unfinished") || !reflect.DeepEqual(before, initSnapshot(t, root)) {
		t.Fatal(out, err)
	}
}

func TestInitWritesTheSameFilesWhateverToolchainIsPresent(t *testing.T) {
	plain := t.TempDir()
	if out, err := initCall(t, plain); err != nil {
		t.Fatal(out, err)
	}
	for _, tt := range []struct{ file, content string }{
		{"go.mod", "module fixture\n"}, {"example.sln", ""}, {"Cargo.toml", ""},
		{"package.json", "invalid JSON"}, {"pytest.ini", ""}, {"Makefile", "test:\n"},
		{".github/workflows/test.yml", "name: tests\n"},
	} {
		t.Run(tt.file, func(t *testing.T) {
			root := initFixture(t, map[string]string{tt.file: tt.content})
			out, err := initCall(t, root)
			if err != nil || !strings.Contains(out, "hint:") || !strings.Contains(out, tt.file) {
				t.Fatal(out, err)
			}
			for _, file := range []string{"scripts/verify", ".kapellmeister/config.toml"} {
				if initRead(t, root, file) != initRead(t, plain, file) {
					t.Fatal("hint changed", file)
				}
			}
		})
	}
}

func TestInitNeverChangesAFileThatExists(t *testing.T) {
	root := initFixture(t, map[string]string{"go.mod": "", ".kapellmeister/config.toml": "[test]\nverify = 'true'\n", "scripts/verify": "odd bytes\x00\n"})
	if err := os.Chmod(filepath.Join(root, "scripts/verify"), 0711); err != nil {
		t.Fatal(err)
	}
	before := initSnapshot(t, root)
	out, err := initCall(t, root, "-no-run")
	if err != nil || !reflect.DeepEqual(before, initSnapshot(t, root)) || strings.Count(out, "kept, it already exists") != 2 {
		t.Fatal(out, err)
	}
}

func TestInitKeepsAnExistingConfigThatSetsVerifyAndWritesNoScript(t *testing.T) {
	root := initFixture(t, map[string]string{"go.mod": "", ".kapellmeister/config.toml": "[test]\nverify = 'true'\n"})
	before := initSnapshot(t, root)
	out, err := initCall(t, root)
	if err != nil || !reflect.DeepEqual(before, initSnapshot(t, root)) || !strings.Contains(out, "config.toml already sets verify") || !strings.Contains(out, "passed in") {
		t.Fatal(out, err)
	}
}

func TestInitUsesAnExecutableScriptsVerifyOrKapellmeisterVerifyAndWritesNoScript(t *testing.T) {
	for _, hook := range []string{"scripts/verify", ".kapellmeister/verify"} {
		root := initFixture(t, map[string]string{hook: "#!/bin/sh\necho existing\n"})
		if err := os.Chmod(filepath.Join(root, hook), 0755); err != nil {
			t.Fatal(err)
		}
		out, err := initCall(t, root)
		want := hook
		if strings.HasPrefix(hook, ".") {
			want = "./" + hook
		}
		if err != nil || !strings.Contains(initRead(t, root, ".kapellmeister/config.toml"), `verify = "`+want+`"`) || !strings.Contains(out, "passed in") {
			t.Fatal(out, err)
		}
		if len(initSnapshot(t, root)) != 2 {
			t.Fatal(initSnapshot(t, root))
		}
	}
}

func TestInitRefusesANonExecutableScriptsVerifyWithoutTouchingIt(t *testing.T) {
	root := initFixture(t, map[string]string{"scripts/verify": "existing", "go.mod": ""})
	before := initSnapshot(t, root)
	out, err := initCall(t, root, "-no-run")
	if err == nil || !strings.Contains(out, "chmod +x") || !reflect.DeepEqual(before, initSnapshot(t, root)) {
		t.Fatal(out, err)
	}
}

func TestInitANewRunWritesNothingAndSaysSo(t *testing.T) {
	root := initFixture(t, map[string]string{"go.mod": ""})
	if out, err := initCall(t, root, "-no-run"); err != nil {
		t.Fatal(out, err)
	}
	before := initSnapshot(t, root)
	out, err := initCall(t, root, "-no-run")
	if err != nil || !reflect.DeepEqual(before, initSnapshot(t, root)) || !strings.Contains(out, "nothing written: every file already exists") || strings.Contains(out, "review and commit") {
		t.Fatal(out, err)
	}
}

func TestInitDryRunWritesAndRunsNothingAndPrintsTheExactContents(t *testing.T) {
	root := initFixture(t, map[string]string{"go.mod": ""})
	before := initSnapshot(t, root)
	out, err := initCall(t, root, "-dry-run", "-json")
	if err != nil {
		t.Fatal(out, err)
	}
	var report initrepo.Result
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatal(out, err)
	}
	if !reflect.DeepEqual(before, initSnapshot(t, root)) || report.Changed || report.Verify.Status != "unfinished" {
		t.Fatal(out)
	}
	if out, err := initCall(t, root, "-no-run"); err != nil {
		t.Fatal(out, err)
	}
	for _, f := range report.Files {
		if f.Status != "would_write" || f.Content != initRead(t, root, f.Path) {
			t.Fatal(f)
		}
	}
}

func TestInitAcceptsAVerifyFlagWhenNothingIsDetected(t *testing.T) {
	root := t.TempDir()
	out, err := initCall(t, root, "-verify", "printf explicit", "-check", "echo {args}")
	if err != nil || !strings.Contains(out, "passed in") || initRead(t, root, ".kapellmeister/config.toml") != "[test]\nverify = \"scripts/verify\"\ncheck  = \"echo {args}\"\n" {
		t.Fatal(out, err)
	}
	if len(initSnapshot(t, root)) != 2 {
		t.Fatal(initSnapshot(t, root))
	}
}

func TestInitReportsAFailingVerifyPlainlyKeepsTheDraftedFilesAndFails(t *testing.T) {
	root := t.TempDir()
	out, err := initCall(t, root, "-json", "-verify", `i=0; while [ "$i" -lt 500 ]; do echo failure; i=$((i+1)); done; exit 9`)
	var r initrepo.Result
	if e := json.Unmarshal([]byte(out), &r); e != nil {
		t.Fatal(out, e)
	}
	if err == nil || r.Verify.Status != "failed" || r.Verify.ExitCode != 9 || strings.Count(r.Verify.Output, "\n") > 40 || !r.Changed {
		t.Fatal(out, err)
	}
	if _, err := os.Stat(filepath.Join(root, ".kapellmeister/config.toml")); err != nil {
		t.Fatal(err)
	}
	out, err = initCall(t, root)
	if err == nil || !strings.Contains(out, "failed (exit 9)") {
		t.Fatal(out, err)
	}
}

func TestInitTimesOutAHungVerify(t *testing.T) {
	out, err := initCall(t, t.TempDir(), "-verify", "sleep 30 & wait", "-timeout", "30ms")
	if err == nil || !strings.Contains(out, "timed out") {
		t.Fatal(out, err)
	}
}

func TestInitNoRunSkipsTheVerifyRun(t *testing.T) {
	root := t.TempDir()
	out, err := initCall(t, root, "-verify", "touch ran", "-no-run")
	if err != nil || !strings.Contains(out, "skipped (-no-run)") {
		t.Fatal(out, err)
	}
	if _, err := os.Stat(filepath.Join(root, "ran")); !os.IsNotExist(err) {
		t.Fatal(err)
	}
}

func TestInitWithoutVerifyDoesNotRunTheStubAndSaysItIsUnfinished(t *testing.T) {
	root := initFixture(t, map[string]string{"go.mod": "module fixture\n\ngo 1.21\n", "pass.go": "package fixture\n"})
	out, err := initCall(t, root)
	if err != nil || !strings.Contains(out, "unfinished") {
		t.Fatal(out, err)
	}
}

func TestInitDryRunInThisRepositoryKeepsTheExistingConfig(t *testing.T) {
	out, err := initCall(t, "../..", "-dry-run")
	if err != nil || !strings.Contains(out, ".kapellmeister/config.toml: kept, it already exists") || !strings.Contains(out, "scripts/verify: skipped") || !strings.Contains(out, "skipped (-dry-run)") {
		t.Fatal(out, err)
	}
}

func TestInitIsListedInTheUsage(t *testing.T) {
	if !strings.Contains(usage, "km init [-worktree DIR]") {
		t.Fatal("init missing from usage")
	}
}

func TestInitValidatesEveryDestinationBeforeWritingAnything(t *testing.T) {
	for _, files := range []map[string]string{{"go.mod": "", ".kapellmeister": "blocked"}, {"go.mod": "", "scripts": "blocked"}, {"go.mod": "", ".kapellmeister/config.toml": "[unknown]\nvalue = 3\n"}} {
		root := initFixture(t, files)
		before := initSnapshot(t, root)
		out, err := initCall(t, root, "-no-run")
		if err == nil || !reflect.DeepEqual(before, initSnapshot(t, root)) {
			t.Fatal(out, err)
		}
	}
}

func TestInitDryRunTextContainsTheExactDraftsAndLeavesAllFilesUntouched(t *testing.T) {
	root := initFixture(t, map[string]string{"go.mod": ""})
	before := initSnapshot(t, root)
	out, err := initCall(t, root, "-dry-run")
	if err != nil || !reflect.DeepEqual(before, initSnapshot(t, root)) {
		t.Fatal(out, err)
	}
	if actual, err := initCall(t, root, "-no-run"); err != nil {
		t.Fatal(actual, err)
	}
	for _, file := range []string{"scripts/verify", ".kapellmeister/config.toml"} {
		if !strings.Contains(out, file+": would write\n"+initRead(t, root, file)) {
			t.Fatal(out)
		}
	}
}

func TestInitVerifyFlagOverridesAnExistingConfigOnlyForTheRun(t *testing.T) {
	root := initFixture(t, map[string]string{".kapellmeister/config.toml": "[test]\nverify = 'exit 4'\n"})
	before := initSnapshot(t, root)
	out, err := initCall(t, root, "-verify", "true")
	if err != nil || !strings.Contains(out, "verify: true passed") || !reflect.DeepEqual(before, initSnapshot(t, root)) {
		t.Fatal(out, err)
	}
}

func TestInitExplainsHowToConfigureANewStubWhenTheExistingConfigHasNoVerify(t *testing.T) {
	root := initFixture(t, map[string]string{"go.mod": "module fixture\n\ngo 1.21\n", "pass.go": "package fixture\n", ".kapellmeister/config.toml": "[test]\ncheck = 'go test {args}'\n"})
	before := initRead(t, root, ".kapellmeister/config.toml")
	out, err := initCall(t, root)
	if err != nil || !strings.Contains(out, `add verify = "scripts/verify"`) || !strings.Contains(out, "unfinished") || initRead(t, root, ".kapellmeister/config.toml") != before {
		t.Fatal(out, err)
	}
}

func TestInitJSONCarriesTheSameFactsAsTheText(t *testing.T) {
	root := initFixture(t, map[string]string{"package.json": `{"scripts":{"test":"test"}}`, "pnpm-lock.yaml": ""})
	text, err := initCall(t, root, "-dry-run")
	if err != nil {
		t.Fatal(text, err)
	}
	data, err := initCall(t, root, "-dry-run", "-json")
	if err != nil {
		t.Fatal(data, err)
	}
	var r initrepo.Result
	if err := json.Unmarshal([]byte(data), &r); err != nil {
		t.Fatal(err)
	}
	if len(r.Hints) != 1 || r.Hints[0].Kind != "toolchain" || r.Hints[0].File != "package.json" || r.Verify.Command != "scripts/verify" {
		t.Fatal(data)
	}
	for _, f := range r.Files {
		if !strings.Contains(text, f.Path+": would write\n"+f.Content) {
			t.Fatal(text, data)
		}
	}
	for _, next := range r.Next {
		if !strings.Contains(text, "next: "+next) {
			t.Fatal(text, data)
		}
	}
}

func TestInitLeavesUnrelatedFilesAloneAndNeedsNoUserConfigOrAgents(t *testing.T) {
	root := initFixture(t, map[string]string{"unrelated/config.toml": "untouched", "go.mod": ""})
	home := initFixture(t, map[string]string{"config.toml": "not valid TOML ["})
	t.Setenv("KAPELLMEISTER_HOME", home)
	out, err := initCall(t, root, "-no-run")
	if err != nil || strings.Contains(out, "unrelated") || initRead(t, root, "unrelated/config.toml") != "untouched" || len(initSnapshot(t, root)) != 4 {
		t.Fatal(out, err)
	}
}

func TestInitKeepsBothFilesWhenAnExplicitGoVerifyFails(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	root := initFixture(t, map[string]string{"go.mod": "module fixture\n\ngo 1.21\n", "pass.go": "package fixture\n", "fail_test.go": "package fixture\n\nimport \"testing\"\n\nfunc TestFail(t *testing.T) { t.Fatal(\"failure\") }\n"})
	out, err := initCall(t, root, "-verify", "go test ./...")
	if err == nil || !strings.Contains(out, "step: verify") || !strings.Contains(out, "full log:") {
		t.Fatal(out, err)
	}
	if len(initSnapshot(t, root)) != 5 {
		t.Fatal(initSnapshot(t, root))
	}
}

func TestInitKeepsTheFailingStepAndLogPathWhenTheGeneratedScriptPrintsItsFullTail(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	root := initFixture(t, map[string]string{
		"go.mod":       "module fixture\n\ngo 1.21\n",
		"fail_test.go": "package fixture\n\nimport (\n\t\"fmt\"\n\t\"testing\"\n)\n\nfunc TestFail(t *testing.T) {\n\tfor i := 0; i < 500; i++ {\n\t\tfmt.Println(i, \"failure\")\n\t}\n\tt.Fail()\n}\n",
	})
	out, err := initCall(t, root, "-json", "-verify", "go test ./...")
	var r initrepo.Result
	if e := json.Unmarshal([]byte(out), &r); e != nil {
		t.Fatal(out, e)
	}
	if err == nil || !strings.Contains(r.Verify.Output, "step: verify (exit 1)") || !strings.Contains(r.Verify.Output, "full log:") || strings.Count(r.Verify.Output, "\n") > 40 {
		t.Fatal(out, err)
	}
}

func TestInitKeepsAnExistingConfigWithoutVerifyAndWritesAStub(t *testing.T) {
	root := initFixture(t, map[string]string{".kapellmeister/config.toml": "[test]\ncheck = 'echo {args}'\n"})
	before := initRead(t, root, ".kapellmeister/config.toml")
	out, err := initCall(t, root, "-no-run")
	if err != nil || !strings.Contains(out, ".kapellmeister/config.toml: kept, it already exists") || before != initRead(t, root, ".kapellmeister/config.toml") || !strings.Contains(out, "unfinished") {
		t.Fatal(out, err)
	}
}

func TestInitWithVerifyWritesTheWrapperAroundThatCommandAndRunsItOnce(t *testing.T) {
	root := t.TempDir()
	command := `printf '%s\n' 'one run' >> counter`
	out, err := initCall(t, root, "-verify", command)
	if err != nil || !strings.Contains(out, "passed in") || initRead(t, root, "counter") != "one run\n" {
		t.Fatal(out, err)
	}
	want, err := initrepo.RenderVerify(command)
	if err != nil {
		t.Fatal(err)
	}
	if initRead(t, root, "scripts/verify") != want || initRead(t, root, ".kapellmeister/config.toml") != "[test]\nverify = \"scripts/verify\"\n" {
		t.Fatal("command was not wrapped")
	}
}

func TestInitCheckFlagNeverEditsAnExistingConfig(t *testing.T) {
	root := initFixture(t, map[string]string{".kapellmeister/config.toml": "[test]\nverify = 'true'\ncheck = 'old {args}'\n"})
	before := initSnapshot(t, root)
	out, err := initCall(t, root, "-check", "new {args}")
	if err != nil || !reflect.DeepEqual(before, initSnapshot(t, root)) {
		t.Fatal(out, err)
	}
}
