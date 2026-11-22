package loop

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestScoreDescriptionsAreValidatedInUserAndRepositoryFiles(t *testing.T) {
	for _, repo := range []bool{false, true} {
		for _, value := range []string{"useful", strings.Repeat("x", 512), "", " \n\t", strings.Repeat("x", 513)} {
			s := scoreFileSource(t)
			root, path := s.Home, "scores/small.toml"
			if repo {
				s.Home = t.TempDir()
				s.Worktree = t.TempDir()
				root = s.Worktree
				path = ".kapellmeister/scores/small.toml"
				scoreFileGit(t, root, "init", "-q")
				scoreFileWrite(t, root, ".kapellmeister/parts/drafter.md", "Draft {reply}.")
			}
			full := scoreFileWrite(t, root, path, fmt.Sprintf("description = %q\n", value)+smallScoreFile)
			if repo {
				scoreFileGit(t, root, "add", ".")
				scoreFileGit(t, root, "commit", "-qm", "fixture")
				full = path
			}
			got, err := LoadScore("small", s)
			valid := strings.TrimSpace(value) != "" && len(value) <= 512
			if !valid {
				if err == nil || !strings.Contains(err.Error(), full) {
					t.Fatalf("%q: %v", value, err)
				}
				continue
			}
			if err != nil {
				t.Fatal(err)
			}
			field := reflect.ValueOf(got.Score).FieldByName("Description")
			if !field.IsValid() || field.String() != value {
				t.Fatalf("description missing: %+v", got.Score)
			}
		}
	}
}

func TestParallelRepliesNameThePartWhenNoChairsAreConfigured(t *testing.T) {
	for _, tc := range []struct {
		part string
		kind StepKind
		want string
	}{{"scout", KindProduce, "no scout chairs configured"}, {"reviewer", KindReview, "no reviewers configured"}} {
		_, err := new(Engine).parallelReplies(context.Background(), ScoreStep{Part: tc.part, Kind: tc.kind}, 0, handoff{}, nil)
		if err == nil || err.Error() != tc.want {
			t.Fatalf("%s: %v", tc.part, err)
		}
	}
}
