package loop

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

type fakeReseater struct {
	calls []string
	play  func(Chair) (Reseated, error)
}

func (f *fakeReseater) Reseat(_ context.Context, chair Chair) (Reseated, error) {
	f.calls = append(f.calls, chair.Address)
	return f.play(chair)
}

func TestReviewChairsAreReseatedInOrderBeforeTheSnapshotAndOnlyForReviewKinds(t *testing.T) {
	for _, name := range []string{"review", "audit"} {
		t.Run(name, func(t *testing.T) {
			eng, fake, records := newEngine(t, passing)
			eng.Config.Chairs.Reviewers = []Chair{{Provider: "p", Profile: "x", Address: "winds.first"}, {Provider: "p", Profile: "x", Address: "winds.second"}}
			eng.Score = DefaultScore()
			for i := range eng.Score.Steps {
				step := &eng.Score.Steps[i]
				if step.Name == "review" {
					step.Name = name
				}
				for j := range step.Exits {
					if step.Exits[j].To == "review" {
						step.Exits[j].To = name
					}
				}
				for j := range step.Reads {
					if step.Reads[j] == "review" {
						step.Reads[j] = name
					}
				}
			}
			reseater := &fakeReseater{}
			reseater.play = func(chair Chair) (Reseated, error) {
				for _, step := range fake.steps {
					if step.Name == name {
						t.Error("review played before all reseats")
					}
				}
				err := os.WriteFile(filepath.Join(eng.Config.Worktree, "reseated.txt"), []byte(chair.Address), 0600)
				return Reseated{Old: "old-" + chair.Address, New: "new-" + chair.Address}, err
			}
			eng.Reseater = reseater
			fake.actions[name] = func(step Step) {
				if !slices.Equal(reseater.calls, []string{"winds.first", "winds.second"}) {
					t.Errorf("reseats = %v", reseater.calls)
				}
				fake.write(step.ReplyPath, "APPROVE\n")
			}
			result := run(t, eng)
			if result.Outcome != Approved || len(result.Restores) != 0 {
				t.Fatal(result)
			}
			if !slices.Equal(reseater.calls, []string{"winds.first", "winds.second"}) {
				t.Fatal(reseater.calls)
			}
			got := recordsOf(t, records, "reseat")
			if len(got) != 2 {
				t.Fatal(got)
			}
			for i, record := range got {
				if record.Chair.Address != reseater.calls[i] || record.TerminalID != "new-"+reseater.calls[i] || record.Part != "reviewer" {
					t.Fatal(record)
				}
			}
			data, err := os.ReadFile(filepath.Join(eng.Config.Worktree, "reseated.txt"))
			if err != nil || string(data) != "winds.second" {
				t.Fatalf("snapshot lost reseat work: %s, %v", data, err)
			}
		})
	}
}

func TestAReseatFailureStopsBeforeReviewAndNamesTheChairWithoutReseatingLaterChairs(t *testing.T) {
	eng, fake, _ := newEngine(t, passing)
	for _, address := range []string{"winds.first", "winds.second", "winds.third"} {
		eng.Config.Chairs.Reviewers = append(eng.Config.Chairs.Reviewers, Chair{Provider: "p", Profile: "x", Address: address})
	}
	eng.Config.Chairs.Reviewers = eng.Config.Chairs.Reviewers[1:]
	reseater := &fakeReseater{play: func(c Chair) (Reseated, error) {
		if c.Address == "winds.second" {
			return Reseated{}, errors.New("briefing failed")
		}
		return Reseated{Old: "old", New: "new"}, nil
	}}
	eng.Reseater = reseater
	result := run(t, eng)
	if result.Outcome != Failed || !strings.Contains(result.Reason, "winds.second") || !strings.Contains(result.Reason, "briefing failed") {
		t.Fatal(result)
	}
	if !slices.Equal(reseater.calls, []string{"winds.first", "winds.second"}) {
		t.Fatal(reseater.calls)
	}
	for _, step := range fake.steps {
		if step.Name == "review" {
			t.Fatal("review played after reseat failed")
		}
	}
}

func TestAChairListedTwiceIsReseatedOnceBeforeReview(t *testing.T) {
	eng, _, _ := newEngine(t, passing)
	chair := eng.Config.Chairs.Reviewers[0]
	chair.Address = "winds.first"
	eng.Config.Chairs.Reviewers = []Chair{chair, chair}
	reseater := &fakeReseater{play: func(Chair) (Reseated, error) { return Reseated{Old: "old", New: "new"}, nil }}
	eng.Reseater = reseater
	if result := run(t, eng); result.Outcome != Approved {
		t.Fatal(result)
	}
	if !slices.Equal(reseater.calls, []string{chair.Address}) {
		t.Fatal(reseater.calls)
	}
}

func TestNoReviewChairsFailsBeforeAnyReseat(t *testing.T) {
	eng, _, _ := newEngine(t, passing)
	eng.Config.Chairs.Reviewers = nil
	reseater := &fakeReseater{play: func(Chair) (Reseated, error) { t.Fatal("reseat without a chair"); return Reseated{}, nil }}
	eng.Reseater = reseater
	result := run(t, eng)
	if result.Outcome != Failed || !strings.Contains(result.Reason, "no reviewers configured") || len(reseater.calls) != 0 {
		t.Fatal(result)
	}
}
