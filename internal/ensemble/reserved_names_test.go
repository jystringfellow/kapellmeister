package ensemble

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jystringfellow/kapellmeister/internal/loop"
)

func TestReservedEnsembleNamesAreRefusedWhenFilesLoad(t *testing.T) {
	cases := []struct{ name, file, body, kind, word, meaning string }{
		{"section", "duo", `[sections.podium.chairs.lead]
part="planner"
agents=["codex"]`, "section", "podium", "the idle terminal that keeps a run's CAO session alive"},
		{"chair", "duo", `[sections.leads.chairs.hall]
part="planner"
agents=["codex"]`, "chair", "hall", "something that holds many ensembles"},
		{"ensemble", "conductor", thorough, "ensemble", "conductor", "km itself"},
		{"score", "duo", `score="podium"` + "\n" + thorough, "score", "podium", "the idle terminal that keeps a run's CAO session alive"},
	}
	for _, c := range cases {
		for _, source := range []string{"user", "repository"} {
			t.Run(c.name+"/"+source, func(t *testing.T) {
				home, wt := t.TempDir(), t.TempDir()
				dir := filepath.Join(wt, RepoDir)
				if source == "user" {
					dir = filepath.Join(home, UserDir)
				}
				path := writeEnsemble(t, dir, c.file, c.body)
				_, _, err := Find(c.file, home, wt, loop.BuiltInAgents())
				want := fmt.Sprintf("%s: %s %q: %s is km's word for %s; choose another name", path, c.kind, c.word, c.word, c.meaning)
				if err == nil || err.Error() != want {
					t.Errorf("error = %v, want %s", err, want)
				}
				if c.kind == "score" || c.kind == "ensemble" {
					_, err = ScoreName(c.file, home, wt)
					if err == nil || err.Error() != want {
						t.Errorf("ScoreName error = %v, want %s", err, want)
					}
				}
			})
		}
	}
}

func TestSectionsContainingReservedWordsLoadFine(t *testing.T) {
	home, wt := repoEnsemble(t, "duo", strings.ReplaceAll(strings.ReplaceAll(thorough, "sections.leads", "sections.podium-2"), "sections.review", "sections.town-hall"))
	find(t, "duo", home, wt)
}
