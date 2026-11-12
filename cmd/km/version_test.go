package main

import (
	"bytes"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strings"
	"testing"
)

func TestVersionPrintsTheInjectedVersionForAnLdflagsBuild(t *testing.T) {
	if testing.Short() {
		t.Skip("builds km")
	}
	binary := filepath.Join(t.TempDir(), "km")
	cmd := exec.Command("go", "build", "-ldflags", "-X main.version=v9.9.9", "-o", binary, ".")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build: %v: %s", err, out)
	}
	cmd = exec.Command(binary, "version")
	cmd.Dir = t.TempDir()
	if out, err := cmd.CombinedOutput(); err != nil || string(out) != "v9.9.9\n" {
		t.Fatalf("version = %q, %v", out, err)
	}
}

func TestVersionOfAnUnreleasedBuildCannotBeMistakenForARelease(t *testing.T) {
	for _, tc := range []struct {
		name, injected, module, revision string
		dirty, have                      bool
		want                             string
	}{
		{name: "no info", injected: "dev", want: "dev"},
		{name: "revision", injected: "dev", revision: "1234567890abcdef", have: true, want: "dev+1234567890ab"},
		{name: "dirty", injected: "dev", revision: "abc", dirty: true, have: true, want: "dev+abc-dirty"},
		{name: "module", injected: "dev", module: "v1.2.3", have: true, want: "v1.2.3"},
		{name: "devel", injected: "dev", module: "(devel)", have: true, want: "dev"},
		{name: "injected", injected: "v9.9.9", module: "v1.2.3", have: true, want: "v9.9.9"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			info := &debug.BuildInfo{Main: debug.Module{Version: tc.module}, Settings: []debug.BuildSetting{{Key: "vcs.revision", Value: tc.revision}}}
			if tc.dirty {
				info.Settings = append(info.Settings, debug.BuildSetting{Key: "vcs.modified", Value: "true"})
			}
			if got := versionString(tc.injected, info, tc.have); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestVersionRejectsExtraArguments(t *testing.T) {
	var stderr bytes.Buffer
	if code := execute([]string{"version", "extra"}, &stderr); code != 2 || !strings.Contains(stderr.String(), "km version") {
		t.Fatalf("exit=%d stderr=%s", code, &stderr)
	}
}

func TestAWorkspaceVersionWithGoGeneratedModuleVersionStillSaysDev(t *testing.T) {
	info := &debug.BuildInfo{Main: debug.Module{Version: "v0.0.0-20261112042900-544ee2141335+dirty"}, Settings: []debug.BuildSetting{{Key: "vcs.revision", Value: "544ee2141335c7d6"}, {Key: "vcs.modified", Value: "true"}}}
	if got := versionString("dev", info, true); got != "dev+544ee2141335-dirty" {
		t.Fatalf("workspace version = %q", got)
	}
	info.Main.Version = "v1.2.3"
	if got := versionString("dev", info, true); got != "dev+544ee2141335-dirty" {
		t.Fatalf("tagged workspace version = %q", got)
	}
}
