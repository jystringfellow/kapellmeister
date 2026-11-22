package loop

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

type ScoreSource struct {
	Home, Worktree  string
	AllowRepository bool
}
type ScoreFile struct {
	Kind   string `json:"kind"`
	Name   string `json:"name"`
	Source string `json:"source"`
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}
type LoadedScore struct {
	Score Score
	Files []ScoreFile
}
type scoreReader struct {
	src    ScoreSource
	engine *Engine
	tree   map[string][]string
}

var scoreName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

func LoadScore(name string, src ScoreSource) (LoadedScore, error) {
	if name == "" {
		return LoadedScore{Score: DefaultScore()}, nil
	}
	if !scoreName.MatchString(name) {
		return LoadedScore{}, fmt.Errorf("invalid score name %q", name)
	}
	r, err := newScoreReader(src)
	if err != nil {
		return LoadedScore{}, err
	}
	return r.load(name)
}
func newScoreReader(src ScoreSource) (scoreReader, error) {
	r := scoreReader{src: src, engine: &Engine{Config: Config{Worktree: src.Worktree}}, tree: map[string][]string{}}
	if src.Worktree != "" {
		base, err := r.engine.gitOutput("rev-parse", "HEAD")
		if err != nil {
			return r, err
		}
		entries, err := r.engine.gitOutput("ls-tree", "-r", "-z", strings.TrimSpace(base), "--", ".kapellmeister/scores", ".kapellmeister/parts")
		if err != nil {
			return r, err
		}
		for _, entry := range strings.Split(entries, "\x00") {
			meta, path, ok := strings.Cut(entry, "\t")
			if f := strings.Fields(meta); ok && len(f) == 3 {
				r.tree[path] = f
			}
		}
	}
	return r, nil
}
func (r scoreReader) load(name string) (LoadedScore, error) {
	for _, builtin := range append([]string{"default"}, BuiltinScoreNames()...) {
		if err := r.reserved("score", builtin); err != nil {
			return LoadedScore{}, err
		}
	}
	if name == "default" {
		return LoadedScore{Score: DefaultScore()}, nil
	}
	for _, part := range AllRoles() {
		if err := r.reserved("part", part); err != nil {
			return LoadedScore{}, err
		}
	}
	if score, ok := BuiltinScore(name); ok {
		return LoadedScore{Score: score}, nil
	}
	text, file, err := r.read("score", name)
	if err != nil {
		return LoadedScore{}, err
	}
	if message, ok := ReservedNameMessage(name); ok {
		return LoadedScore{}, fmt.Errorf("%s: score %q: %s", file.Path, name, message)
	}
	score, err := decodeScore(text, file.Path, name)
	if err != nil {
		return LoadedScore{}, err
	}
	score.Source = file.Source
	loaded := LoadedScore{Score: score, Files: []ScoreFile{file}}
	for i := range loaded.Score.Parts {
		part := &loaded.Score.Parts[i]
		if built, ok := DefaultScore().Part(part.Name); ok {
			part.Prompt = built.Prompt
			continue
		}
		text, file, err := r.read("part", part.Name)
		if err != nil {
			if !strings.HasPrefix(err.Error(), "unknown ") {
				return LoadedScore{}, err
			}
			steps := loaded.Score.StepsOf(part.Name)
			return LoadedScore{}, fmt.Errorf("%s: step %s: %w", loaded.Files[0].Path, steps[0].Name, err)
		}
		part.Prompt, err = decodePart(text, file.Path, loaded.Score.StepsOf(part.Name))
		if err != nil {
			return LoadedScore{}, err
		}
		loaded.Files = append(loaded.Files, file)
	}
	return loaded, nil
}
func scorePath(kind, name string) string {
	if kind == "score" {
		return "scores/" + name + ".toml"
	}
	return "parts/" + name + ".md"
}
func (r scoreReader) reserved(kind, name string) error {
	rel := scorePath(kind, name)
	path := filepath.Join(r.src.Home, rel)
	if _, err := os.Lstat(path); err == nil {
		return fmt.Errorf("%s: built-in name is reserved", path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	path = ".kapellmeister/" + rel
	if _, ok := r.tree[path]; ok {
		return fmt.Errorf("%s at the base commit: built-in name is reserved", path)
	}
	return nil
}
func (r scoreReader) read(kind, name string) (string, ScoreFile, error) {
	rel := scorePath(kind, name)
	path := filepath.Join(r.src.Home, rel)
	file := ScoreFile{Kind: kind, Name: name, Source: "user", Path: path}
	limit := int64(64 << 10)
	if kind == "part" {
		limit = 256 << 10
	}
	tooLarge := func(path string, size int64) error {
		return fmt.Errorf("%s: %s file is %d bytes, over the %d-byte limit", path, kind, size, limit)
	}
	if info, err := os.Stat(path); err == nil && info.Mode()&os.ModeNamedPipe != 0 {
		return "", file, fmt.Errorf("%s: named pipe is not a regular file", path)
	}
	var data []byte
	input, err := os.Open(path)
	if err == nil {
		defer input.Close()
		info, statErr := input.Stat()
		if statErr != nil {
			return "", file, fmt.Errorf("%s: %w", path, statErr)
		}
		if info.Size() > limit {
			return "", file, tooLarge(path, info.Size())
		}
		data, err = io.ReadAll(io.LimitReader(input, limit+1))
		if int64(len(data)) > limit {
			return "", file, tooLarge(path, int64(len(data)))
		}
	}
	if errors.Is(err, os.ErrNotExist) {
		file.Path, file.Source = ".kapellmeister/"+rel, "repository"
		fields, ok := r.tree[file.Path]
		if !ok {
			return "", file, fmt.Errorf("unknown %s %q (looked in %s and %s at the base commit)", kind, name, path, file.Path)
		}
		if !r.src.AllowRepository {
			return "", file, fmt.Errorf("repository %s %q (%s) refused: [run] repository_scores is false", kind, name, file.Path)
		}
		sizeText, sizeErr := r.engine.gitOutput("cat-file", "-s", fields[2])
		if sizeErr != nil {
			return "", file, fmt.Errorf("%s: %w", file.Path, sizeErr)
		}
		size, sizeErr := strconv.ParseInt(strings.TrimSpace(sizeText), 10, 64)
		if sizeErr != nil {
			return "", file, fmt.Errorf("%s: %w", file.Path, sizeErr)
		}
		if size > limit {
			return "", file, tooLarge(file.Path, size)
		}
		blob := r.engine.readTextBlob(fields[0], fields[2])
		if blob.reason != "" {
			return "", file, fmt.Errorf("%s at the base commit: %s", file.Path, blob.reason)
		}
		data, err = []byte(blob.text), nil
	}
	if err != nil {
		return "", file, fmt.Errorf("%s: %w", path, err)
	}
	file.SHA256 = fmt.Sprintf("%x", sha256.Sum256(data))
	return string(data), file, nil
}
func ScoreFileState(path string) string { return scriptState(path) }

type ScoreEntry struct {
	Name, Source string
	Score        Score
	Err          error
}

func ListScores(src ScoreSource) ([]ScoreEntry, error) {
	r, note := newScoreReader(src)
	entries := []ScoreEntry{{Name: "default", Source: "built-in", Score: DefaultScore()}}
	for _, name := range BuiltinScoreNames() {
		score, _ := BuiltinScore(name)
		entries = append(entries, ScoreEntry{Name: name, Source: "built-in", Score: score})
	}
	names := map[string]string{}
	for path := range r.tree {
		const prefix = ".kapellmeister/scores/"
		if strings.HasPrefix(path, prefix) {
			name := strings.TrimSuffix(strings.TrimPrefix(path, prefix), ".toml")
			if scoreName.MatchString(name) && path == prefix+name+".toml" {
				names[name] = "repository"
			}
		}
	}
	paths, _ := filepath.Glob(filepath.Join(src.Home, "scores", "*.toml"))
	for _, path := range paths {
		name := strings.TrimSuffix(filepath.Base(path), ".toml")
		if scoreName.MatchString(name) {
			names[name] = "user"
		}
	}
	for name, source := range names {
		loaded, err := r.load(name)
		entries = append(entries, ScoreEntry{Name: name, Source: source, Score: loaded.Score, Err: err})
	}
	return entries, note
}
