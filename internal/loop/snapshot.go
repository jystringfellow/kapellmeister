package loop

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

type workSnapshot struct {
	Dir         string
	Tree        string
	Head        string
	State       string
	IndexPath   string
	Index       []byte
	IndexExists bool
}

func (e *Engine) snapshotGit(index string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", e.Config.Worktree}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_INDEX_FILE="+index, "GIT_OBJECT_DIRECTORY="+filepath.Join(e.snapshotDir, "objects"), "GIT_ALTERNATE_OBJECT_DIRECTORIES="+strconv.Quote(e.snapshotObjects), "GIT_OPTIONAL_LOCKS=0")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, &stderr)
	}
	return string(out), nil
}

func (e *Engine) snapshotTree(index []byte, exists bool) (string, error) {
	path := filepath.Join(e.snapshotDir, "work-index")
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if exists {
		if err := os.WriteFile(path, index, 0600); err != nil {
			return "", err
		}
	} else if _, err := e.snapshotGit(path, "read-tree", "HEAD"); err != nil {
		return "", err
	}
	if _, err := e.snapshotGit(path, "add", "--all"); err != nil {
		return "", err
	}
	tree, err := e.snapshotGit(path, "write-tree")
	return strings.TrimSpace(tree), err
}

func (e *Engine) takeSnapshot() (*workSnapshot, error) {
	if e.keepSnapshot {
		return nil, fmt.Errorf("previous restore could not be confirmed; saved work remains in %s", e.snapshotDir)
	}
	if e.snapshotDir == "" {
		objects, err := e.gitOutput("rev-parse", "--path-format=absolute", "--git-path", "objects")
		if err != nil {
			return nil, err
		}
		e.snapshotObjects = strings.TrimSpace(objects)
		dir, err := os.MkdirTemp("", "km-snapshot-")
		if err != nil {
			return nil, err
		}
		e.snapshotDir = dir
		dir, err = filepath.EvalSymlinks(dir)
		if err != nil {
			return nil, err
		}
		dir, err = filepath.Abs(dir)
		if err != nil {
			return nil, err
		}
		e.snapshotDir = dir
		for _, root := range []string{e.Config.Worktree, e.Config.ScratchDir} {
			if root == "" {
				continue
			}
			root, err = filepath.EvalSymlinks(root)
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				return nil, err
			}
			root, err = filepath.Abs(root)
			if err != nil {
				return nil, err
			}
			rel, err := filepath.Rel(root, dir)
			if err != nil {
				return nil, err
			}
			if rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				return nil, fmt.Errorf("snapshot must be outside the worktree and tester scratch; choose another temporary directory")
			}
		}
		if err := os.Mkdir(filepath.Join(dir, "objects"), 0700); err != nil {
			return nil, err
		}
	}
	s := &workSnapshot{Dir: e.snapshotDir}
	var err error
	s.State, err = e.treeState()
	if err != nil {
		return nil, err
	}
	s.Head, err = e.gitOutput("rev-parse", "HEAD")
	if err != nil {
		return nil, err
	}
	index, err := e.gitOutput("rev-parse", "--path-format=absolute", "--git-path", "index")
	if err != nil {
		return nil, err
	}
	s.IndexPath = strings.TrimSpace(index)
	s.Index, err = os.ReadFile(s.IndexPath)
	s.IndexExists = err == nil
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	s.Tree, err = e.snapshotTree(s.Index, s.IndexExists)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(s.Dir, "saved-index"), s.Index, 0600); err != nil {
		return nil, err
	}
	data, err := json.Marshal(struct {
		Directory, Tree, Head, State string
		IndexExists                  bool
	}{s.Dir, s.Tree, strings.TrimSpace(s.Head), s.State, s.IndexExists})
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(s.Dir, "snapshot.json"), data, 0600); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(e.Config.Worktree, e.rel("snapshot.json")), data, 0600); err != nil {
		return nil, err
	}
	return s, nil
}

