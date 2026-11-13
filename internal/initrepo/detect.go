package initrepo

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/BurntSushi/toml"
)

type Step struct {
	Label   string
	Command string
}

type Evidence struct {
	Toolchain string `json:"toolchain"`
	File      string `json:"file"`
	Used      bool   `json:"used"`
	Reason    string `json:"reason,omitempty"`
	Lockfile  string `json:"lockfile,omitempty"`
}

type Detection struct {
	Detected []Evidence
	Steps    []Step
	Check    string
}

func Detect(root string) (Detection, error) {
	d := Detection{Detected: []Evidence{}, Steps: []Step{}}
	entries, err := os.ReadDir(root)
	if err != nil {
		return d, err
	}
	files := map[string]bool{}
	for _, entry := range entries {
		if entry.Type().IsRegular() {
			files[entry.Name()] = true
		}
	}
	used := 0
	add := func(tool, file, lock, check string, steps ...Step) {
		d.Detected = append(d.Detected, Evidence{Toolchain: tool, File: file, Used: true, Lockfile: lock})
		d.Steps = append(d.Steps, steps...)
		used++
		d.Check = check
	}
	read := func(name string) (string, error) {
		if !files[name] {
			return "", nil
		}
		data, err := os.ReadFile(filepath.Join(root, name))
		return string(data), err
	}
	if files["go.mod"] {
		add("go", "go.mod", "", "go test {args}",
			Step{"gofmt", `unformatted=$(gofmt -l .) || exit; [ -z "$unformatted" ] || { echo "gofmt would change:"; echo "$unformatted"; exit 1; }`},
			Step{"go vet", "go vet ./..."}, Step{"go build", "go build ./..."}, Step{"go test", "go test ./..."})
	}
	var solutions []string
	for _, entry := range entries {
		name := entry.Name()
		if files[name] && (strings.HasSuffix(name, ".sln") || strings.HasSuffix(name, ".slnx")) {
			solutions = append(solutions, name)
		}
	}
	if len(solutions) == 1 {
		add("dotnet", solutions[0], "", "dotnet test --nologo {args}", Step{"dotnet format", "dotnet format --verify-no-changes"}, Step{"dotnet build", "dotnet build --nologo"}, Step{"dotnet test", "dotnet test --nologo"})
	} else if len(solutions) > 1 {
		d.Detected = append(d.Detected, Evidence{Toolchain: "dotnet", File: strings.Join(solutions, ", "), Reason: "multiple solution files make dotnet commands ambiguous"})
	}
	if files["Cargo.toml"] {
		add("rust", "Cargo.toml", "", "cargo test {args}", Step{"cargo fmt", "cargo fmt --check"}, Step{"cargo build", "cargo build"}, Step{"cargo test", "cargo test"})
	}
	if files["package.json"] {
		content, err := read("package.json")
		if err != nil {
			return d, err
		}
		var pkg struct {
			Scripts map[string]string `json:"scripts"`
		}
		if err := json.Unmarshal([]byte(content), &pkg); err != nil {
			d.Detected = append(d.Detected, Evidence{Toolchain: "node", File: "package.json", Reason: "invalid JSON"})
		} else if test := strings.TrimSpace(pkg.Scripts["test"]); test != "" && test != `echo "Error: no test specified" && exit 1` {
			pm, lock := "npm", ""
			for _, candidate := range []struct{ file, pm string }{{"pnpm-lock.yaml", "pnpm"}, {"yarn.lock", "yarn"}, {"bun.lock", "bun"}, {"bun.lockb", "bun"}, {"package-lock.json", "npm"}} {
				if files[candidate.file] {
					pm, lock = candidate.pm, candidate.file
					break
				}
			}
			var steps []Step
			for _, name := range []string{"lint", "build", "test"} {
				if strings.TrimSpace(pkg.Scripts[name]) != "" {
					steps = append(steps, Step{pm + " " + name, pm + " run " + name})
				}
			}
			add("node", "package.json", lock, "", steps...)
		}
	}
	pythonFile := ""
	for _, candidate := range []struct{ file, section string }{{"pytest.ini", ""}, {"tox.ini", "pytest"}, {"setup.cfg", "tool:pytest"}} {
		if !files[candidate.file] {
			continue
		}
		content, err := read(candidate.file)
		if err != nil {
			return d, err
		}
		if candidate.section == "" || regexp.MustCompile(`(?m)^\s*\[`+regexp.QuoteMeta(candidate.section)+`\]\s*(?:[#;].*)?$`).MatchString(content) {
			pythonFile = candidate.file
			break
		}
	}
	if files["pyproject.toml"] {
		content, err := read("pyproject.toml")
		if err != nil {
			return d, err
		}
		var project map[string]any
		if _, err := toml.Decode(content, &project); err != nil {
			d.Detected = append(d.Detected, Evidence{Toolchain: "python", File: "pyproject.toml", Reason: "invalid TOML, not pytest evidence"})
		} else if pythonFile == "" && hasPytest(project) {
			pythonFile = "pyproject.toml"
		}
	}
	if pythonFile == "" {
		for _, entry := range entries {
			name := entry.Name()
			if !files[name] || !(strings.HasPrefix(name, "requirements") && strings.HasSuffix(name, ".txt") || strings.HasSuffix(name, "-requirements.txt")) {
				continue
			}
			content, err := read(name)
			if err != nil {
				return d, err
			}
			for _, line := range strings.Split(content, "\n") {
				if pytestRequirement(line) {
					pythonFile = name
					break
				}
			}
			if pythonFile != "" {
				break
			}
		}
	}
	if pythonFile != "" {
		runner, lock := "python3 -m pytest", ""
		for _, candidate := range []struct{ file, runner string }{{"uv.lock", "uv run pytest"}, {"poetry.lock", "poetry run pytest"}} {
			if files[candidate.file] {
				runner, lock = candidate.runner, candidate.file
				break
			}
		}
		add("python", pythonFile, lock, runner+" {args}", Step{"pytest", runner + " -q"})
	}
	for _, name := range []string{"GNUmakefile", "makefile", "Makefile"} {
		if !files[name] {
			continue
		}
		content, err := read(name)
		if err != nil {
			return d, err
		}
		if regexp.MustCompile(`(?m)^test[\t ]*:(?:[^=]|$)`).MatchString(content) {
			if used == 0 {
				add("make test", name, "", "", Step{"make test", "make test"})
			} else {
				d.Detected = append(d.Detected, Evidence{Toolchain: "make test", File: name, Reason: "a toolchain is detected"})
			}
		}
		break
	}
	if used != 1 {
		d.Check = ""
	}
	return d, nil
}

