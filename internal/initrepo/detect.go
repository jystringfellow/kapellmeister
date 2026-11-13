package initrepo

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

type Hint struct {
	Kind string `json:"kind"`
	File string `json:"file"`
	Note string `json:"note"`
}

func Hints(root string) ([]Hint, error) {
	hints := []Hint{}
	entries, err := os.ReadDir(root)
	if err != nil {
		return hints, err
	}
	files := map[string]bool{}
	for _, entry := range entries {
		if !entry.Type().IsRegular() {
			continue
		}
		name := entry.Name()
		files[name] = true
		switch name {
		case "go.mod", "Cargo.toml", "package.json", "pytest.ini", "pyproject.toml", "setup.cfg", "tox.ini":
			hints = append(hints, Hint{Kind: "toolchain", File: name})
		default:
			if strings.HasSuffix(name, ".sln") || strings.HasSuffix(name, ".slnx") || strings.HasPrefix(name, "requirements") && strings.HasSuffix(name, ".txt") {
				hints = append(hints, Hint{Kind: "toolchain", File: name})
			}
		}
	}
	for _, name := range []string{"GNUmakefile", "makefile", "Makefile"} {
		if !files[name] {
			continue
		}
		content, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			return hints, err
		}
		if regexp.MustCompile(`(?m)^test[\t ]*:(?:[^=]|$)`).Match(content) {
			hints = append(hints, Hint{Kind: "make_target", File: name, Note: "test"})
		}
		break
	}
	ci := []string{".gitlab-ci.yml", ".circleci/config.yml", "Jenkinsfile", "azure-pipelines.yml", "bitbucket-pipelines.yml"}
	for _, pattern := range []string{".github/workflows/*.yml", ".github/workflows/*.yaml"} {
		matches, err := filepath.Glob(filepath.Join(root, pattern))
		if err != nil {
			return hints, err
		}
		for _, match := range matches {
			rel, _ := filepath.Rel(root, match)
			ci = append(ci, filepath.ToSlash(rel))
		}
	}
	for _, name := range ci {
		info, err := os.Stat(filepath.Join(root, name))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return hints, err
		}
		if info.Mode().IsRegular() {
			hints = append(hints, Hint{Kind: "ci", File: name})
		}
	}
	sort.Slice(hints, func(i, j int) bool {
		if hints[i].Kind != hints[j].Kind {
			return hints[i].Kind < hints[j].Kind
		}
		return hints[i].File < hints[j].File
	})
	return hints, nil
}
