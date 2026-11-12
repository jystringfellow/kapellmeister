package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/sys/unix"
)

type reportNotSearched struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

type reportScan struct {
	withParts         bool
	maxDepth          int
	maxEntries        int
	entries           int
	copies            []reportRun
	diagnostics       []string
	visited           map[string]bool
	skipped           map[string]bool
	notSearched       []reportNotSearched
	notSearchedTotal  int
	depthSkipped      int
	entrySkipped      int
	firstEntrySkipped string
}

func findRuns(dirs []string, defaultDir bool, maxDepth, maxEntries int, refuseLinkedRoots, withParts bool) ([]reportRun, *reportScan, error) {
	scan := &reportScan{withParts: withParts, maxDepth: maxDepth, maxEntries: maxEntries, visited: map[string]bool{}, skipped: map[string]bool{}, notSearched: []reportNotSearched{}}
	roots := map[string]bool{}
	for _, dir := range dirs {
		var root string
		var err error
		if refuseLinkedRoots {
			root, err = unlinkedRoot(dir)
		} else {
			root, err = filepath.EvalSymlinks(dir)
		}
		if defaultDir && errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, scan, err
		}
		root, err = filepath.Abs(root)
		if err != nil {
			return nil, scan, err
		}
		info, err := os.Stat(root)
		if err != nil {
			return nil, scan, err
		}
		if !info.IsDir() {
			return nil, scan, fmt.Errorf("%s: not a directory", dir)
		}
		roots[root] = true
	}
	paths := make([]string, 0, len(roots))
	for root := range roots {
		paths = append(paths, root)
	}
	sort.Strings(paths)
	for _, root := range paths {
		if scan.visited[root] {
			continue
		}
		if scan.entries >= scan.maxEntries {
			scan.skip(root, "entries")
			continue
		}
		dir, err := os.OpenFile(root, os.O_RDONLY|unix.O_NOFOLLOW|unix.O_DIRECTORY, 0)
		if err != nil {
			return nil, scan, err
		}
		err = scan.directory(dir, 0)
		dir.Close()
		if err != nil {
			return nil, scan, err
		}
	}
	if scan.depthSkipped > 20 {
		scan.diagnostics = append(scan.diagnostics, fmt.Sprintf("km: report: …and %d more directories not searched", scan.depthSkipped-20))
	}
	if scan.entrySkipped > 0 {
		scan.diagnostics = append(scan.diagnostics, fmt.Sprintf("km: report: stopped after reading %d entries (-max-entries %d); %s and %d more directories were not searched", scan.entries, scan.maxEntries, plain(scan.firstEntrySkipped), scan.entrySkipped-1))
	}
	runs, messages := resolveReportCopies(scan.copies)
	scan.diagnostics = append(scan.diagnostics, messages...)
	return runs, scan, nil
}

func unlinkedRoot(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(absolute))
	if err != nil {
		return "", err
	}
	root := filepath.Join(parent, filepath.Base(absolute))
	info, err := os.Lstat(root)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", &os.PathError{Op: "open", Path: path, Err: unix.ELOOP}
	}
	return root, nil
}

func (scan *reportScan) skip(path, reason string) {
	if scan.skipped[path] {
		return
	}
	scan.skipped[path] = true
	scan.notSearchedTotal++
	if len(scan.notSearched) < 100 {
		scan.notSearched = append(scan.notSearched, reportNotSearched{Path: path, Reason: reason})
	}
	if reason == "depth" {
		scan.depthSkipped++
		if scan.depthSkipped <= 20 {
			scan.diagnostics = append(scan.diagnostics, fmt.Sprintf("km: report: %s: not searched: deeper than -max-depth %d", plain(path), scan.maxDepth))
		}
	} else {
		scan.entrySkipped++
		if scan.firstEntrySkipped == "" {
			scan.firstEntrySkipped = path
		}
	}
}

