package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/jystringfellow/kapellmeister/internal/ensemble"
	"github.com/jystringfellow/kapellmeister/internal/loop"
	"golang.org/x/sys/unix"
)

type checkResult struct {
	Borrowed []borrowedPart `json:"borrowed,omitempty"`
	Schema   int            `json:"schema"`
	Kind     string         `json:"kind"`
	Name     string         `json:"name"`
	OK       bool           `json:"ok"`
	Ready    *bool          `json:"ready,omitempty"`
	Reason   string         `json:"reason"`
	Errors   []string       `json:"errors"`
}

func checkCommand(kind string, args []string, in io.Reader, out, stderr io.Writer, labelOverride string) error {
	fs := flag.NewFlagSet(kind+" check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	worktree := fs.String("worktree", ".", "repository directory")
	name := fs.String("name", "", "file name")
	held := false
	if kind == "ensemble" {
		fs.BoolVar(&held, "text", false, "check text ensemble agent names")
	}
	asJSON := fs.Bool("json", false, "print JSON")
	if err := fs.Parse(args); err != nil {
		return &exitStatusError{code: 2, err: err}
	}
	if fs.NArg() != 1 {
		usage := "usage: km " + kind + " check [-worktree DIR] [-name NAME] [-json]"
		if kind == "ensemble" {
			usage += " [-text]"
		}
		return &exitStatusError{code: 2, err: fmt.Errorf("%s FILE", usage)}
	}
	path := fs.Arg(0)
	if path == "-" && *name == "" {
		return &exitStatusError{code: 2, err: fmt.Errorf("-name is required when reading standard input")}
	}
	if *name == "" {
		*name = strings.TrimSuffix(filepath.Base(path), ".toml")
	}
	label := path
	if path == "-" {
		label = "<stdin>"
	}
	if labelOverride != "" {
		label = labelOverride
	}
	result := checkResult{Schema: 1, Kind: kind, Name: *name, Errors: []string{}}
	if kind == "ensemble" {
		ready := false
		result.Ready = &ready
	}
	data, err := readTextFile(kind, label, path, in)
	if err == nil {
		err = checkContent(&result, label, string(data), *worktree, stderr, held)
	}
	if err != nil {
		result.Errors = append(result.Errors, err.Error())
	} else {
		result.OK = true
	}
	if *asJSON {
		if e := json.NewEncoder(out).Encode(result); e != nil {
			return e
		}
	} else if !result.OK {
		for _, e := range result.Errors {
			fmt.Fprintln(out, plain(e))
		}
	} else {
		for _, b := range result.Borrowed {
			fmt.Fprintf(out, "part %s  from %s", plain(b.Part), plain(b.From))
			if b.FrontMatter != "" {
				fmt.Fprintf(out, "  %s", b.FrontMatter)
			}
			fmt.Fprintln(out)
		}
		if result.Reason != "" {
			fmt.Fprintf(out, "ok, not ready: %s\n", plain(result.Reason))
		} else {
			fmt.Fprintln(out, "ok")
		}
	}
	if !result.OK {
		return &exitStatusError{code: 1}
	}
	return nil
}

func checkContent(result *checkResult, label, content, worktree string, stderr io.Writer, held bool) error {
	home := loop.Home()
	settings, err := loop.LoadSettings(home, worktree)
	if err != nil {
		return err
	}
	source := loop.ScoreSource{Home: home, Worktree: worktree, AllowRepository: settings.RepositoryScores}
	note := func(err error) {
		if err != nil {
			fmt.Fprintf(stderr, "note: repository scores not read: %v\n", err)
		}
	}
	if result.Kind == "score" {
		loaded, n, err := loop.CheckScore(result.Name, label, content, source)
		for _, f := range loaded.Files {
			if f.From != "" {
				result.Borrowed = append(result.Borrowed, borrowedPart{Part: f.Name, From: f.From, FrontMatter: f.FrontMatter})
			}
		}
		note(n)
		if err != nil && !strings.HasPrefix(err.Error(), label+": ") {
			return fmt.Errorf("%s: %w", label, err)
		}
		return err
	}
	_, reason, err := checkEnsemble(result.Name, label, content, worktree, stderr, held)
	if err != nil {
		return err
	}
	result.Reason = reason
	*result.Ready = reason == ""
	return nil
}

func checkEnsemble(name, label, content, worktree string, stderr io.Writer, held bool) (ensemble.Spec, string, error) {
	home := loop.Home()
	settings, err := loop.LoadSettings(home, worktree)
	if err != nil {
		return ensemble.Spec{}, "", err
	}
	source := loop.ScoreSource{Home: home, Worktree: worktree, AllowRepository: settings.RepositoryScores}
	note := func(err error) {
		if err != nil {
			fmt.Fprintf(stderr, "note: repository scores not read: %v\n", err)
		}
	}
	var spec ensemble.Spec
	if held {
		spec, err = ensemble.CheckText(name, label, content, settings.Defined, settings.Agents)
	} else {
		spec, err = ensemble.Check(name, label, content, settings.Defined)
	}
	if err != nil {
		return spec, "", err
	}
	score := loop.DefaultScore()
	if spec.Score != "" {
		loaded, n, e := loop.LoadScoreLenient(spec.Score, source)
		note(n)
		if e != nil {
			return spec, "", fmt.Errorf("%s: %w", label, e)
		}
		score = loaded.Score
	}
	if err := spec.Fits(score); err != nil {
		return spec, "", err
	}
	return spec, repertoireReadiness(spec, score, settings), nil
}

func readTextFile(kind, label, path string, in io.Reader) ([]byte, error) {
	if path != "-" || in == nil {
		fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", label, err)
		}
		f := os.NewFile(uintptr(fd), path)
		defer f.Close()
		info, err := f.Stat()
		if err != nil {
			return nil, fmt.Errorf("%s: %w", label, err)
		}
		if !info.Mode().IsRegular() {
			return nil, loop.NonRegularFileError(label, info.Mode())
		}
		in = f
	}
	data, err := io.ReadAll(io.LimitReader(in, loop.ScoreSizeLimit+1))
	if err != nil {
		return nil, err
	}
	if len(data) > loop.ScoreSizeLimit {
		return nil, fmt.Errorf("%s: %s file is %d bytes, over the %d-byte limit", label, kind, len(data), loop.ScoreSizeLimit)
	}
	return data, nil
}