var pytestName = regexp.MustCompile(`(?i)^pytest(?:\[[^\]]+\])?\s*(?:[<>=!~;@]|$)`)

func pytestRequirement(value string) bool { return pytestName.MatchString(strings.TrimSpace(value)) }

func tableAt(m map[string]any, keys ...string) map[string]any {
	for _, key := range keys {
		next, ok := m[key].(map[string]any)
		if !ok {
			return nil
		}
		m = next
	}
	return m
}

func hasPytest(project map[string]any) bool {
	if tableAt(project, "tool", "pytest") != nil {
		return true
	}
	var dependenciesContain func(any) bool
	dependenciesContain = func(value any) bool {
		switch v := value.(type) {
		case string:
			return pytestRequirement(v)
		case []any:
			for _, item := range v {
				if requirement, ok := item.(string); ok && pytestRequirement(requirement) {
					return true
				}
			}
		case []string:
			for _, item := range v {
				if pytestRequirement(item) {
					return true
				}
			}
		case map[string]any:
			for _, item := range v {
				if dependenciesContain(item) {
					return true
				}
			}
		}
		return false
	}
	p := tableAt(project, "project")
	if dependenciesContain(p["dependencies"]) || dependenciesContain(p["optional-dependencies"]) || dependenciesContain(project["dependency-groups"]) {
		return true
	}
	poetry := tableAt(project, "tool", "poetry")
	var poetryHasPytest func(map[string]any) bool
	poetryHasPytest = func(m map[string]any) bool {
		for key, value := range m {
			child, ok := value.(map[string]any)
			if !ok {
				continue
			}
			if key == "dependencies" || key == "dev-dependencies" {
				if _, ok := child["pytest"]; ok {
					return true
				}
			}
			if poetryHasPytest(child) {
				return true
			}
		}
		return false
	}
	return poetryHasPytest(poetry)
}

func (s Step) validate() error {
	if strings.ContainsAny(s.Label+s.Command, "'\x00") || strings.ContainsAny(s.Label, "\r\n") {
		return fmt.Errorf("step %q cannot contain a single quote, NUL or a multiline label", s.Label)
	}
	return nil
}
