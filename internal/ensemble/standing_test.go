package ensemble

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestStandingRecordsRoundTripInSessionOrderAndRemovalOnlyRemovesTheNamedSession(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "ensembles")
	got, err := ListStanding(dir)
	if err != nil || len(got) != 0 {
		t.Fatalf("missing directory = %v, %v", got, err)
	}
	a := Standing{Ensemble: "thorough", Session: "a", Worktree: "/work", Queue: "/work/cues.jsonl", Task: "full task\nsecond line", Chairs: []StandingChair{{Address: "review.security@thorough", Section: "review", Part: "reviewer", Chair: "codex:km-codex", TerminalID: "1234"}}}
	b := Standing{Ensemble: "quick", Session: "b", TaskFile: "/task.md"}
	for _, s := range []Standing{b, a} {
		if err := s.Save(dir); err != nil {
			t.Fatal(err)
		}
	}
	got, err = ListStanding(dir)
	if err != nil || !reflect.DeepEqual(got, []Standing{a, b}) {
		t.Fatalf("round trip = %+v, %v", got, err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "a.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{`"ensemble"`, `"session"`, `"worktree"`, `"queue"`, `"task"`, `"chairs"`, `"address"`, `"section"`, `"part"`, `"chair"`, `"terminal_id"`} {
		if !strings.Contains(string(data), field) {
			t.Errorf("record lacks %s: %s", field, data)
		}
	}
	for range 2 {
		if err := RemoveStanding(dir, "a"); err != nil {
			t.Fatal(err)
		}
	}
	got, err = ListStanding(dir)
	if err != nil || !reflect.DeepEqual(got, []Standing{b}) {
		t.Fatalf("remaining records = %+v, %v", got, err)
	}
}

func TestAnInvalidStandingRecordReportsItsFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "broken.json")
	if err := os.WriteFile(path, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ListStanding(dir); err == nil || !strings.Contains(err.Error(), path) {
		t.Fatalf("invalid record error = %v", err)
	}
}
