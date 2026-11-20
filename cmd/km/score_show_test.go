package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jystringfellow/kapellmeister/internal/loop"
)

func TestKmScoreShowDefaultPrintsTheDefaultScoreAsTomlWithBuiltInPartsNamed(t *testing.T) {
	var diag bytes.Buffer
	code := -1
	out, err := captureStdout(t, func() error { code = execute([]string{"score", "show", "default"}, &diag); return nil })
	if err != nil || code != 0 || diag.Len() != 0 {
		t.Fatalf("code=%d error=%v diag=%s", code, err, &diag)
	}
	if strings.Contains(out, "==>") || strings.Contains(out, "Study the code") {
		t.Fatal(out)
	}
	home := t.TempDir()
	writeFile(t, filepath.Join(home, "scores", "copy.toml"), out)
	loaded, err := loop.LoadScore("copy", loop.ScoreSource{Home: home})
	if err != nil || len(loaded.Score.Steps) != len(loop.DefaultScore().Steps) {
		t.Fatalf("score=%+v error=%v diag=%s", loaded, err, &diag)
	}
	for _, part := range []string{"planner", "implementer", "reviewer", "gate", "tester"} {
		if !strings.Contains(out, "[parts."+part+"]") {
			t.Fatal(out)
		}
	}
}
