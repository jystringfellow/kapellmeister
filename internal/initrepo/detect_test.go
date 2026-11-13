package initrepo

import (
	"os"
	"path/filepath"
	"reflect"
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

func TestHintsReportToolchainFilesAMakeTestTargetAndCIWorkflowFilesWithoutChoosingACommand(t *testing.T) {
	files := map[string]string{
		"go.mod": "", "one.sln": "", "two.slnx": "", "Cargo.toml": "", "package.json": "not JSON",
		"pytest.ini": "", "pyproject.toml": "not TOML", "setup.cfg": "", "tox.ini": "", "requirements-dev.txt": "",
		"Makefile": "test: all\n", ".github/workflows/ci.yml": "", ".github/workflows/build.yaml": "",
		".gitlab-ci.yml": "", ".circleci/config.yml": "", "Jenkinsfile": "", "azure-pipelines.yml": "", "bitbucket-pipelines.yml": "",
		"nested/go.mod": "", ".github/workflows/ignored.txt": "", "yarn.lock": "",
	}
	root := fixture(t, files)
	hints, err := Hints(root)
	if err != nil || len(hints) != 18 {
		t.Fatalf("%+v %v", hints, err)
	}
	again, err := Hints(root)
	if err != nil || !reflect.DeepEqual(hints, again) {
		t.Fatal(hints, again, err)
	}
	for _, h := range hints {
		switch h.Kind {
		case "toolchain":
			if h.Note != "" {
				t.Fatal(h)
			}
		case "make_target":
			if h.File != "Makefile" || h.Note != "test" {
				t.Fatal(h)
			}
		case "ci":
		default:
			t.Fatal(h)
		}
	}
}

func TestHintsIgnoreNestedToolchainsAndMakefilesWithoutATestTarget(t *testing.T) {
	for _, text := range []string{"test := value\n", "other:\n\techo test:\n", "# test:\n"} {
		hints, err := Hints(fixture(t, map[string]string{"Makefile": text, "nested/go.mod": ""}))
		if err != nil || len(hints) != 0 {
			t.Fatal(hints, err)
		}
	}
}
