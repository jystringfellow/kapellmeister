package loop

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
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
	r := scoreReader{src: src, engine: &Engine{Config: Config{Worktree: src.Worktree}}, tree: map[string][]string{}}
	if src.Worktree != "" {
		base, err := r.engine.gitOutput("rev-parse", "HEAD")
		if err != nil {
			return LoadedScore{}, err
		}
		entries, err := r.engine.gitOutput("ls-tree", "-r", "-z", strings.TrimSpace(base), "--", ".kapellmeister/scores", ".kapellmeister/parts")
		if err != nil {
			return LoadedScore{}, err
		}
		for _, entry := range strings.Split(entries, "\x00") {
			meta, path, ok := strings.Cut(entry, "\t")
			if f := strings.Fields(meta); ok && len(f) == 3 {
				r.tree[path] = f
			}
		}
	}
	if err := r.reserved("score", "default"); err != nil {
		return LoadedScore{}, err
	}
	if name == "default" {
		return LoadedScore{Score: DefaultScore()}, nil
	}
	for _, part := range AllRoles() {
		if err := r.reserved("part", part); err != nil {
			return LoadedScore{}, err
		}
	}
	text, file, err := r.read("score", name)
	if err != nil {
		return LoadedScore{}, err
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
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		file.Path, file.Source = ".kapellmeister/"+rel, "repository"
		fields, ok := r.tree[file.Path]
		if !ok {
			return "", file, fmt.Errorf("unknown %s %q (looked in %s and %s at the base commit)", kind, name, path, file.Path)
		}
		if !r.src.AllowRepository {
			return "", file, fmt.Errorf("repository %s %q (%s) refused: [run] repository_scores is false", kind, name, file.Path)
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
