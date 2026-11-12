package loop

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestReportDirsAreReadFromTheUserConfigAndExpandTildeToTheHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	absolute := filepath.Join(t.TempDir(), "records")
	config := writeUserConfig(t, fmt.Sprintf("[report]\ndirs = [%q, %q]\n", "~/kept", absolute))
	s, err := LoadSettings(config, "")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := s.ReportDirs, []string{filepath.Join(home, "kept"), absolute}; !reflect.DeepEqual(got, want) {
		t.Fatalf("dirs = %v, want %v", got, want)
	}
}

func TestReportDirsBelongInTheUserConfigAndARepositoryConfigNamingThemIsRefused(t *testing.T) {
	for _, body := range []string{"[report]\n", "[report]\ndirs = []\n", "[report]\ndirs = ['/records']\n"} {
		t.Run(body, func(t *testing.T) {
			home, repo := t.TempDir(), t.TempDir()
			t.Setenv(HomeEnv, home)
			writeConfig(t, repo, body)
			_, settingsErr := LoadSettings(home, repo)
			_, baseErr := RepoBaseBranch([]byte(body))
			for _, err := range []error{settingsErr, baseErr} {
				if err == nil || !strings.Contains(err.Error(), ConfigFile+": [report] belongs in "+UserConfigPath(home)) {
					t.Fatalf("error = %v", err)
				}
			}
		})
	}
}

func TestAnUnknownKeyUnderReportIsAnError(t *testing.T) {
	home := writeUserConfig(t, "[report]\ndirectories = ['/kept']\n")
	_, err := LoadSettings(home, "")
	if err == nil || !strings.Contains(err.Error(), UserConfigPath(home)+": unknown keys") || !strings.Contains(err.Error(), "report.directories") {
		t.Fatalf("error = %v", err)
	}
}

func TestARelativeOrEmptyReportDirIsRefusedNamingItsKey(t *testing.T) {
	for _, path := range []string{"relative", "~", "", "  "} {
		t.Run(path, func(t *testing.T) {
			home := writeUserConfig(t, fmt.Sprintf("[report]\ndirs = ['/valid', %q]\n", path))
			_, err := LoadSettings(home, "")
			if err == nil || !strings.Contains(err.Error(), UserConfigPath(home)+": report.dirs[1]:") {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestReportDirsDropExactDuplicatesAfterCleaningAndKeepTheFirstPosition(t *testing.T) {
	home := writeUserConfig(t, "[report]\ndirs = ['/a/b', '/second', '/a/b/', '/a/./b']\n")
	s, err := LoadSettings(home, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := s.ReportDirs; !reflect.DeepEqual(got, []string{"/a/b", "/second"}) {
		t.Fatal(got)
	}
}

func TestWithoutAReportTableThereAreNoReportDirs(t *testing.T) {
	for _, body := range []string{"", "[report]\n", "[report]\ndirs = []\n"} {
		home := writeUserConfig(t, body)
		s, err := LoadSettings(home, "")
		if err != nil {
			t.Fatal(err)
		}
		data, err := json.Marshal(s)
		if err != nil {
			t.Fatal(err)
		}
		if got := s.ReportDirs; got != nil || strings.Contains(string(data), "report_dirs") {
			t.Fatalf("settings = %s", data)
		}
	}
}

func TestReportDirsMustBeAListOfStrings(t *testing.T) {
	for _, value := range []string{`"/kept"`, "[42]"} {
		home := writeUserConfig(t, "[report]\ndirs = "+value+"\n")
		_, err := LoadSettings(home, "")
		if err == nil || !strings.Contains(err.Error(), UserConfigPath(home)) {
			t.Fatalf("error = %v", err)
		}
	}
}

func TestLoadReportDirsReadsOnlyTheUserConfig(t *testing.T) {
	home := t.TempDir()
	dirs, source, err := LoadReportDirs(home)
	if err != nil || dirs != nil || source != "" {
		t.Fatalf("missing config: dirs = %v, source = %q, error = %v", dirs, source, err)
	}
	home = writeUserConfig(t, "[report]\ndirs = ['/kept/./runs', '/kept/runs/']\n[chairs]\ninvalid_role = 'invalid_agent'\n")
	dirs, source, err = LoadReportDirs(home)
	if err != nil || source != UserConfigPath(home) || !reflect.DeepEqual(dirs, []string{"/kept/runs"}) {
		t.Fatalf("dirs = %v, source = %q, error = %v", dirs, source, err)
	}
	for _, body := range []string{"unknown = true\n", "[report]\ndirs = ['relative']\n"} {
		home = writeUserConfig(t, body)
		if _, _, err := LoadReportDirs(home); err == nil || !strings.Contains(err.Error(), UserConfigPath(home)) {
			t.Fatalf("error = %v", err)
		}
	}
}
