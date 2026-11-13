package initrepo

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func fixture(t *testing.T, files map[string]string) string {
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

func detection(t *testing.T, files map[string]string) Detection {
	t.Helper()
	d, err := Detect(fixture(t, files))
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestEachSupportedToolchainIsDetectedFromItsRootFile(t *testing.T) {
	for _, tt := range []struct {
		tool, file, content, check string
		labels                     []string
	}{
		{"go", "go.mod", "module fixture\n", "go test {args}", []string{"gofmt", "go vet", "go build", "go test"}},
		{"dotnet", "example.sln", "", "dotnet test --nologo {args}", []string{"dotnet format", "dotnet build", "dotnet test"}},
		{"rust", "Cargo.toml", "", "cargo test {args}", []string{"cargo fmt", "cargo build", "cargo test"}},
		{"node", "package.json", `{"scripts":{"test":"run-tests"}}`, "", []string{"npm test"}},
		{"python", "pytest.ini", "", "python3 -m pytest {args}", []string{"pytest"}},
		{"make test", "Makefile", "test: all\n\techo ok\n", "", []string{"make test"}},
	} {
		t.Run(tt.tool, func(t *testing.T) {
			d := detection(t, map[string]string{tt.file: tt.content})
			if len(d.Detected) != 1 || d.Detected[0].Toolchain != tt.tool || d.Detected[0].File != tt.file || !d.Detected[0].Used {
				t.Fatalf("detected: %+v", d)
			}
			var labels []string
			for _, s := range d.Steps {
				labels = append(labels, s.Label)
			}
			if !reflect.DeepEqual(labels, tt.labels) || d.Check != tt.check {
				t.Fatalf("steps/check: %+v / %q", d.Steps, d.Check)
			}
		})
	}
}

func TestANodePackageWithoutARealTestScriptIsNotDetected(t *testing.T) {
	for _, content := range []string{`{}`, `{"scripts":{"test":""}}`, `{"scripts":{"test":"echo \"Error: no test specified\" && exit 1"}}`} {
		if d := detection(t, map[string]string{"package.json": content}); len(d.Steps) != 0 {
			t.Fatalf("%s: %+v", content, d)
		}
	}
}

func TestTheNodePackageManagerComesFromTheLockfile(t *testing.T) {
	for _, tt := range []struct{ lock, pm string }{{"pnpm-lock.yaml", "pnpm"}, {"yarn.lock", "yarn"}, {"bun.lock", "bun"}, {"bun.lockb", "bun"}, {"package-lock.json", "npm"}, {"", "npm"}} {
		files := map[string]string{"package.json": `{"scripts":{"test":"test"}}`}
		if tt.lock != "" {
			files[tt.lock] = ""
		}
		d := detection(t, files)
		if d.Steps[0].Command != tt.pm+" run test" {
			t.Fatalf("%s: %+v", tt.lock, d)
		}
		if tt.lock != "" && d.Detected[0].Lockfile != tt.lock {
			t.Fatalf("lockfile: %+v", d)
		}
	}
	d := detection(t, map[string]string{"package.json": `{"scripts":{"test":"test"}}`, "pnpm-lock.yaml": "", "yarn.lock": ""})
	if d.Steps[0].Command != "pnpm run test" {
		t.Fatal(d)
	}
}

func TestNodeLintAndBuildStepsAppearOnlyWhenThePackageDefinesThem(t *testing.T) {
	d := detection(t, map[string]string{"package.json": `{"scripts":{"lint":"lint","build":"build","test":"test"}}`})
	var commands []string
	for _, s := range d.Steps {
		commands = append(commands, s.Command)
	}
	if !reflect.DeepEqual(commands, []string{"npm run lint", "npm run build", "npm run test"}) {
		t.Fatal(commands)
	}
}

func TestPytestIsDetectedFromItsConfigOrDeclaredDependencyButNotFromPytestPlugins(t *testing.T) {
	for _, tt := range []struct {
		file, content string
		used          bool
	}{
		{"tox.ini", "[pytest]\n", true}, {"setup.cfg", "[tool:pytest]\n", true},
		{"pyproject.toml", "[tool.pytest.ini_options]\n", true},
		{"pyproject.toml", "[project]\ndependencies = ['pytest>=7']\n", true},
		{"pyproject.toml", "[project.optional-dependencies]\ntest = ['pytest[testing]>=7']\n", true},
		{"pyproject.toml", "[dependency-groups]\ntest = ['pytest']\n", true},
		{"pyproject.toml", "[tool.poetry.group.test.dependencies]\npytest = '^8'\n", true},
		{"requirements-dev.txt", "pytest>=7\n", true}, {"dev-requirements.txt", "pytest == 8\n", true},
		{"requirements.txt", "pytest-cov\n", false},
		{"pyproject.toml", "[project]\ndependencies = ['pytest-cov']\n", false},
		{"pyproject.toml", "[ broken", false},
	} {
		t.Run(tt.file+tt.content, func(t *testing.T) {
			d := detection(t, map[string]string{tt.file: tt.content})
			if (len(d.Steps) > 0) != tt.used {
				t.Fatalf("%s: %+v", tt.content, d)
			}
			if tt.content == "[ broken" && (len(d.Detected) == 0 || !strings.Contains(d.Detected[0].Reason, "invalid")) {
				t.Fatal(d)
			}
		})
	}
}

func TestThePythonRunnerFollowsTheLockfile(t *testing.T) {
	for _, tt := range []struct{ lock, runner string }{{"uv.lock", "uv run pytest"}, {"poetry.lock", "poetry run pytest"}, {"", "python3 -m pytest"}} {
		files := map[string]string{"pytest.ini": ""}
		if tt.lock != "" {
			files[tt.lock] = ""
		}
		d := detection(t, files)
		if d.Steps[0].Command != tt.runner+" -q" || d.Check != tt.runner+" {args}" {
			t.Fatal(d)
		}
	}
}

func TestMoreThanOneSolutionFileIsReportedButNotUsed(t *testing.T) {
	d := detection(t, map[string]string{"one.sln": "", "two.slnx": ""})
	if len(d.Steps) != 0 || len(d.Detected) != 1 || d.Detected[0].Used || !strings.Contains(d.Detected[0].Reason, "multiple") {
		t.Fatal(d)
	}
}

func TestAMakefileTestTargetIsUsedOnlyWhenNoToolchainIsDetected(t *testing.T) {
	d := detection(t, map[string]string{"go.mod": "", "Makefile": "test:\n"})
	if len(d.Detected) != 2 || d.Detected[1].Used || len(d.Steps) != 4 {
		t.Fatal(d)
	}
	for _, text := range []string{"test := value\n", "other:\n\techo test:\n", "# test:\n"} {
		if d := detection(t, map[string]string{"Makefile": text}); len(d.Steps) > 0 {
			t.Fatal(d)
		}
	}
}

func TestSeveralToolchainsContributeTheirStepsInTheDocumentedOrder(t *testing.T) {
	d := detection(t, map[string]string{"pytest.ini": "", "Cargo.toml": "", "a.sln": "", "go.mod": "", "package.json": `{"scripts":{"test":"test"}}`})
	var got []string
	for _, v := range d.Detected {
		got = append(got, v.Toolchain)
	}
	if !reflect.DeepEqual(got, []string{"go", "dotnet", "rust", "node", "python"}) || len(d.Steps) != 12 {
		t.Fatal(d)
	}
}

func TestCheckIsDerivedOnlyWhenOneToolchainIsUsed(t *testing.T) {
	for _, files := range []map[string]string{{"go.mod": "", "Cargo.toml": ""}, {"go.mod": "", "package.json": `{"scripts":{"test":"test"}}`}} {
		if d := detection(t, files); d.Check != "" {
			t.Fatal(d.Check)
		}
	}
}

func TestDetectionReadsOnlyTheRepositoryRoot(t *testing.T) {
	if d := detection(t, map[string]string{"nested/go.mod": ""}); len(d.Steps) != 0 {
		t.Fatal(d)
	}
}

func TestAnIncludedDependencyGroupNamedPytestIsNotPytestEvidence(t *testing.T) {
	d := detection(t, map[string]string{"pyproject.toml": "[dependency-groups]\ntest = [{include-group = 'pytest'}]\npytest = ['pytest-cov']\n"})
	if len(d.Steps) != 0 {
		t.Fatal(d)
	}
}
