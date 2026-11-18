package loop

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var (
	testTableLine     = regexp.MustCompile(`^\s*\[test\]\s*(#.*)?$`)
	tableLine         = regexp.MustCompile(`^\s*\[`)
	verifyTimeoutLine = regexp.MustCompile(`^\s*verify_timeout\s*=`)
)

func SetVerifyTimeout(worktree string, timeout time.Duration) error {
	path := filepath.Join(worktree, ConfigFile)
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	original, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	lines := strings.Split(string(original), "\n")
	entry := fmt.Sprintf("verify_timeout = %q", timeout.String())
	header, key := -1, -1
	for i, line := range lines {
		if header < 0 {
			if testTableLine.MatchString(line) {
				header = i
			}
			continue
		}
		if tableLine.MatchString(line) {
			break
		}
		if verifyTimeoutLine.MatchString(line) {
			key = i
			break
		}
	}
	switch {
	case header < 0:
		return fmt.Errorf("%s has no [test] table to record verify_timeout = %q in", ConfigFile, timeout.String())
	case key >= 0:
		lines[key] = entry
	default:
		lines = append(lines[:header+1], append([]string{entry}, lines[header+1:]...)...)
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")), info.Mode().Perm()); err != nil {
		return err
	}
	written, err := ResolveTest(worktree)
	if err == nil && written.VerifyTimeout != timeout.String() {
		err = fmt.Errorf("verify_timeout reads back as %q", written.VerifyTimeout)
	}
	if err != nil {
		if restoreErr := os.WriteFile(path, original, info.Mode().Perm()); restoreErr != nil {
			return restoreErr
		}
		return fmt.Errorf("%s: could not record verify_timeout = %q: %w", ConfigFile, timeout.String(), err)
	}
	return nil
}

func WithoutVerifyTimeout(config string) string {
	var kept []string
	for _, line := range strings.Split(config, "\n") {
		if !verifyTimeoutLine.MatchString(line) {
			kept = append(kept, line)
		}
	}
	return strings.Join(kept, "\n")
}