func (scan *reportScan) directory(dir *os.File, depth int) error {
	if scan.visited[dir.Name()] {
		return nil
	}
	if depth >= scan.maxDepth {
		scan.skip(dir.Name(), "depth")
		return nil
	}
	if scan.entries >= scan.maxEntries {
		scan.skip(dir.Name(), "entries")
		return nil
	}
	scan.visited[dir.Name()] = true
	var entries []os.DirEntry
	for {
		remaining := scan.maxEntries - scan.entries
		if remaining == 0 {
			scan.skip(dir.Name(), "entries")
			break
		}
		batch, err := dir.ReadDir(min(1024, remaining))
		scan.entries += len(batch)
		entries = append(entries, batch...)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	result, telemetry, current, config := false, false, false, false
	for _, entry := range entries {
		result = result || entry.Name() == "result.json"
		telemetry = telemetry || entry.Name() == "telemetry.jsonl"
		current = current || entry.Name() == "current.json"
		config = config || entry.Name() == "config.json"
	}
	if result || telemetry || current {
		run, messages := readReportRun(dir, result, telemetry, current, config)
		if scan.withParts && telemetry {
			var partMessages []string
			run.parts, partMessages = readReportParts(dir)
			messages = append(messages, partMessages...)
		}
		scan.diagnostics = append(scan.diagnostics, messages...)
		scan.copies = append(scan.copies, run)
		return nil
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		path := filepath.Join(dir.Name(), entry.Name())
		if scan.visited[path] {
			continue
		}
		if depth+1 >= scan.maxDepth {
			scan.skip(path, "depth")
			continue
		}
		if scan.entries >= scan.maxEntries {
			scan.skip(path, "entries")
			continue
		}
		child, err := openReportEntry(dir, entry.Name(), unix.O_DIRECTORY)
		if err == nil {
			err = scan.directory(child, depth+1)
			child.Close()
		}
		if err != nil {
			scan.diagnostics = append(scan.diagnostics, "km: report: "+plain(path)+": "+plain(err.Error()))
			scan.copies = append(scan.copies, reportRun{RunID: entry.Name(), Path: path, Outcome: "unreadable"})
		}
	}
	return nil
}

func reportDisagreements(a, b reportRun) []string {
	var fields []string
	if a.Started != nil && b.Started != nil && !a.roughStart && !b.roughStart && !a.Started.Equal(*b.Started) {
		fields = append(fields, "started "+a.Started.String()+" vs "+b.Started.String())
	}
	if a.Repository != "" && b.Repository != "" && a.Repository != b.Repository {
		fields = append(fields, "repository "+a.Repository+" vs "+b.Repository)
	}
	if a.quality == 2 && b.quality == 2 && a.Outcome != b.Outcome {
		fields = append(fields, "outcome "+a.Outcome+" vs "+b.Outcome)
	}
	return fields
}

func resolveReportCopies(copies []reportRun) ([]reportRun, []string) {
	sort.Slice(copies, func(i, j int) bool {
		a, b := copies[i], copies[j]
		if a.RunID != b.RunID {
			return a.RunID < b.RunID
		}
		if a.quality != b.quality {
			return a.quality > b.quality
		}
		if (a.state != nil) != (b.state != nil) {
			return a.state != nil
		}
		if a.state != nil && !a.state.Updated.Equal(b.state.Updated) {
			return a.state.Updated.After(b.state.Updated)
		}
		return a.Path < b.Path
	})
	runs := []reportRun{}
	var messages []string
	versions := map[string][]int{}
	members := map[int][]reportRun{}
	for _, copy := range copies {
		indices := versions[copy.RunID]
		matching := -1
		var previous reportRun
		for _, i := range indices {
			agrees := true
			for _, member := range members[i] {
				if len(reportDisagreements(member, copy)) > 0 {
					if previous.Path == "" {
						previous = member
					}
					agrees = false
					break
				}
			}
			if agrees {
				matching = i
				break
			}
		}
		if matching >= 0 {
			members[matching] = append(members[matching], copy)
			continue
		}
		if len(indices) > 0 {
			copy.Conflict = true
			for _, i := range indices {
				runs[i].Conflict = true
			}
			messages = append(messages, fmt.Sprintf("km: report: run %s has conflicting records: %s and %s (%s)", plain(copy.RunID), plain(previous.Path), plain(copy.Path), plain(strings.Join(reportDisagreements(previous, copy), ", "))))
		}
		index := len(runs)
		versions[copy.RunID] = append(indices, index)
		members[index] = []reportRun{copy}
		runs = append(runs, copy)
	}
	return runs, messages
}
