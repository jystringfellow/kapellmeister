package loop

import (
	"fmt"
	"os"
	"path/filepath"
)

const DefaultMinFreeMB = 1024

const scratchPids = "pids"

var FreeBytes = freeBytes

func LowDisk(minFreeMB int, dirs ...string) error {
	if minFreeMB <= 0 {
		return nil
	}
	for _, dir := range dirs {
		if dir == "" {
			continue
		}
		dir = existingDir(dir)
		free, err := FreeBytes(dir)
		if err != nil {
			continue
		}
		if freeMB := free >> 20; freeMB < uint64(minFreeMB) {
			return fmt.Errorf("low disk: %s has %d MB free and this run needs %d MB ([run] min_free_mb); free some space or lower the setting", dir, freeMB, minFreeMB)
		}
	}
	return nil
}

func existingDir(dir string) string {
	for {
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return dir
		}
		dir = parent
	}
}

func RemoveScratch(dir string) error {
	if dir == "" {
		return nil
	}
	_ = filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err == nil && entry.IsDir() {
			_ = os.Chmod(path, 0700)
		}
		return nil
	})
	return os.RemoveAll(dir)
}

func (e *Engine) clearScratch() {
	entries, err := os.ReadDir(e.Config.ScratchDir)
	if e.Config.ScratchDir == "" || err != nil {
		return
	}
	for _, entry := range entries {
		if entry.Name() == scratchPids {
			continue
		}
		if err := RemoveScratch(filepath.Join(e.Config.ScratchDir, entry.Name())); err != nil {
			e.progress("  warning: scratch: %v", err)
		}
	}
}

func (e *Engine) lowDisk() error {
	return LowDisk(e.Config.MinFreeMB, e.Config.Worktree, e.Config.StateDir, e.Config.ScratchDir)
}
