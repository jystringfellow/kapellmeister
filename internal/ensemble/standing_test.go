package ensemble

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/jystringfellow/kapellmeister/internal/cao"
	"github.com/jystringfellow/kapellmeister/internal/loop"
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

func TestStandingBindingsNamesAndGateWhenRoundTrip(t *testing.T) {
	s := standingScore()
	s.GateWhen = loop.GateWhenAll
	dir := t.TempDir()
	if err := s.Save(dir); err != nil {
		t.Fatal(err)
	}
	records, err := ListStanding(dir)
	if err != nil || !reflect.DeepEqual(records, []Standing{s}) {
		t.Fatalf("round trip = %+v, %v", records, err)
	}
	data, err := os.ReadFile(filepath.Join(dir, s.Session+".json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"binding"`, `"name"`, `"gate_when"`} {
		if !strings.Contains(string(data), want) {
			t.Errorf("record lacks %s", want)
		}
	}
}

func TestAStandingEnsembleRebuildsItsChairsWithReviewersInRecordOrder(t *testing.T) {
	s := standingScore()
	chairs, err := s.RebuildChairs()
	if err != nil {
		t.Fatal(err)
	}
	got := []loop.Chair{chairs.Planner, chairs.Implementer, chairs.Reviewers[0], chairs.Reviewers[1], chairs.Gate, chairs.Classifier}
	for i, chair := range got {
		if chair != s.Chairs[i].Binding {
			t.Errorf("chair %d = %+v, want %+v", i, chair, s.Chairs[i].Binding)
		}
	}
}

func TestAStandingEnsembleMissingAPartCannotBeRebuilt(t *testing.T) {
	s := standingScore()
	s.Chairs = s.Chairs[1:]
	if _, err := s.RebuildChairs(); err == nil || !strings.Contains(err.Error(), "planner") || !strings.Contains(err.Error(), s.Session) {
		t.Fatalf("missing part: %v", err)
	}
}

func TestARecordWithoutBindingsAsksForANewRehearsal(t *testing.T) {
	s := standingScore()
	s.Chairs[0].Binding = loop.Chair{}
	_, err := s.RebuildChairs()
	for _, want := range []string{s.Session, "older km", "rehearse again"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("old record: %v lacks %q", err, want)
		}
	}
}

func TestMissingOccupantsNamesEveryChairCAOReports404For(t *testing.T) {
	s := standingScore()
	for _, status := range []int{http.StatusNotFound, http.StatusServiceUnavailable} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var checked []string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				checked = append(checked, r.URL.Path)
				if r.URL.Path == "/terminals/"+s.Chairs[1].TerminalID || r.URL.Path == "/terminals/"+s.Chairs[4].TerminalID {
					w.WriteHeader(status)
					return
				}
				fmt.Fprint(w, `{}`)
			}))
			defer srv.Close()
			missing, err := MissingOccupants(context.Background(), cao.NewClient(srv.URL), s)
			if status == http.StatusNotFound {
				want := []string{s.Chairs[1].Address, s.Chairs[4].Address}
				if err != nil || !reflect.DeepEqual(missing, want) || len(checked) != len(s.Chairs) {
					t.Fatalf("missing = %v, checked = %v, error = %v", missing, checked, err)
				}
			} else {
				var httpErr *cao.HTTPError
				if !errors.As(err, &httpErr) || httpErr.Status != status {
					t.Fatalf("non-404 error = %v", err)
				}
			}
		})
	}
}
