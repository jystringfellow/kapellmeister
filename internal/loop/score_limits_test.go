package loop

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestScoreAndPartFilesOverTheirLimitsAreRefusedNamingTheFileItsSizeAndTheLimit(t *testing.T) {
	for _, repository := range []bool{false, true} {
		for _, kind := range []string{"score", "part"} {
			t.Run(fmt.Sprintf("%s repository=%v", kind, repository), func(t *testing.T) {
				s := scoreFileSource(t)
				limit := 64 << 10
				rel := "scores/small.toml"
				text := smallScoreFile
				if kind == "part" {
					limit = 256 << 10
					rel = "parts/drafter.md"
					text = "Draft"
				}
				text += strings.Repeat(" ", limit+1-len(text))
				root := s.Home
				path := filepath.Join(root, rel)
				if repository {
					s.Worktree = t.TempDir()
					root = s.Worktree
					path = ".kapellmeister/" + rel
					scoreFileGit(t, root, "init", "-q")
					if err := os.Remove(filepath.Join(s.Home, rel)); err != nil {
						t.Fatal(err)
					}
					scoreFileWrite(t, root, path, text)
					scoreFileGit(t, root, "add", ".")
					scoreFileGit(t, root, "-c", "commit.gpgsign=false", "commit", "-qm", "fixture")
				} else {
					scoreFileWrite(t, root, rel, text)
				}
				_, err := LoadScore("small", s)
				if err == nil {
					t.Fatal("oversized file accepted")
				}
				for _, want := range []string{path, fmt.Sprint(limit + 1), fmt.Sprint(limit)} {
					if !strings.Contains(err.Error(), want) {
						t.Fatalf("error %v missing %s", err, want)
					}
				}
			})
		}
	}
}

func TestScoreAndPartFilesAtTheirLimitsAreAccepted(t *testing.T) {
	for _, repository := range []bool{false, true} {
		t.Run(fmt.Sprintf("repository=%v", repository), func(t *testing.T) {
			s := ScoreSource{Home: t.TempDir(), AllowRepository: true}
			root, prefix := s.Home, ""
			if repository {
				s.Worktree = t.TempDir()
				root, prefix = s.Worktree, ".kapellmeister/"
				scoreFileGit(t, root, "init", "-q")
			}
			scoreFileWrite(t, root, prefix+"scores/small.toml", smallScoreFile+strings.Repeat(" ", (64<<10)-len(smallScoreFile)))
			scoreFileWrite(t, root, prefix+"parts/drafter.md", strings.Repeat("x", 256<<10))
			if repository {
				scoreFileGit(t, root, "add", ".")
				scoreFileGit(t, root, "-c", "commit.gpgsign=false", "commit", "-qm", "fixture")
			}
			if _, err := LoadScore("small", s); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestScoreAndPartFilesThatAreFIFOsAreRefusedBeforeOpeningThem(t *testing.T) {
	for _, kind := range []string{"score", "part"} {
		for _, symlink := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s symlink=%t", kind, symlink), func(t *testing.T) {
				src := scoreFileSource(t)
				rel := "scores/small.toml"
				if kind == "part" {
					rel = "parts/drafter.md"
				}
				path := filepath.Join(src.Home, rel)
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				fifo := path
				if symlink {
					fifo = filepath.Join(src.Home, "fifo")
				}
				if err := syscall.Mkfifo(fifo, 0600); err != nil {
					t.Fatal(err)
				}
				if symlink {
					if err := os.Symlink(fifo, path); err != nil {
						t.Fatal(err)
					}
				}
				done := make(chan error, 1)
				go func() {
					_, err := LoadScore("small", src)
					done <- err
				}()
				var loadErr error
				select {
				case loadErr = <-done:
				case <-time.After(2 * time.Second):
					writer, err := os.OpenFile(fifo, os.O_RDWR|syscall.O_NONBLOCK, 0)
					if err != nil {
						t.Fatal(err)
					}
					writer.Close()
					select {
					case <-done:
					case <-time.After(5 * time.Second):
						t.Fatal("score stayed blocked after releasing the FIFO")
					}
					t.Fatal("score loading blocked opening a FIFO")
				}
				if want := path + ": named pipe is not a regular file"; loadErr == nil || loadErr.Error() != want {
					t.Fatalf("error = %v, want %s", loadErr, want)
				}
			})
		}
	}
}
