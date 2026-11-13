package trust

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/BurntSushi/toml"
)

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@t"}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %s: %v", args, out, err)
	}
}
func fixture(t *testing.T) (string, string, *Folders, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	git(t, dir, "init")
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	out, diagnostic := &bytes.Buffer{}, &bytes.Buffer{}
	return dir, filepath.Join(home, "config.toml"), &Folders{Enabled: true, Out: out, Err: diagnostic}, out, diagnostic
}
func contents(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
func put(t *testing.T, path, data string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
}
func TestTheSettingOffChangesNothingAndCreatesNoCodexConfig(t *testing.T) {
	dir, path, f, out, diagnostic := fixture(t)
	f.Enabled = false
	f.BeforeLaunch("codex", dir)
	if _, err := os.Stat(path); !os.IsNotExist(err) || out.Len() != 0 || diagnostic.Len() != 0 {
		t.Fatal(err, out, diagnostic)
	}
}
func TestCodexEntryIsAppendedOnceAndTheRestOfTheFileIsByteIdentical(t *testing.T) {
	dir, path, f, out, diagnostic := fixture(t)
	before := "# retain formatting\nmodel   = 'gpt-6'\n[other]\nkey=  7"
	put(t, path, before)
	f.BeforeLaunch("codex", dir)
	after := contents(t, path)
	f.BeforeLaunch("codex", dir)
	(&Folders{Enabled: true, Out: out, Err: diagnostic}).BeforeLaunch("codex", dir)
	if !strings.HasPrefix(after, before+"\n\n[projects.") || contents(t, path) != after || strings.Count(out.String(), "\n") != 1 || !strings.Contains(out.String(), "as trusted for Codex") || !strings.Contains(out.String(), dir) || diagnostic.Len() != 0 {
		t.Fatal(after, out, diagnostic)
	}
}
func TestAnExistingEntryForTheFolderIsLeftAloneWhateverItsTrustLevel(t *testing.T) {
	for _, level := range []string{"trusted", "untrusted", ""} {
		t.Run(level, func(t *testing.T) {
			dir, path, f, out, diagnostic := fixture(t)
			before := fmt.Sprintf("[projects.%q]\ntrust_level = %q\n", dir, level)
			put(t, path, before)
			f.BeforeLaunch("codex", dir)
			if contents(t, path) != before || out.Len() != 0 || diagnostic.Len() != 0 {
				t.Fatal(out, diagnostic)
			}
		})
	}
}
func TestAnUnparseableCodexConfigIsLeftAloneWithADiagnostic(t *testing.T) {
	assertInvalid(t, "[broken")
}
func TestAProjectsInlineTableThatAppendingWouldBreakIsLeftAloneWithADiagnostic(t *testing.T) {
	assertInvalid(t, "projects = {}\n")
}
func assertInvalid(t *testing.T, before string) {
	t.Helper()
	dir, path, f, out, diagnostic := fixture(t)
	put(t, path, before)
	f.BeforeLaunch("codex", dir)
	f.BeforeLaunch("codex", dir)
	if contents(t, path) != before || out.Len() != 0 || !strings.Contains(diagnostic.String(), path) || !strings.Contains(diagnostic.String(), "Codex may still ask") || strings.Count(diagnostic.String(), "\n") != 1 {
		t.Fatal(out, diagnostic)
	}
}
func TestAMissingCodexConfigIsCreatedWithOnlyTheEntry(t *testing.T) {
	dir, path, f, _, diagnostic := fixture(t)
	f.BeforeLaunch("codex", dir)
	var cfg map[string]any
	if _, err := toml.Decode(contents(t, path), &cfg); err != nil || len(cfg) != 1 || diagnostic.Len() != 0 {
		t.Fatal(cfg, err, diagnostic)
	}
}
func TestNothingIsWrittenForTheOtherAgentsOrUnknownProviders(t *testing.T) {
	dir, path, f, out, diagnostic := fixture(t)
	for _, provider := range []string{"claude_code", "copilot_cli", "antigravity_cli", "other", ""} {
		f.BeforeLaunch(provider, dir)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) || out.Len() != 0 || diagnostic.Len() != 0 {
		t.Fatal(err, out, diagnostic)
	}
}
func TestNothingIsWrittenOutsideAGitRepository(t *testing.T) {
	_, path, f, out, diagnostic := fixture(t)
	f.BeforeLaunch("codex", t.TempDir())
	if _, err := os.Stat(path); !os.IsNotExist(err) || out.Len() != 0 || diagnostic.Len() != 0 {
		t.Fatal(err, out, diagnostic)
	}
}
func TestTheTrustKeyIsTheMainRepositoryRootForALinkedWorktreeAndASubdirectory(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(fmt.Sprint(existing), func(t *testing.T) {
			dir, path, f, out, diagnostic := fixture(t)
			git(t, dir, "commit", "--allow-empty", "-m", "initial")
			wt := filepath.Join(t.TempDir(), "wt")
			git(t, dir, "worktree", "add", "--detach", wt)
			sub := filepath.Join(wt, "sub")
			if err := os.Mkdir(sub, 0700); err != nil {
				t.Fatal(err)
			}
			link := filepath.Join(t.TempDir(), "link")
			if err := os.Symlink(sub, link); err != nil {
				t.Fatal(err)
			}
			if existing {
				put(t, path, fmt.Sprintf("[projects.%q]\ntrust_level='untrusted'\n", sub))
			}
			f.BeforeLaunch("codex", link)
			if existing {
				if out.Len() != 0 || strings.Contains(contents(t, path), dir) {
					t.Fatal(out, contents(t, path))
				}
			} else if !strings.Contains(contents(t, path), fmt.Sprintf("[projects.%q]", dir)) {
				t.Fatal(contents(t, path))
			}
			if diagnostic.Len() != 0 {
				t.Fatal(diagnostic)
			}
		})
	}
}
func TestCodexHomeDecidesWhichConfigIsEdited(t *testing.T) {
	dir, _, f, _, _ := fixture(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", "")
	f.BeforeLaunch("codex", dir)
	if !strings.Contains(contents(t, filepath.Join(home, ".codex", "config.toml")), "trust_level") {
		t.Fatal("missing entry")
	}
}
func TestAKeyWithQuotesAndBackslashesIsEscapedAndReadsBack(t *testing.T) {
	_, path, f, _, diagnostic := fixture(t)
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(base, "quote\"slash\\tab\t")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "init")
	f.BeforeLaunch("codex", dir)
	var cfg struct {
		Projects map[string]struct {
			TrustLevel string `toml:"trust_level"`
		}
	}
	if _, err := toml.Decode(contents(t, path), &cfg); err != nil || cfg.Projects[dir].TrustLevel != "trusted" || diagnostic.Len() != 0 {
		t.Fatal(err, cfg, diagnostic)
	}
}
func TestConcurrentChairsInOneFolderAppendOneEntry(t *testing.T) {
	dir, path, f, out, diagnostic := fixture(t)
	var wg sync.WaitGroup
	for range 12 {
		wg.Add(1)
		go func() { defer wg.Done(); f.BeforeLaunch("codex", dir) }()
	}
	wg.Wait()
	if strings.Count(contents(t, path), "trust_level") != 1 || strings.Count(out.String(), "\n") != 1 || diagnostic.Len() != 0 {
		t.Fatal(out, diagnostic)
	}
}

func TestAnUnrelatedProjectsAssignmentDoesNotPreventRecordingCodexTrust(t *testing.T) {
	for _, before := range []string{
		"[other]\nprojects={}\n",
		"[projects]\n[other]\nprojects={}\n",
		"note=\"\"\"\nprojects={}\n\"\"\"\n[projects]\n",
	} {
		t.Run(before, func(t *testing.T) {
			dir, path, f, out, diagnostic := fixture(t)
			put(t, path, before)
			f.BeforeLaunch("codex", dir)
			if !strings.HasPrefix(contents(t, path), before) || !strings.Contains(contents(t, path), "trust_level") || out.Len() == 0 || diagnostic.Len() != 0 {
				t.Fatal(contents(t, path), out, diagnostic)
			}
		})
	}
}
