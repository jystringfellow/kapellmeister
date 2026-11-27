package main

import (
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/jystringfellow/kapellmeister/internal/loop"
)

func scoreCommand(args []string, out, stderr io.Writer) error {
	if len(args) == 0 || args[0] != "show" {
		return &exitStatusError{code: 2, err: fmt.Errorf("usage: km score (show [-worktree DIR] NAME | check [-worktree DIR] [-name NAME] [-json] FILE)")}
	}
	fs := flag.NewFlagSet("score show", flag.ContinueOnError)
	fs.SetOutput(stderr)
	worktree := fs.String("worktree", ".", "repository directory")
	if err := fs.Parse(args[1:]); err != nil {
		return &exitStatusError{code: 2, err: err}
	}
	if fs.NArg() != 1 {
		return &exitStatusError{code: 2, err: fmt.Errorf("usage: km score show [-worktree DIR] NAME")}
	}
	name := fs.Arg(0)
	if name == "default" {
		_, err := io.WriteString(out, loop.DefaultScore().File())
		return err
	}
	text, ok := loop.BuiltinScoreText(name)
	if !ok {
		settings, err := loop.LoadSettings(loop.Home(), *worktree)
		if err != nil {
			return err
		}
		loaded, _, err := loop.LoadScoreLenient(name, loop.ScoreSource{Home: loop.Home(), Worktree: *worktree, AllowRepository: settings.RepositoryScores})
		if err != nil {
			return fmt.Errorf("%w; printable built-ins: %s", err, strings.Join(append([]string{"default"}, loop.BuiltinScoreNames()...), ", "))
		}
		_, err = io.WriteString(out, loaded.Score.File())
		return err
	}
	fmt.Fprintf(out, "==> scores/%s.toml <==\n%s", name, text)
	score, _ := loop.BuiltinScore(name)
	for _, part := range score.Parts {
		if part.Name == score.Classifier {
			continue
		}
		fmt.Fprintf(out, "\n==> parts/%s.md <==\n%s", part.Name, loop.BuiltinPartText(name, part.Name))
	}
	return nil
}
