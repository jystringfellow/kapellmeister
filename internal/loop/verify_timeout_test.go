package loop

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSetVerifyTimeoutAddsOrReplacesTheKeyInTheTestTableAndLeavesTheRestAlone(t *testing.T) {
	for before, after := range map[string]string{
		"[test]\nverify = 'scripts/verify'\n":                                                "[test]\nverify_timeout = \"20m0s\"\nverify = 'scripts/verify'\n",
		"# note\n[test] # tests\nverify = 'v'\nverify_timeout = '5m'\n\n[run]\nrounds = 2\n": "# note\n[test] # tests\nverify = 'v'\nverify_timeout = \"20m0s\"\n\n[run]\nrounds = 2\n",
		"[run]\nrounds = 2\n[test]\nverify = 'v'":                                            "[run]\nrounds = 2\n[test]\nverify_timeout = \"20m0s\"\nverify = 'v'",
	} {
		root := t.TempDir()
		path := filepath.Join(root, ConfigFile)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(before), 0640); err != nil {
			t.Fatal(err)
		}
		if err := SetVerifyTimeout(root, 20*time.Minute); err != nil {
			t.Fatal(before, err)
		}
		got, _ := os.ReadFile(path)
		info, _ := os.Stat(path)
		if string(got) != after || info.Mode().Perm() != 0640 {
			t.Fatalf("%q became %q (%v)", before, got, info.Mode())
		}
		if test, err := ResolveTest(root); err != nil || test.Timeout(time.Minute) != 20*time.Minute {
			t.Fatal(test, err)
		}
	}
}

func TestSetVerifyTimeoutLeavesAConfigItCannotRecordInUntouchedAndSaysSo(t *testing.T) {
	for _, before := range []string{"[run]\nrounds = 2\n", "test.verify = 'v'\n"} {
		root := t.TempDir()
		path := filepath.Join(root, ConfigFile)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(before), 0644); err != nil {
			t.Fatal(err)
		}
		err := SetVerifyTimeout(root, 20*time.Minute)
		got, _ := os.ReadFile(path)
		if err == nil || !strings.Contains(err.Error(), "verify_timeout") || string(got) != before {
			t.Fatalf("%q: %v, now %q", before, err, got)
		}
	}
}
