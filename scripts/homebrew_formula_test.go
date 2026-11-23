package scripts

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var platforms = []string{"darwin_arm64", "darwin_amd64", "linux_arm64", "linux_amd64"}
var hashes = []string{strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64), strings.Repeat("d", 64)}

func formulaFixture(t *testing.T, tag string, omit int, replacement string) string {
	t.Helper()
	var sums strings.Builder
	for i, platform := range platforms {
		if i == omit {
			continue
		}
		hash := hashes[i]
		if i == 0 && replacement != "" {
			hash = replacement
		}
		fmt.Fprintf(&sums, "%s  km_%s_%s.tar.gz\n", hash, tag, platform)
	}
	sums.WriteString(strings.Repeat("e", 64) + "  unrelated.tar.gz\n")
	path := filepath.Join(t.TempDir(), "SHA256SUMS")
	if err := os.WriteFile(path, []byte(sums.String()), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func renderFormula(t *testing.T, shell string, args ...string) (string, string, error) {
	t.Helper()
	c := exec.Command(shell, append([]string{"homebrew-formula"}, args...)...)
	var out, stderr bytes.Buffer
	c.Stdout, c.Stderr = &out, &stderr
	err := c.Run()
	return out.String(), stderr.String(), err
}

const formulaTemplate = "../packaging/homebrew/kapellmeister.rb.tmpl"

func TestRenderingPutsTheVersionAndEachChecksumOnItsPlatformsLine(t *testing.T) {
	for _, shell := range shells(t) {
		t.Run(shell, func(t *testing.T) {
			tag := "v0.0.1-rc.6"
			out, stderr, err := renderFormula(t, shell, tag, formulaFixture(t, tag, -1, ""), formulaTemplate)
			if err != nil {
				t.Fatalf("%v: %s", err, stderr)
			}
			for i, platform := range platforms {
				want := fmt.Sprintf("releases/download/%s/km_%s_%s.tar.gz\"\n      sha256 \"%s\"", tag, tag, platform, hashes[i])
				if !strings.Contains(out, want) {
					t.Errorf("missing %s", want)
				}
			}
			if strings.Count(out, "releases/download/"+tag+"/") != 4 || strings.Contains(out, "{{") || regexp.MustCompile(`(?m)^\s*version\b`).MatchString(out) {
				t.Errorf("invalid formula: %s", out)
			}
		})
	}
}

func TestTheCheckedInTemplateRenders(t *testing.T) {
	for _, shell := range shells(t) {
		t.Run(shell, func(t *testing.T) {
			out, stderr, err := renderFormula(t, shell, "v1.2.3", formulaFixture(t, "v1.2.3", -1, ""), formulaTemplate)
			if err != nil {
				t.Fatalf("%v: %s", err, stderr)
			}
			for _, want := range []string{"class Kapellmeister < Formula", `bin.install "km"`, `license "Apache-2.0"`} {
				if !strings.Contains(out, want) {
					t.Errorf("missing %s", want)
				}
			}
			if strings.Contains(out, "{{") {
				t.Error("unrendered placeholder")
			}
		})
	}
}

func TestAMissingArchiveEntryIsRefusedWithoutOutput(t *testing.T) {
	for _, shell := range shells(t) {
		for i, platform := range platforms {
			t.Run(shell+"/"+platform, func(t *testing.T) {
				out, stderr, err := renderFormula(t, shell, "v1.2.3", formulaFixture(t, "v1.2.3", i, ""), formulaTemplate)
				if err == nil || out != "" || !strings.Contains(stderr, "km_v1.2.3_"+platform+".tar.gz") {
					t.Fatalf("%v stdout %q stderr %q", err, out, stderr)
				}
			})
		}
	}
}

func TestAMalformedChecksumIsRefusedWithoutOutput(t *testing.T) {
	for _, shell := range shells(t) {
		for _, hash := range []string{strings.Repeat("a", 63), strings.Repeat("a", 65), strings.Repeat("A", 64), strings.Repeat("g", 64)} {
			t.Run(shell+"/"+hash, func(t *testing.T) {
				out, stderr, err := renderFormula(t, shell, "v1.2.3", formulaFixture(t, "v1.2.3", -1, hash), formulaTemplate)
				if err == nil || out != "" || !strings.Contains(stderr, "km_v1.2.3_darwin_arm64.tar.gz") {
					t.Fatalf("%v stdout %q stderr %q", err, out, stderr)
				}
			})
		}
	}
}

func TestAnInvalidTagIsRefusedWithoutOutput(t *testing.T) {
	for _, shell := range shells(t) {
		for _, tag := range []string{"1.2.3", "v"} {
			t.Run(shell+"/"+tag, func(t *testing.T) {
				out, stderr, err := renderFormula(t, shell, tag, formulaFixture(t, tag, -1, ""), formulaTemplate)
				if err == nil || out != "" || stderr == "" {
					t.Fatalf("%v stdout %q stderr %q", err, out, stderr)
				}
			})
		}
	}
}

func TestAnUnknownPlaceholderIsRefusedWithoutOutput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "template")
	if err := os.WriteFile(path, []byte("{{NOT_A_PLACEHOLDER}}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, shell := range shells(t) {
		t.Run(shell, func(t *testing.T) {
			out, stderr, err := renderFormula(t, shell, "v1.2.3", formulaFixture(t, "v1.2.3", -1, ""), path)
			if err == nil || out != "" || !strings.Contains(stderr, "{{NOT_A_PLACEHOLDER}}") {
				t.Fatalf("%v stdout %q stderr %q", err, out, stderr)
			}
		})
	}
}

func TestTheRenderedFormulaPassesRubySyntaxCheckWhenRubyIsAvailable(t *testing.T) {
	ruby, err := exec.LookPath("ruby")
	if err != nil {
		t.Skip("ruby unavailable")
	}
	for _, shell := range shells(t) {
		t.Run(shell, func(t *testing.T) {
			out, stderr, err := renderFormula(t, shell, "v1.2.3", formulaFixture(t, "v1.2.3", -1, ""), formulaTemplate)
			if err != nil {
				t.Fatalf("%v: %s", err, stderr)
			}
			path := filepath.Join(t.TempDir(), "formula.rb")
			if err := os.WriteFile(path, []byte(out), 0600); err != nil {
				t.Fatal(err)
			}
			if result, err := exec.Command(ruby, "-c", path).CombinedOutput(); err != nil {
				t.Fatalf("%v: %s", err, result)
			}
		})
	}
}

func TestAWrongNumberOfArgumentsIsAUsageError(t *testing.T) {
	for _, shell := range shells(t) {
		t.Run(shell, func(t *testing.T) {
			out, stderr, err := renderFormula(t, shell)
			exit, ok := err.(*exec.ExitError)
			if !ok || exit.ExitCode() != 2 || out != "" || !strings.Contains(stderr, "usage:") {
				t.Fatalf("%v stdout %q stderr %q", err, out, stderr)
			}
		})
	}
}
