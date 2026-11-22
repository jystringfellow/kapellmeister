package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jystringfellow/kapellmeister/internal/loop"
)

type concurrentSave struct {
	path     string
	replaced bool
	content  string
	err      error
}

func concurrentEnsembleSaves(wt, name string, repository, bothLocations bool) []concurrentSave {
	const count = 32
	start := make(chan struct{})
	done := make(chan concurrentSave, count)
	for i := range count {
		go func() {
			content := fmt.Sprintf("%s\n# source %d\n", sectionedEnsemble, i)
			<-start
			path, replaced, err := saveEnsemble(name, wt, []byte(content), repository || bothLocations && i%2 == 0, false, loop.Settings{})
			done <- concurrentSave{path, replaced, content, err}
		}()
	}
	close(start)
	results := make([]concurrentSave, 0, count)
	for range count {
		results = append(results, <-done)
	}
	return results
}

func TestConcurrentSavesWithoutReplaceKeepOnlyTheFirstFile(t *testing.T) {
	for _, repository := range []bool{false, true} {
		t.Run(fmt.Sprintf("repository=%t", repository), func(t *testing.T) {
			home, wt := t.TempDir(), t.TempDir()
			t.Setenv(loop.HomeEnv, home)
			for round := range 20 {
				name := fmt.Sprintf("sample-%d", round)
				results := concurrentEnsembleSaves(wt, name, repository, false)
				var winner concurrentSave
				successes := 0
				for _, result := range results {
					if result.err == nil {
						successes++
						winner = result
					} else if !strings.Contains(result.err.Error(), "-replace") {
						t.Errorf("unexpected refusal: %v", result.err)
					}
				}
				if successes != 1 {
					t.Fatalf("round %d: %d saves succeeded without -replace; want exactly one", round, successes)
				}
				data, err := os.ReadFile(winner.path)
				if err != nil || string(data) != winner.content || winner.replaced {
					t.Fatalf("saved bytes differ from the sole successful save: %v, replaced=%t", err, winner.replaced)
				}
			}
		})
	}
}

func TestConcurrentUserAndRepositorySavesCannotLeaveAShadowedFile(t *testing.T) {
	home, wt := t.TempDir(), t.TempDir()
	t.Setenv(loop.HomeEnv, home)
	for round := range 20 {
		name := fmt.Sprintf("sample-%d", round)
		results := concurrentEnsembleSaves(wt, name, false, true)
		successes := 0
		for _, result := range results {
			if result.err == nil {
				successes++
			} else if !strings.Contains(result.err.Error(), "shadow") && !strings.Contains(result.err.Error(), "already exists") {
				t.Errorf("unexpected refusal: %v", result.err)
			}
		}
		if successes != 1 {
			t.Fatalf("round %d: %d saves succeeded; want exactly one across both locations", round, successes)
		}
		files := 0
		for _, path := range []string{filepath.Join(home, "ensembles", name+".toml"), filepath.Join(wt, ".kapellmeister", "ensembles", name+".toml")} {
			if _, err := os.Stat(path); err == nil {
				files++
			} else if !os.IsNotExist(err) {
				t.Fatal(err)
			}
		}
		if files != 1 {
			t.Fatalf("round %d: %d files saved; want one without shadowing", round, files)
		}
	}
}
