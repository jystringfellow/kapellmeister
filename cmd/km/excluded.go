package main

import (
	"fmt"
	"io"

	"github.com/jystringfellow/kapellmeister/internal/loop"
)

func printExcluded(w io.Writer, worktree string) {
	test, err := loop.ResolveTest(worktree)
	if err != nil {
		return
	}
	for _, entry := range test.Excluded {
		fmt.Fprintf(w, "verify excludes %s\n", entry)
	}
}
