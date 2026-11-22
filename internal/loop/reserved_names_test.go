package loop

import (
	"fmt"
	"strings"
	"testing"
)

func TestReservedNamesInScoresAreRefused(t *testing.T) {
	cases := []struct{ name, text, subject, word, meaning string }{
		{"step", strings.ReplaceAll(smallScoreFile, `"draft"`, `"podium"`), "step podium", "podium", "the idle terminal that keeps a run's CAO session alive"},
		{"step part", strings.Replace(smallScoreFile, `part = "drafter"`, `part = "hall"`, 1), `step draft: part "hall"`, "hall", "something that holds many ensembles"},
		{"part table", strings.ReplaceAll(smallScoreFile, `drafter`, `conductor`), "part conductor", "conductor", "km itself"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := decodeScore(c.text, "score.toml", "score")
			want := fmt.Sprintf("score.toml: %s: %s is km's word for %s; choose another name", c.subject, c.word, c.meaning)
			if err == nil || err.Error() != want {
				t.Errorf("error = %v, want %s", err, want)
			}
		})
	}
}

func TestAScoreFileNamedPodiumIsRefusedFromUserAndRepositoryFiles(t *testing.T) {
	for _, source := range []string{"user", "repository"} {
		t.Run(source, func(t *testing.T) {
			s := ScoreSource{Home: t.TempDir(), AllowRepository: true}
			path := ""
			scoreFileWrite(t, s.Home, "parts/drafter.md", "Draft {reply}.")
			if source == "user" {
				path = scoreFileWrite(t, s.Home, "scores/podium.toml", smallScoreFile)
			} else {
				s.Worktree = t.TempDir()
				scoreFileGit(t, s.Worktree, "init", "-q")
				scoreFileWrite(t, s.Worktree, ".kapellmeister/scores/podium.toml", smallScoreFile)
				scoreFileGit(t, s.Worktree, "add", ".")
				scoreFileGit(t, s.Worktree, "commit", "-qm", "fixture")
				path = ".kapellmeister/scores/podium.toml"
			}
			_, err := LoadScore("podium", s)
			want := path + `: score "podium": podium is km's word for the idle terminal that keeps a run's CAO session alive; choose another name`
			if err == nil || err.Error() != want {
				t.Errorf("error = %v, want %s", err, want)
			}
		})
	}
}

func TestAnEnsembleTableNamedPodiumInTheUserConfigIsRefused(t *testing.T) {
	home := writeUserConfig(t, "[ensembles.podium]\n")
	_, err := LoadSettings(home, "")
	want := UserConfigPath(home) + `: ensembles.podium: podium is km's word for the idle terminal that keeps a run's CAO session alive; choose another name`
	if err == nil || err.Error() != want {
		t.Errorf("error = %v, want %s", err, want)
	}
}

func TestExistingReservedStepNamesKeepTheirMessage(t *testing.T) {
	for _, name := range []string{"verify", "classify-x"} {
		text := strings.ReplaceAll(smallScoreFile, `"draft"`, `"`+name+`"`)
		_, err := decodeScore(text, "score.toml", "score")
		want := "score.toml: step " + name + ": step name is reserved"
		if err == nil || err.Error() != want {
			t.Errorf("error = %v, want %s", err, want)
		}
	}
}

func TestNamesContainingReservedWordsLoadFine(t *testing.T) {
	for _, name := range []string{"podium-2", "town-hall"} {
		text := strings.ReplaceAll(smallScoreFile, `"draft"`, `"`+name+`"`)
		if _, err := decodeScore(text, "score.toml", "score"); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	home := writeUserConfig(t, "[ensembles.town-hall]\n")
	if _, err := LoadSettings(home, ""); err != nil {
		t.Fatal(err)
	}
}
