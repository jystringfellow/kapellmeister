package loop

import (
	"crypto/sha1"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

var ErrNativeDefinition = errors.New("native agent definition")

const nativeSizeLimit = 256 * 1024

type nativeEntry struct {
	mode, oid string
	size      int64
}
type NativeDifference struct{ Path, Reason string }
type NativeManifest struct {
	Entries []NativeManifestEntry `json:"entries"`
}
type NativeManifestEntry struct {
	Path   []byte `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

func nativeHash(data []byte) string { return fmt.Sprintf("%x", sha256.Sum256(data)) }
func (m *NativeManifest) Pinned() bool {
	if m == nil {
		return false
	}
	for _, e := range m.Entries {
		if e.Size == 0 && e.SHA256 != nativeHash(nil) {
			return false
		}
	}
	return true
}
func (c Chair) WithDefinitionOf(o Chair) Chair {
	c.Definition, c.DefinitionBase, c.Manifest = o.Definition, o.DefinitionBase, o.Manifest
	return c
}
func nativePaths(paths []string) string {
	quoted := make([]string, len(paths))
	for i, p := range paths {
		quoted[i] = nativePath(p)
	}
	return strings.Join(quoted, ", ")
}

func nativeTree(e *Engine, base string) (map[string]nativeEntry, error) {
	out, err := e.gitOutput("--no-replace-objects", "ls-tree", "-r", "-l", "-z", base, "--", ".claude/agents")
	if err != nil {
		return nil, err
	}
	entries := map[string]nativeEntry{}
	for _, row := range strings.Split(out, "\x00") {
		if row == "" {
			continue
		}
		meta, path, ok := strings.Cut(row, "\t")
		f := strings.Fields(meta)
		if !ok || len(f) != 4 {
			return nil, fmt.Errorf("invalid git tree entry")
		}
		size := int64(0)
		if f[3] != "-" {
			size, err = strconv.ParseInt(f[3], 10, 64)
			if err != nil {
				return nil, fmt.Errorf("invalid git tree entry size")
			}
		}
		entries[path] = nativeEntry{f[0], f[2], size}
	}
	return entries, nil
}
func nativeBlob(e *Engine, oid string) ([]byte, error) {
	s, err := e.gitOutput("--no-replace-objects", "cat-file", "blob", oid)
	return []byte(s), err
}
func nativeBlobMatches(data []byte, oid string) bool {
	h := sha1.New()
	if len(oid) == sha256.Size*2 {
		h = sha256.New()
	}
	fmt.Fprintf(h, "blob %d%c", len(data), 0)
	h.Write(data)
	return fmt.Sprintf("%x", h.Sum(nil)) == oid
}
func nativePath(path string) string {
	if !utf8.ValidString(path) || strings.IndexFunc(path, func(r rune) bool { return !unicode.IsPrint(r) }) >= 0 {
		return fmt.Sprintf("%q", path)
	}
	return path
}
func NativeTreeDiff(worktree string, manifest *NativeManifest) ([]NativeDifference, error) {
	entries := map[string]NativeManifestEntry{}
	for _, entry := range manifest.Entries {
		entries[string(entry.Path)] = entry
	}
	var err error
	seen := map[string]bool{}
	diff := map[string]string{}
	for _, ancestor := range []string{".claude", ".claude/agents"} {
		info, err := os.Lstat(filepath.Join(worktree, ancestor))
		if errors.Is(err, os.ErrNotExist) {
			break
		}
		if err != nil {
			diff[ancestor] = "unreadable"
			break
		}
		if !info.IsDir() {
			diff[ancestor] = "not a regular directory"
			break
		}
	}
	root := filepath.Join(worktree, ".claude/agents")
	if len(diff) == 0 {
		err = filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
			rel, _ := filepath.Rel(worktree, path)
			rel = filepath.ToSlash(rel)
			if walkErr != nil {
				if errors.Is(walkErr, os.ErrNotExist) && path == root {
					return nil
				}
				diff[rel] = "unreadable"
				return nil
			}
			if d.IsDir() {
				return nil
			}
			seen[rel] = true
			info, er := d.Info()
			if er != nil {
				diff[rel] = "unreadable"
				return nil
			}
			if !info.Mode().IsRegular() {
				diff[rel] = "not a regular file"
				return nil
			}
			entry, ok := entries[rel]
			if !ok {
				diff[rel] = "is not in the base commit"
				return nil
			}
			if info.Size() != entry.Size {
				diff[rel] = "differs from the base commit"
				return nil
			}
			data, er := os.ReadFile(path)
			if er != nil {
				diff[rel] = "unreadable"
				return nil
			}
			if nativeHash(data) != entry.SHA256 {
				diff[rel] = "differs from the base commit"
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	for path := range entries {
		if !seen[path] {
			diff[path] = "missing"
		}
	}
	paths := make([]string, 0, len(diff))
	for path := range diff {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	result := make([]NativeDifference, 0, len(paths))
	for _, p := range paths {
		result = append(result, NativeDifference{p, diff[p]})
	}
	return result, nil
}
func (c Chair) CheckDefinition(worktree string) error {
	if c.NativeAgent == "" {
		return nil
	}
	if (c.Definition != "repository" && c.Definition != "user") || c.DefinitionBase == "" {
		return fmt.Errorf("%w: definition not bound; km did not launch the chair", ErrNativeDefinition)
	}
	if c.Manifest == nil {
		return fmt.Errorf("%w: definition manifest not pinned; rehearse again; km did not launch the chair", ErrNativeDefinition)
	}
	diffs, err := NativeTreeDiff(worktree, c.Manifest)
	if err != nil {
		return fmt.Errorf("%w: cannot compare base commit: %v", ErrNativeDefinition, err)
	}
	if len(diffs) > 0 {
		var parts []string
		for i, d := range diffs {
			if i == 5 {
				parts = append(parts, fmt.Sprintf("and %d more", len(diffs)-i))
				break
			}
			parts = append(parts, fmt.Sprintf("%s (%s)", nativePath(d.Path), d.Reason))
		}
		return fmt.Errorf("%w: .claude/agents/ does not match the base commit: %s; km did not launch the chair", ErrNativeDefinition, strings.Join(parts, ", "))
	}
	if err := os.MkdirAll(filepath.Join(worktree, ".claude/agents"), 0755); err != nil {
		return fmt.Errorf("%w: cannot create .claude/agents: %v", ErrNativeDefinition, err)
	}
	return nil
}
func ResolveNativeDefinition(worktree string, chair Chair, portable bool) (Chair, error) {
	if chair.NativeAgent == "" {
		return chair, nil
	}
	e := Engine{Config: Config{Worktree: worktree}}
	base, err := e.gitOutput("--no-replace-objects", "rev-parse", "HEAD")
	if err != nil {
		return chair, err
	}
	return ResolveNativeDefinitionAt(worktree, strings.TrimSpace(base), chair, portable)
}
func ResolveNativeDefinitionAt(worktree, base string, chair Chair, portable bool) (Chair, error) {
	if chair.NativeAgent == "" {
		return chair, nil
	}
	e := Engine{Config: Config{Worktree: worktree}}
	root, err := e.gitOutput("--no-replace-objects", "rev-parse", "--show-toplevel")
	if err != nil {
		return chair, err
	}
	rootPath, err := filepath.EvalSymlinks(strings.TrimSpace(root))
	if err != nil {
		return chair, err
	}
	worktreePath, err := filepath.EvalSymlinks(worktree)
	if err != nil {
		return chair, err
	}
	worktreePath, err = filepath.Abs(worktreePath)
	if err != nil {
		return chair, err
	}
	if rootPath != worktreePath {
		return chair, fmt.Errorf("native chairs require the worktree to be the repository root (%s)", nativePath(rootPath))
	}
	oid, err := e.gitOutput("--no-replace-objects", "rev-parse", base+"^{commit}")
	if err != nil {
		return chair, err
	}
	base = strings.TrimSpace(oid)
	path := ".claude/agents/" + chair.NativeAgent + ".md"
	ancestors, err := e.gitOutput("--no-replace-objects", "ls-tree", "-z", base, "--", ".claude", ".claude/agents")
	if err != nil {
		return chair, err
	}
	for _, row := range strings.Split(ancestors, "\x00") {
		if row == "" {
			continue
		}
		meta, p, _ := strings.Cut(row, "\t")
		f := strings.Fields(meta)
		if (p == ".claude" || p == ".claude/agents") && len(f) >= 2 && f[1] != "tree" {
			return chair, fmt.Errorf("%s: not a regular directory", p)
		}
	}
	entries, err := nativeTree(&e, base)
	if err != nil {
		return chair, err
	}
	var matches []string
	manifest := &NativeManifest{Entries: []NativeManifestEntry{}}
	paths := make([]string, 0, len(entries))
	for p := range entries {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		entry := entries[p]
		if entry.mode != "100644" && entry.mode != "100755" {
			return chair, fmt.Errorf("%s: not a regular file", nativePath(p))
		}
		if entry.size > nativeSizeLimit {
			return chair, fmt.Errorf("%s at %s: exceeds 256 KiB", nativePath(p), base)
		}
		data, err := nativeBlob(&e, entry.oid)
		if err != nil {
			return chair, err
		}
		if !nativeBlobMatches(data, entry.oid) {
			return chair, fmt.Errorf("%s at %s: content does not match its object ID", nativePath(p), base)
		}
		manifest.Entries = append(manifest.Entries, NativeManifestEntry{Path: []byte(p), Size: int64(len(data)), SHA256: nativeHash(data)})
		if p == path || !strings.HasSuffix(p, ".md") {
			continue
		}
		name, err := nativeName(p, data)
		if err != nil {
			return chair, err
		}
		if name == chair.NativeAgent {
			matches = append(matches, p)
		}
	}
	sort.Strings(matches)
	check := func(p string, data []byte) error {
		name, err := nativeName(p, data)
		if err != nil {
			return err
		}
		if name == "" {
			return fmt.Errorf("%s declares no name, not %q; Claude Code finds an agent by its front-matter name", nativePath(p), chair.NativeAgent)
		}
		if name != chair.NativeAgent {
			return fmt.Errorf("%s declares name %q, not %q; Claude Code finds an agent by its front-matter name", nativePath(p), name, chair.NativeAgent)
		}
		return nil
	}
	if entry, ok := entries[path]; ok {
		blob := e.readTextBlobWithGitOptions(entry.mode, entry.oid, []string{"--no-replace-objects"})
		if blob.reason != "" {
			reason := blob.reason
			if blob.fault != faultNone {
				reason = diagnoseText(blob.raw)
			}
			return chair, fmt.Errorf("%s at %s: %s", path, base, reason)
		}
		if err := check(path, []byte(blob.text)); err != nil {
			return chair, err
		}
		if len(matches) > 0 {
			matches = append(matches, path)
			sort.Strings(matches)
			return chair, fmt.Errorf("ambiguous native agent %q: %s", chair.NativeAgent, nativePaths(matches))
		}
		chair.Definition = "repository"
	} else {
		if portable {
			return chair, fmt.Errorf("%s does not exist at base commit %s", path, base)
		}
		config := os.Getenv("CLAUDE_CONFIG_DIR")
		if config == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return chair, err
			}
			config = filepath.Join(home, ".claude")
		}
		userPath := filepath.Join(config, "agents", chair.NativeAgent+".md")
		info, err := os.Stat(userPath)
		if err != nil || !info.Mode().IsRegular() {
			return chair, fmt.Errorf("native agent definition missing at %s in base commit %s and %s", nativePath(path), base, nativePath(userPath))
		}
		if info.Size() > nativeSizeLimit {
			return chair, fmt.Errorf("%s: exceeds 256 KiB", nativePath(userPath))
		}
		data, err := os.ReadFile(userPath)
		if err != nil {
			return chair, fmt.Errorf("native agent definition missing at %s in base commit %s and %s", nativePath(path), base, nativePath(userPath))
		}
		if err := check(userPath, data); err != nil {
			return chair, err
		}
		if len(matches) > 0 {
			verb := "declare"
			if len(matches) == 1 {
				verb = "declares"
			}
			return chair, fmt.Errorf("%s %s name %q and would replace the user definition %s (project agents take precedence)", nativePaths(matches), verb, chair.NativeAgent, nativePath(userPath))
		}
		chair.Definition = "user"
	}
	chair.Manifest = manifest
	chair.DefinitionBase = base
	return chair, nil
}
