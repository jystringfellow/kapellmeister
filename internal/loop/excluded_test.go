package loop

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func excludedConfig(t *testing.T, body string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, filepath.Dir(ConfigFile)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ConfigFile), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestAnExcludedSuiteIsReadFromTheTestTableWithItsReasonMethodEvidenceAndCommand(t *testing.T) {
	root := excludedConfig(t, "[test]\nverify = 'scripts/verify'\n\n[[test.excluded]]\nsuite = 'e2e'\nreason = 'network'\nmethod = 'network namespace'\nevidence = 'exit 7: could not resolve host'\nrun = 'make e2e'\n")
	test, err := ResolveTest(root)
	want := Exclusion{Suite: "e2e", Reason: "network", Method: "network namespace", Evidence: "exit 7: could not resolve host", Run: "make e2e"}
	if err != nil || len(test.Excluded) != 1 || test.Excluded[0] != want {
		t.Fatal(test, err)
	}
	if got := want.String(); got != "e2e (network; network namespace): exit 7: could not resolve host; run it by hand: make e2e" {
		t.Fatal(got)
	}
}

func TestAnExcludedSuiteNeedsANameAndAReasonAndIsListedOnce(t *testing.T) {
	for _, bad := range []string{
		"[[test.excluded]]\nreason = 'network'\n",
		"[[test.excluded]]\nsuite = 'e2e'\n",
		"[[test.excluded]]\nsuite = 'e2e'\nreason = 'network'\n[[test.excluded]]\nsuite = 'e2e'\nreason = 'network'\n",
		"[[test.excluded]]\nsuite = 'e2e'\nreason = 'network'\nwhy = 'x'\n",
	} {
		if _, err := ResolveTest(excludedConfig(t, "[test]\nverify = 'scripts/verify'\n"+bad)); err == nil || !strings.Contains(err.Error(), "test.excluded") && !strings.Contains(err.Error(), "unknown keys") {
			t.Fatal(bad, err)
		}
	}
}

func TestKmWritesExcludedSuitesIntoTheConfigReplacingTheOnesThereAndKeepingEverythingElse(t *testing.T) {
	root := excludedConfig(t, "# kept\n[test]\nverify = 'scripts/verify'\n\n[[test.excluded]]\nsuite = 'old'\nreason = 'network'\n\n[run]\nrounds = 2\n")
	entries := []Exclusion{
		{Suite: "e2e", Reason: "network", Method: "proxy variables (best effort)", Evidence: "exit 7: \"quoted\" and 'single'\\back", Run: "make e2e"},
		{Suite: "load", Reason: "network", Method: "network namespace", Evidence: "exit 1", Run: "make load"},
	}
	if err := SetExcluded(root, entries); err != nil {
		t.Fatal(err)
	}
	test, err := ResolveTest(root)
	if err != nil || !slices.Equal(test.Excluded, entries) {
		t.Fatal(test, err)
	}
	data, _ := os.ReadFile(filepath.Join(root, ConfigFile))
	for _, want := range []string{"# kept\n", "[run]\nrounds = 2\n", "verify = 'scripts/verify'"} {
		if !strings.Contains(string(data), want) {
			t.Fatal(string(data))
		}
	}
	if strings.Contains(string(data), "old") {
		t.Fatal(string(data))
	}
	if err := SetExcluded(root, nil); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(filepath.Join(root, ConfigFile))
	if strings.Contains(string(data), "excluded") || string(data) != WithoutExcluded(string(data)) {
		t.Fatal(string(data))
	}
}

func TestAConfigComparedWithoutItsExcludedSuitesKeepsEveryOtherLine(t *testing.T) {
	config := "[test]\nverify = 'v'\n\n[[test.excluded]]\nsuite = 'e2e'\nreason = 'network'\n\n[run]\nrounds = 2\n"
	if got := WithoutExcluded(config); got != "[test]\nverify = 'v'\n\n[run]\nrounds = 2\n" {
		t.Fatalf("%q", got)
	}
}
