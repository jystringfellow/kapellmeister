package profiles_test

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jystringfellow/kapellmeister/internal/loop"
	"github.com/jystringfellow/kapellmeister/profiles"
)

func TestTheEmbeddedBaseProfilesAreByteIdenticalToTheFilesUnderProfiles(t *testing.T) {
	embedded, err := fs.Glob(profiles.Files, "km-*.md")
	if err != nil {
		t.Fatal(err)
	}
	disk, err := filepath.Glob("km-*.md")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(embedded, ",") != strings.Join(disk, ",") {
		t.Fatalf("embedded %v; disk %v", embedded, disk)
	}
	for _, name := range embedded {
		got, err := profiles.Source(strings.TrimSuffix(name, ".md"))
		if err != nil {
			t.Fatal(err)
		}
		want, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("%s differs", name)
		}
	}
}

func TestEveryBuiltInAgentHasAnEmbeddedProfileOfTheNameAndProviderItsChairNames(t *testing.T) {
	for _, agent := range loop.BuiltInAgents() {
		source, err := profiles.Source(agent.Chair.Profile)
		if err != nil {
			t.Fatal(err)
		}
		for _, field := range []string{"name: " + agent.Chair.Profile, "provider: " + agent.Chair.Provider} {
			if !strings.Contains(string(source), "\n"+field+"\n") {
				t.Fatalf("%s missing %s", agent.Name, field)
			}
		}
	}
}

func TestBaseProfilesExistOnlyUnderTheProfilesDirectory(t *testing.T) {
	err := filepath.WalkDir("..", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == ".git" || d.Name() == ".kapellmeister") {
			return filepath.SkipDir
		}
		isBase := false
		for _, agent := range loop.BuiltInAgents() {
			isBase = isBase || d.Name() == agent.Chair.Profile+".md"
		}
		if !d.IsDir() && isBase && filepath.Dir(path) != filepath.Join("..", "profiles") {
			t.Errorf("duplicate profile %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