func (e *Engine) currentSnapshotTree(s *workSnapshot) (string, error) {
	index, err := os.ReadFile(s.IndexPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	return e.snapshotTree(index, err == nil)
}

func (e *Engine) snapshotPaths(s *workSnapshot, tree string) ([]string, int, error) {
	out, err := e.snapshotGit(filepath.Join(s.Dir, "work-index"), "diff-tree", "-r", "--name-only", "--no-renames", "-z", s.Tree, tree)
	if err != nil {
		return nil, 0, err
	}
	paths := strings.Split(strings.TrimSuffix(out, "\x00"), "\x00")
	if out == "" {
		paths = []string{"(index only)"}
	}
	more := max(0, len(paths)-10)
	paths = paths[:min(10, len(paths))]
	for i, p := range paths {
		quoted := strconv.Quote(p)
		paths[i] = quoted[1 : len(quoted)-1]
	}
	return paths, more, nil
}

func removeSnapshotPath(root, rel string) error {
	parts := strings.Split(filepath.FromSlash(rel), string(filepath.Separator))
	for i := 1; i < len(parts); i++ {
		parent := filepath.Join(append([]string{root}, parts[:i]...)...)
		info, err := os.Lstat(parent)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if !info.IsDir() {
			return os.Remove(parent)
		}
	}
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for parent := filepath.Dir(path); parent != root; parent = filepath.Dir(parent) {
		if os.Remove(parent) != nil {
			break
		}
	}
	return nil
}

func removeEmptySnapshotDirs(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return os.Remove(path)
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			return fmt.Errorf("ignored file obstructs restore: %s", filepath.Join(path, entry.Name()))
		}
		if err := removeEmptySnapshotDirs(filepath.Join(path, entry.Name())); err != nil {
			return err
		}
	}
	return os.Remove(path)
}

func (e *Engine) restoreSnapshot(s *workSnapshot) (err error) {
	e.keepSnapshot = true
	defer func() {
		if err != nil {
			err = fmt.Errorf("restore could not be confirmed: %w; saved work tree %s is in %s", err, s.Tree, s.Dir)
		} else {
			e.keepSnapshot = false
		}
	}()
	index := filepath.Join(s.Dir, "restore-index")
	if _, err = e.snapshotGit(index, "read-tree", s.Tree); err != nil {
		return err
	}
	extras, outErr := e.snapshotGit(index, "ls-files", "-z", "--others", "--exclude-standard")
	if outErr != nil {
		return outErr
	}
	paths := strings.Split(strings.TrimSuffix(extras, "\x00"), "\x00")
	sort.Sort(sort.Reverse(sort.StringSlice(paths)))
	for _, path := range paths {
		if path != "" {
			if err = removeSnapshotPath(e.Config.Worktree, path); err != nil {
				return err
			}
		}
	}
	files, outErr := e.snapshotGit(index, "ls-files", "--stage", "-z")
	if outErr != nil {
		return outErr
	}
	for _, entry := range strings.Split(strings.TrimSuffix(files, "\x00"), "\x00") {
		if entry == "" || strings.HasPrefix(entry, "160000 ") {
			continue
		}
		_, rel, ok := strings.Cut(entry, "\t")
		if !ok {
			return fmt.Errorf("invalid snapshot entry %q", entry)
		}
		parts := strings.Split(rel, "/")
		for i := 1; i < len(parts); i++ {
			parent := filepath.Join(e.Config.Worktree, filepath.Join(parts[:i]...))
			info, lerr := os.Lstat(parent)
			if lerr == nil && !info.IsDir() {
				if err = os.Remove(parent); err != nil {
					return err
				}
			}
			if lerr != nil && !errors.Is(lerr, os.ErrNotExist) {
				return lerr
			}
		}
		path := filepath.Join(e.Config.Worktree, filepath.FromSlash(rel))
		info, lerr := os.Lstat(path)
		if lerr == nil && info.IsDir() {
			if err = removeEmptySnapshotDirs(path); err != nil {
				return err
			}
		}
		if lerr != nil && !errors.Is(lerr, os.ErrNotExist) {
			return lerr
		}
	}
	if _, err = e.snapshotGit(index, "checkout-index", "--all", "--force"); err != nil {
		return err
	}
	if s.IndexExists {
		var file *os.File
		file, err = os.CreateTemp(filepath.Dir(s.IndexPath), "km-index-")
		if err != nil {
			return err
		}
		defer os.Remove(file.Name())
		_, err = file.Write(s.Index)
		closeErr := file.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		if err = os.Rename(file.Name(), s.IndexPath); err != nil {
			return err
		}
	} else if err = os.Remove(s.IndexPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	state, stateErr := e.treeState()
	if stateErr != nil {
		return stateErr
	}
	tree, treeErr := e.currentSnapshotTree(s)
	if treeErr != nil {
		return treeErr
	}
	if state != s.State || tree != s.Tree {
		return fmt.Errorf("restored state differs from snapshot")
	}
	return nil
}

func (e *Engine) cleanupSnapshot() {
	if e.snapshotDir != "" && !e.keepSnapshot {
		os.RemoveAll(e.snapshotDir)
		os.Remove(filepath.Join(e.Config.Worktree, e.rel("snapshot.json")))
		e.snapshotDir = ""
	}
}
