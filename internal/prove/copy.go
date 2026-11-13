package prove

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func git(ctx context.Context, root string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-c", "core.hooksPath=" + os.DevNull, "-C", root}, args...)...)
	for _, value := range os.Environ() {
		name, _, _ := strings.Cut(value, "=")
		switch name {
		case "GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_COMMON_DIR", "GIT_OBJECT_DIRECTORY":
		default:
			cmd.Env = append(cmd.Env, value)
		}
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", args[0], err, bounded(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

func bounded(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > 40 {
		lines = lines[len(lines)-40:]
	}
	for i, line := range lines {
		if len(line) > 300 {
			lines[i] = line[:300]
		}
	}
	return strings.Join(lines, "\n")
}

type setupFile struct {
	body   []byte
	mode   fs.FileMode
	exists bool
}

func readSetup(root string) (map[string]setupFile, error) {
	setup := map[string]setupFile{}
	for _, name := range []string{".kapellmeister/config.toml", "scripts/verify"} {
		path := filepath.Join(root, name)
		info, err := os.Lstat(path)
		if os.IsNotExist(err) {
			setup[name] = setupFile{}
			continue
		}
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("setup file %s must be a regular file", name)
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		setup[name] = setupFile{body: body, mode: info.Mode().Perm(), exists: true}
	}
	return setup, nil
}

func cleanCopy(ctx context.Context, root, head, scratch, name string, setup map[string]setupFile) (string, error) {
	dir := filepath.Join(scratch, name)
	if _, err := git(ctx, scratch, "clone", "-q", "--no-checkout", "--no-hardlinks", "--", root, dir); err != nil {
		return "", err
	}
	if _, err := git(ctx, dir, "checkout", "-q", "--detach", head); err != nil {
		return "", err
	}
	if err := writeSetup(dir, setup); err != nil {
		return "", err
	}
	return dir, nil
}

func OverlaySetup(src, dst string) error {
	setup, err := readSetup(src)
	if err != nil {
		return err
	}
	return writeSetup(dst, setup)
}

func writeSetup(dir string, setup map[string]setupFile) error {
	for name, file := range setup {
		path := filepath.Join(dir, name)
		parent := filepath.Dir(path)
		if info, err := os.Lstat(parent); err == nil && !info.IsDir() {
			return fmt.Errorf("setup parent %s must be a directory", name)
		} else if err != nil && !os.IsNotExist(err) {
			return err
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		if !file.exists {
			continue
		}
		if err := os.MkdirAll(parent, 0755); err != nil {
			return err
		}
		if err := os.WriteFile(path, file.body, file.mode); err != nil {
			return err
		}
		if err := os.Chmod(path, file.mode); err != nil {
			return err
		}
	}
	return nil
}

type fileState struct {
	Mode fs.FileMode
	Hash [32]byte
	Link string
}

func setupSnapshot(root string) (map[string]fileState, error) {
	state := map[string]fileState{}
	for _, name := range []string{".kapellmeister", "scripts/verify"} {
		err := filepath.WalkDir(filepath.Join(root, name), func(path string, entry fs.DirEntry, err error) error {
			if os.IsNotExist(err) {
				return nil
			}
			if err != nil {
				return err
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			value := fileState{Mode: info.Mode()}
			if info.Mode()&os.ModeSymlink != 0 {
				value.Link, err = os.Readlink(path)
			} else if info.Mode().IsRegular() {
				var file *os.File
				file, err = os.Open(path)
				if err == nil {
					hash := sha256.New()
					_, err = io.Copy(hash, file)
					closeErr := file.Close()
					if err == nil {
						err = closeErr
					}
					copy(value.Hash[:], hash.Sum(nil))
				}
			}
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			state[rel] = value
			if name == "scripts/verify" {
				parent, err := os.Lstat(filepath.Join(root, "scripts"))
				if err != nil {
					return err
				}
				parentState := fileState{Mode: parent.Mode()}
				if parent.Mode()&os.ModeSymlink != 0 {
					parentState.Link, err = os.Readlink(filepath.Join(root, "scripts"))
					if err != nil {
						return err
					}
				}
				state["scripts"] = parentState
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return state, nil
}
