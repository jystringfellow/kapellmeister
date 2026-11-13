package trust

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/BurntSushi/toml"
)

type Folders struct {
	Enabled     bool
	Out, Err    io.Writer
	CodexConfig string
	diagnosed   map[string]bool
}

var configMu sync.Mutex

func (f *Folders) BeforeLaunch(provider, dir string) {
	if f == nil || !f.Enabled || provider != "codex" || dir == "" {
		return
	}
	key := repositoryRoot(dir)
	if key == "" {
		return
	}
	launch, err := filepath.Abs(dir)
	if err != nil {
		return
	}
	launch, err = filepath.EvalSymlinks(launch)
	if err != nil {
		return
	}
	configMu.Lock()
	defer configMu.Unlock()
	path := f.CodexConfig
	if path == "" {
		home := os.Getenv("CODEX_HOME")
		if home == "" {
			userHome, err := os.UserHomeDir()
			if err != nil {
				f.diagnostic("Codex config", err)
				return
			}
			home = filepath.Join(userHome, ".codex")
		}
		path = filepath.Join(home, "config.toml")
	}
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		f.diagnostic(path, err)
		return
	}
	var cfg map[string]any
	md, err := toml.Decode(string(data), &cfg)
	if err != nil {
		f.diagnostic(path, err)
		return
	}
	projects, _ := cfg["projects"].(map[string]any)
	if _, ok := projects[key]; ok {
		return
	}
	if _, ok := projects[launch]; ok {
		return
	}
	for existing := range projects {
		if resolved, err := filepath.EvalSymlinks(existing); err == nil && (resolved == key || resolved == launch) {
			return
		}
	}
	if md.Type("projects") == "Hash" && projectsAssignedAtRoot(string(data), cfg) {
		f.diagnostic(path, fmt.Errorf("projects assignment cannot safely be extended by appending a table"))
		return
	}

	entry := ""
	if len(data) > 0 && data[len(data)-1] != '\n' {
		entry = "\n"
	}
	entry += "\n[projects." + tomlString(key) + "]\ntrust_level = \"trusted\"\n"
	var next struct {
		Projects map[string]struct {
			TrustLevel string `toml:"trust_level"`
		}
	}
	if _, err := toml.Decode(string(data)+entry, &next); err != nil {
		f.diagnostic(path, err)
		return
	}
	if next.Projects[key].TrustLevel != "trusted" {
		f.diagnostic(path, fmt.Errorf("appended entry would not record trust"))
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		f.diagnostic(path, err)
		return
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		f.diagnostic(path, err)
		return
	}
	n, err := file.WriteString(entry)
	closeErr := file.Close()
	if err == nil && n != len(entry) {
		err = io.ErrShortWrite
	}
	if err == nil {
		err = closeErr
	}
	if err != nil {
		f.diagnostic(path, err)
		return
	}
	if f.Out != nil {
		fmt.Fprintf(f.Out, "trust: recorded %s as trusted for Codex in %s", key, path)
		if launch != key {
			fmt.Fprintf(f.Out, " (chairs start in %s)", launch)
		}
		fmt.Fprintln(f.Out)
	}
}

func (f *Folders) diagnostic(path string, err error) {
	if f.diagnosed == nil {
		f.diagnosed = map[string]bool{}
	}
	if f.diagnosed[path] {
		return
	}
	f.diagnosed[path] = true
	if f.Err != nil {
		fmt.Fprintf(f.Err, "trust: cannot append to %s: %s; Codex may still ask\n", path, strings.ReplaceAll(err.Error(), "\n", " "))
	}
}

func repositoryRoot(dir string) string {
	common, err := exec.Command("git", "-C", dir, "rev-parse", "--git-common-dir").Output()
	if err != nil {
		return ""
	}
	path := strings.TrimSuffix(string(common), "\n")
	if !filepath.IsAbs(path) {
		path = filepath.Join(dir, path)
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return ""
	}
	path, err = filepath.EvalSymlinks(path)
	if err != nil {
		return ""
	}
	if filepath.Base(path) == ".git" {
		return filepath.Dir(path)
	}
	root, err := exec.Command("git", "-C", dir, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return ""
	}
	path, err = filepath.EvalSymlinks(strings.TrimSuffix(string(root), "\n"))
	if err != nil {
		return ""
	}
	return path
}

func tomlString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"' || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r < 0x20 || r == 0x7f:
			fmt.Fprintf(&b, "\\u%04X", r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

var assignmentKey = regexp.MustCompile(`(?m)^[ \t]*([A-Za-z0-9_-]+|"(?:[^"\\]|\\.)*"|'[^']*')[ \t]*=`)

func projectsAssignedAtRoot(data string, cfg map[string]any) bool {
	probe := "__kapellmeister_trust_scope__"
	for cfg[probe] != nil {
		probe += "_"
	}
	for _, match := range assignmentKey.FindAllStringSubmatchIndex(data, -1) {
		var assignment map[string]any
		if _, err := toml.Decode(data[match[2]:match[3]]+" = true", &assignment); err != nil || assignment["projects"] == nil {
			continue
		}
		var scope map[string]any
		if _, err := toml.Decode(data[:match[0]]+"\n"+probe+" = true\n", &scope); err == nil && scope[probe] == true {
			return true
		}
	}
	return false
}
