package main

import (
	"fmt"
	"io"
	"strings"

	"github.com/jystringfellow/kapellmeister/internal/loop"
)

func scoreCommand(args []string, out io.Writer) error {
	if len(args) != 2 || args[0] != "show" {
		return &exitStatusError{code: 2, err: fmt.Errorf("usage: km score show NAME")}
	}
	name := args[1]
	text, ok := loop.BuiltinScoreText(name)
	if !ok {
		if name == "default" {
			return fmt.Errorf("score default has no file form yet; printable built-ins: %s", strings.Join(loop.BuiltinScoreNames(), ", "))
		}
		return fmt.Errorf("unknown printable score %q; printable built-ins: %s", name, strings.Join(loop.BuiltinScoreNames(), ", "))
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
