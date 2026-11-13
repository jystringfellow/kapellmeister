package prove

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func copyFixture(t *testing.T) (string, string) {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "scripts"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".kapellmeister"), 0755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"file": "HEAD\n", "scripts/verify": "HEAD verify\n", ".kapellmeister/config.toml": "HEAD config\n"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	for _, args := range [][]string{{"init", "-q"}, {"add", "."}, {"-c", "user.name=t", "-c", "user.email=t@example.invalid", "commit", "-qm", "fixture"}, {"checkout", "--detach", "-q"}} {
		if _, err := git(ctx, root, args...); err != nil {
			t.Fatal(err)
		}
	}
	head, err := git(ctx, root, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	return root, head
}

func TestCleanCopiesUseHEADAndOnlyTheCurrentSetupBytesAndModes(t *testing.T) {
	root, head := copyFixture(t)
	for name, body := range map[string]string{"file": "uncommitted\n", "untracked": "untracked\n", "scripts/verify": "current verify\n", ".kapellmeister/config.toml": "current config\n"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(filepath.Join(root, "scripts/verify"), 0755); err != nil {
		t.Fatal(err)
	}
	setup, err := readSetup(root)
	if err != nil {
		t.Fatal(err)
	}
	dir, err := cleanCopy(context.Background(), root, head, t.TempDir(), "copy", setup)
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"file": "HEAD\n", "scripts/verify": "current verify\n", ".kapellmeister/config.toml": "current config\n"} {
		body, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || string(body) != want {
			t.Fatal(name, string(body), err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "untracked")); !os.IsNotExist(err) {
		t.Fatal("untracked file copied", err)
	}
	info, err := os.Stat(filepath.Join(dir, "scripts/verify"))
	if err != nil || info.Mode().Perm() != 0755 {
		t.Fatal(info, err)
	}
}

func TestCleanCopiesRemoveSetupFilesMissingFromTheCheckout(t *testing.T) {
	root, head := copyFixture(t)
	for _, name := range []string{"scripts/verify", ".kapellmeister/config.toml"} {
		if err := os.Remove(filepath.Join(root, name)); err != nil {
			t.Fatal(err)
		}
	}
	setup, err := readSetup(root)
	if err != nil {
		t.Fatal(err)
	}
	dir, err := cleanCopy(context.Background(), root, head, t.TempDir(), "copy", setup)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"scripts/verify", ".kapellmeister/config.toml"} {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Fatal(name, err)
		}
	}
}

func TestSetupSnapshotsRecogniseSymlinkTargetsWithoutFollowingThem(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, ".kapellmeister")); err != nil {
		t.Fatal(err)
	}
	before, err := setupSnapshot(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "other"), []byte("not setup"), 0644); err != nil {
		t.Fatal(err)
	}
	after, err := setupSnapshot(root)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal(after, err)
	}
	if err := os.Remove(filepath.Join(root, ".kapellmeister")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(root, ".kapellmeister")); err != nil {
		t.Fatal(err)
	}
	after, err = setupSnapshot(root)
	if err != nil || reflect.DeepEqual(before, after) {
		t.Fatal(after, err)
	}
}
