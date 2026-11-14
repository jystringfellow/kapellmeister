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

func TestReportPricesAreReadFromTheUserConfigPerModelInDollarsPerMillionTokens(t *testing.T) {
	home := writeUserConfig(t, "[report.prices.\"model.a\"]\ninput = 0.1\noutput = 2\n[report.prices.Zero]\ninput = 0\noutput = 0.0\n")
	s, err := LoadSettings(home, "")
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(s)
	var fields map[string]any
	json.Unmarshal(data, &fields)
	want := map[string]any{"model.a": map[string]any{"input": 0.1, "output": float64(2)}, "Zero": map[string]any{"input": float64(0), "output": float64(0)}}
	if !reflect.DeepEqual(fields["report_prices"], want) {
		t.Fatalf("settings = %s", data)
	}
}

func TestReportPricesInARepositoryConfigAreRefusedLikeAnyOtherUserOnlySetting(t *testing.T) {
	home, repo := t.TempDir(), t.TempDir()
	t.Setenv(HomeEnv, home)
	for _, body := range []string{"[report.prices.m]\ninput=1\noutput=2\n", "[report.prices.m]\ninput='bad'\n"} {
		writeConfig(t, repo, body)
		_, err := LoadSettings(home, repo)
		_, baseErr := RepoBaseBranch([]byte(body))
		for _, e := range []error{err, baseErr} {
			if e == nil || !strings.Contains(e.Error(), ConfigFile+": [report] belongs in "+UserConfigPath(home)) {
				t.Fatalf("error = %v", e)
			}
		}
	}
}

func TestANegativeOrNonNumericReportPriceIsRejectedNamingTheModelAndTheFile(t *testing.T) {
	for _, body := range []string{"input=-1\noutput=2", "input=1\noutput='bad'", "input=true\noutput=2", "input=nan\noutput=2", "input=1\noutput=inf", "input=1", "output=2", "input=1\noutput=2\nextra=3"} {
		t.Run(body, func(t *testing.T) {
			home := writeUserConfig(t, "[report.prices.\"model.a\"]\n"+body+"\n")
			_, err := LoadSettings(home, "")
			if err == nil || !strings.Contains(err.Error(), UserConfigPath(home)) || !strings.Contains(err.Error(), `report.prices."model.a"`) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestWithoutPricesSettingsOmitReportPrices(t *testing.T) {
	for _, body := range []string{"", "[report]\n", "[report.prices]\n"} {
		home := writeUserConfig(t, body)
		s, err := LoadSettings(home, "")
		if err != nil {
			t.Fatal(err)
		}
		data, _ := json.Marshal(s)
		if strings.Contains(string(data), "report_prices") {
			t.Fatal(string(data))
		}
	}
}

func TestLoadReportPricesReadsOnlyTheUserConfig(t *testing.T) {
	home := writeUserConfig(t, "[report.prices.m]\ninput=1\noutput=2\n[chairs]\ninvalid_role='invalid_agent'\n")
	prices, source, err := LoadReportPrices(home)
	if err != nil || source != UserConfigPath(home) || prices["m"].Input != 1 || prices["m"].Output != 2 {
		t.Fatalf("prices=%v source=%q error=%v", prices, source, err)
	}
	prices, source, err = LoadReportPrices(t.TempDir())
	if err != nil || source != "" || len(prices) != 0 {
		t.Fatalf("prices=%v source=%q error=%v", prices, source, err)
	}
}

func TestReportPricesMustBeATableWhenSettingsOrPricesAreLoaded(t *testing.T) {
	for _, body := range []string{"[report]\nprices='x'\n", "[report]\nprices=3\n", "[report]\nprices=true\n", "[report]\nprices=[]\n", "[[report.prices]]\ninput=1\noutput=2\n"} {
		t.Run(body, func(t *testing.T) {
			home := writeUserConfig(t, body)
			_, settingsErr := LoadSettings(home, "")
			_, source, pricesErr := LoadReportPrices(home)
			for _, err := range []error{settingsErr, pricesErr} {
				if err == nil || !strings.Contains(err.Error(), UserConfigPath(home)+": report.prices:") || !strings.Contains(err.Error(), "must be a table") {
					t.Errorf("error=%v", err)
				}
			}
			if source != UserConfigPath(home) {
				t.Errorf("source=%q", source)
			}
		})
	}
}

func TestReportPriceParsingFailuresRetainTheUserConfigAsTheirSource(t *testing.T) {
	for name, body := range map[string]string{
		"float overflow":   "[report.prices.alpha]\ninput=1e400\noutput=2\n",
		"integer overflow": "[report.prices.alpha]\ninput=99999999999999999999\noutput=2\n",
		"duplicate model":  "[report.prices.alpha]\ninput=1\noutput=2\n[report.prices.alpha]\ninput=3\noutput=4\n",
	} {
		t.Run(name, func(t *testing.T) {
			home := writeUserConfig(t, body)
			_, source, err := LoadReportPrices(home)
			if err == nil || source != UserConfigPath(home) || !strings.Contains(err.Error(), UserConfigPath(home)) || !strings.Contains(err.Error(), "report.prices") {
				t.Fatalf("source=%q error=%v", source, err)
			}
		})
	}
}
