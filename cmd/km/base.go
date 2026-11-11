package main

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"unicode"
	"unicode/utf8"
)

func validBaseBranch(name string) error {
	invalid := func() error { return fmt.Errorf("-base %q is not a valid branch name", name) }
	if name == "" || name == "@" || name == "HEAD" || strings.HasPrefix(name, "-") || !utf8.ValidString(name) {
		return invalid()
	}
	for _, r := range name {
		if !unicode.IsPrint(r) || unicode.Is(unicode.Cf, r) || r == ' ' {
			return invalid()
		}
	}
	if strings.ContainsAny(name, "~^:?*[\\") || strings.Contains(name, "..") || strings.Contains(name, "@{") || strings.HasSuffix(name, ".") {
		return invalid()
	}
	for _, part := range strings.Split(name, "/") {
		if part == "" || strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".lock") {
			return invalid()
		}
	}
	return nil
}

func resolveBase(repo, branch string) (string, error) {
	ref := "refs/heads/" + branch
	out, err := gitOutput(repo, "ls-remote", "--exit-code", "--heads", "origin", ref)
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 2 {
			return "", err
		}
	}
	found := false
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[1] == ref {
			found = true
			break
		}
	}
	if !found {
		return "", fmt.Errorf("branch %q does not exist on remote %q", branch, "origin")
	}
	tracking := "refs/remotes/origin/" + branch
	if _, err := gitOutput(repo, "fetch", "--no-tags", "origin", "+"+ref+":"+tracking); err != nil {
		return "", err
	}
	return gitOutput(repo, "rev-parse", "--verify", "--end-of-options", tracking+"^{commit}")
}
