package loop

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/BurntSushi/toml"
)

type Exclusion struct {
	Suite    string `toml:"suite" json:"suite"`
	Reason   string `toml:"reason" json:"reason"`
	Method   string `toml:"method" json:"method"`
	Evidence string `toml:"evidence" json:"evidence"`
	Run      string `toml:"run" json:"run"`
}

func (x Exclusion) String() string {
	text := fmt.Sprintf("%s (%s; %s): %s", x.Suite, x.Reason, x.Method, x.Evidence)
	if x.Run != "" {
		text += "; run it by hand: " + x.Run
	}
	return text
}

func ValidExclusions(entries []Exclusion) error {
	suites := make([]string, len(entries))
	for i, entry := range entries {
		suites[i] = entry.Suite
		if strings.TrimSpace(entry.Reason) == "" {
			return fmt.Errorf("suite %q needs a reason", entry.Suite)
		}
	}
	return ValidSuites(suites)
}

var (
	excludedLine = regexp.MustCompile(`^\s*\[\[\s*test\s*\.\s*excluded\s*\]\]\s*(#.*)?$`)
	anyTableLine = regexp.MustCompile(`^\s*\[`)
)

func WithoutExcluded(config string) string {
	var kept []string
	inside := false
	for _, line := range strings.Split(config, "\n") {
		if excludedLine.MatchString(line) {
			inside = true
			continue
		}
		if inside && anyTableLine.MatchString(line) {
			inside = false
		}
		if !inside {
			kept = append(kept, line)
		}
	}
	return strings.Join(kept, "\n")
}

func SetExcluded(worktree string, entries []Exclusion) error {
	path := filepath.Join(worktree, ConfigFile)
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	original, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var text strings.Builder
	text.WriteString(strings.TrimRight(WithoutExcluded(string(original)), "\n") + "\n")
	for _, entry := range entries {
		text.WriteString("\n[[test.excluded]]\n")
		if err := toml.NewEncoder(&text).Encode(entry); err != nil {
			return err
		}
	}
	if err := os.WriteFile(path, []byte(text.String()), info.Mode().Perm()); err != nil {
		return err
	}
	written, err := ResolveTest(worktree)
	if err == nil && !slices.Equal(written.Excluded, entries) {
		err = fmt.Errorf("[[test.excluded]] reads back as %v", written.Excluded)
	}
	if err != nil {
		if restoreErr := os.WriteFile(path, original, info.Mode().Perm()); restoreErr != nil {
			return restoreErr
		}
		return fmt.Errorf("%s: could not record [[test.excluded]]: %w", ConfigFile, err)
	}
	return nil
}
