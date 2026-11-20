package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"syscall"
	"testing"

	"github.com/jystringfellow/kapellmeister/internal/ensemble"
	"github.com/jystringfellow/kapellmeister/internal/loop"
)

func playWithTwoRounds(t *testing.T) (ensemble.Standing, *playCAO, string) {
	t.Helper()
	s := playStanding(t)
	f := &playCAO{changes: true}
	srv := f.serve(t)
	out, err := capturePlay(t, []string{s.Session, "-cao", srv.URL})
	if !errors.Is(err, errNotApproved) || !strings.Contains(out, "rounds_exhausted after 2 round(s)") {
		t.Fatalf("play: %v\n%s", err, out)
	}
	requirePlayStillStanding(t, s, f, out)
	return s, f, out
}

func reviewTerminals(f *playCAO, round int) []string {
	ids := make([]string, 2)
	for i, name := range f.replies {
		for chair := range 2 {
			if name == fmt.Sprintf("review-%d-%d.md", round, chair+1) {
				ids[chair] = f.requests[i].ReuseTerminalID
			}
		}
	}
	return ids
}

func TestPlayPlaysEachReviewChairInANewTerminalEveryRoundAndEveryOtherChairInItsOwn(t *testing.T) {
	s, f, _ := playWithTwoRounds(t)
	first, second := reviewTerminals(f, 1), reviewTerminals(f, 2)
	for i := range first {
		if first[i] == "" || second[i] == "" || first[i] == second[i] {
			t.Errorf("review chair %d: %v then %v", i, first, second)
		}
	}
	counts := map[string]int{}
	for i, req := range f.requests {
		name := f.replies[i]
		part := playReplyPart(name)
		if part == "reviewer" || name == "lessons-reply.json" {
			continue
		}
		for _, chair := range s.Chairs {
			if chair.Part == part {
				counts[part]++
				if req.ReuseTerminalID != chair.TerminalID {
					t.Errorf("%s played in %s, want %s", name, req.ReuseTerminalID, chair.TerminalID)
				}
			}
		}
	}
	for part, want := range map[string]int{"planner": 1, "implementer": 2, "gate": 2, "classifier": 6} {
		if counts[part] != want {
			t.Errorf("%s steps = %d, want %d", part, counts[part], want)
		}
	}
}

func TestPlayDoesNotPlayTheFirstReviewStepInTheTerminalTheRecordNamedBeforeThePlay(t *testing.T) {
	s, f, _ := playWithTwoRounds(t)
	for i, id := range reviewTerminals(f, 1) {
		if id == "" || id == s.Chairs[i+2].TerminalID {
			t.Errorf("first review chair %s reused %s", s.Chairs[i+2].Address, id)
		}
	}
}

func TestPlayLeavesTheRecordNamingTheTerminalsTheReviewChairsLastPlayedInAndReleasesTheOldOnes(t *testing.T) {
	s, f, _ := playWithTwoRounds(t)
	got, err := findStanding(filepath.Join(loop.StateDir(), "ensembles"), s.Session)
	if err != nil {
		t.Fatal(err)
	}
	for i, chair := range s.Chairs {
		want := chair
		if chair.Part == "reviewer" {
			want.TerminalID = reviewTerminals(f, 2)[i-2]
			want.Chair = want.Binding.String()
			for _, old := range []string{chair.TerminalID, reviewTerminals(f, 1)[i-2]} {
				count := 0
				for _, deleted := range f.deleted {
					if deleted == old {
						count++
					}
				}
				if count != 1 {
					t.Errorf("%s released %d times", old, count)
				}
			}
		}
		if !reflect.DeepEqual(got.Chairs[i], want) {
			t.Errorf("chair = %+v, want %+v", got.Chairs[i], want)
		}
	}
	if len(f.deleted) != 4 {
		t.Errorf("deleted = %v", f.deleted)
	}
	for i, launch := range f.launches {
		binding := s.Chairs[2+i%2].Binding
		if launch["provider"] != binding.Provider || launch["profile"] != binding.Profile || launch["model"] != binding.Model {
			t.Errorf("launch = %v, want %v", launch, binding)
		}
		if !strings.Contains(f.briefings[i], s.Task) || !strings.Contains(f.briefings[i], s.Chairs[2+i%2].Address) {
			t.Errorf("briefing = %s", f.briefings[i])
		}
	}
}

func TestPlayReseatsEveryReviewChairBeforeAnyIsSentTheStepAndTheyStillPlayTogether(t *testing.T) {
	s := playStanding(t)
	f := &playCAO{reviewReady: make(chan struct{})}
	srv := f.serve(t)
	out, err := capturePlay(t, []string{s.Session, "-cao", srv.URL})
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	first := -1
	for i, event := range f.sent {
		if strings.HasPrefix(event, "step review-") {
			first = i
			break
		}
	}
	if first < 0 {
		t.Fatal("no review step")
	}
	for _, event := range []string{"create reseated-1", "create reseated-2", "delete " + s.Chairs[2].TerminalID, "delete " + s.Chairs[3].TerminalID} {
		if index := slices.Index(f.sent, event); index < 0 || index >= first {
			t.Errorf("%s did not precede review: %v", event, f.sent)
		}
	}
	if f.reviewWaiting != 2 {
		t.Errorf("review chairs playing together = %d", f.reviewWaiting)
	}
}

func TestPlayPrintsAProgressLinePerReseatedChairBeforeTheReviewStep(t *testing.T) {
	s, _, out := playWithTwoRounds(t)
	for _, chair := range s.Chairs[2:4] {
		line := "reseating review chair " + chair.Address
		if strings.Count(out, line) != 2 {
			t.Errorf("missing progress %s\n%s", line, out)
		}
		if index := strings.Index(out, line); index < 0 || index > strings.Index(out, "review[1]") {
			t.Errorf("late progress\n%s", out)
		}
	}
}

func TestPlayStopsWhenAReseatFailsNamingTheChairAndSendsNoReviewStep(t *testing.T) {
	s := playStanding(t)
	f := &playCAO{failBoot: 2}
	srv := f.serve(t)
	out, err := capturePlay(t, []string{s.Session, "-cao", srv.URL})
	if !errors.Is(err, errNotApproved) || !strings.Contains(out, s.Chairs[3].Address) || !strings.Contains(out, "boot refused") {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, name := range f.replies {
		if strings.HasPrefix(name, "review-") {
			t.Errorf("review sent after failure: %v", f.replies)
		}
	}
	got, err := findStanding(filepath.Join(loop.StateDir(), "ensembles"), s.Session)
	if err != nil {
		t.Fatal(err)
	}
	for i, chair := range got.Chairs {
		if slices.Contains(f.deleted, chair.TerminalID) || chair.TerminalID == "" {
			t.Errorf("record names released terminal: %+v", chair)
		}
		want := s.Chairs[i].TerminalID
		if i == 2 {
			want = "reseated-1"
		}
		if chair.TerminalID != want {
			t.Errorf("chair %s terminal %s, want %s", chair.Address, chair.TerminalID, want)
		}
	}
	requirePlayStillStanding(t, s, f, out)
}

func TestPlayReseatsReviewChairsOfTheBuiltInReviewScore(t *testing.T) {
	s := playStanding(t)
	s.Chairs = slices.Clone(s.Chairs[2:])
	savePlayStanding(t, s)
	writeFile(t, filepath.Join(loop.Home(), "ensembles/score.toml"), "score = 'review'\n[sections.winds.chairs.first]\npart = 'reviewer'\nagents = ['codex']\n[sections.winds.chairs.last]\npart = 'gate'\nagents = ['codex']\n[sections.winds.chairs.voice]\npart = 'classifier'\nagents = ['codex']\n")
	writeFile(t, filepath.Join(s.Worktree, "change.txt"), "change")
	f := &playCAO{}
	srv := f.serve(t)
	out, err := capturePlay(t, []string{s.Session, "-cao", srv.URL})
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for i, id := range reviewTerminals(f, 1) {
		if id == "" || id == s.Chairs[i].TerminalID {
			t.Errorf("review terminal %s", id)
		}
	}
	if f.created != 2 {
		t.Errorf("reseats = %d", f.created)
	}
}

func TestPlayReseatsTheChairsOfAReviewStepNamedAnythingInAScoreFile(t *testing.T) {
	s := playStanding(t)
	s.Chairs[2].Part, s.Chairs[3].Part = "examiner", "examiner"
	savePlayStanding(t, s)
	writeFile(t, filepath.Join(loop.Home(), "ensembles/score.toml"), "score = 'voices'\n[sections.winds.chairs.first]\npart = 'examiner'\nagents = ['codex']\n")
	writeFile(t, filepath.Join(loop.Home(), "parts/examiner.md"), "Examine the change and write {reply}.")
	writeFile(t, filepath.Join(loop.Home(), "scores/voices.toml"), `rounds = 2
[parts.implementer]
memory = "resume"
[[steps]]
name = "plan"
kind = "produce"
part = "planner"
writes = "reply"
next = { done = "implement" }
[[steps]]
name = "implement"
kind = "produce"
part = "implementer"
reads = ["plan"]
next = { done = "audit" }
[[steps]]
name = "audit"
kind = "review"
part = "examiner"
next = { approve = "gate", changes = "implement" }
[[steps]]
name = "gate"
kind = "decide"
part = "gate"
reads = ["plan", "implement", "audit"]
next = { approve = "approved", changes = "implement" }
`)
	f := &playCAO{changes: true}
	srv := f.serve(t)
	out, err := capturePlay(t, []string{s.Session, "-cao", srv.URL})
	if !errors.Is(err, errNotApproved) || !strings.Contains(out, "rounds_exhausted") {
		t.Fatalf("%v\n%s", err, out)
	}
	for i, name := range f.replies {
		if strings.HasPrefix(name, "audit-") && strings.HasSuffix(name, ".md") && !strings.HasPrefix(f.requests[i].ReuseTerminalID, "reseated-") {
			t.Errorf("%s reused %s", name, f.requests[i].ReuseTerminalID)
		}
	}
	if f.created != 4 {
		t.Errorf("reseats = %d, want 4", f.created)
	}
	for i, name := range f.replies {
		if strings.HasPrefix(name, "implement-") && f.requests[i].ReuseTerminalID != s.Chairs[1].TerminalID {
			t.Errorf("resume terminal changed: %+v", f.requests[i])
		}
	}
}

func TestPlayKeepsTheTerminalOfANonReviewPartDeclaringResumeMemory(t *testing.T) {
	s, f, _ := playWithTwoRounds(t)
	count := 0
	for i, name := range f.replies {
		if strings.HasPrefix(name, "implement-") {
			count++
			if f.requests[i].ReuseTerminalID != s.Chairs[1].TerminalID {
				t.Errorf("resume terminal changed: %+v", f.requests[i])
			}
		}
	}
	if count != 2 || slices.Contains(f.deleted, s.Chairs[1].TerminalID) {
		t.Fatalf("implement steps %d, deleted %v", count, f.deleted)
	}
}

func TestStatusJSONDoesNotCountAReseatAsAPlayedStepOrUsage(t *testing.T) {
	root, state := statusDataFixture(t)
	state["command"] = "play"
	state["playing"] = []any{map[string]any{"step": "audit", "chair": "winds.one@voices", "round": 2, "started": statusNow}}
	chairs := state["seating"].(map[string]any)["sections"].([]any)[1].(map[string]any)["chairs"].([]any)
	chairs[0].(map[string]any)["terminal"] = "old"
	saveStatusData(t, root, state)
	writeFile(t, filepath.Join(root, "data/telemetry.jsonl"), `{"step":"audit","part":"auditor","chair":{"address":"winds.one@voices"},"usage":{"known":true,"input_tokens":5,"output_tokens":2,"cache_read_tokens":0,"cache_write_tokens":0}}
{"step":"reseat","part":"auditor","chair":{"address":"winds.one@voices"},"terminal_id":"new"}
`)
	row := statusDataRow(t, root)
	chair := dataChairs(t, row)[1]
	if chair["terminal"] != "new" || chair["state"] != "playing" {
		t.Fatal(chair)
	}
	if len(row["trail"].([]any)) != 1 {
		t.Fatal(row)
	}
	for _, value := range []any{row["usage"], chair["usage"]} {
		usage := value.(map[string]any)
		if usage["measured"] != "all" || usage["input_tokens"] != float64(5) {
			t.Fatal(usage)
		}
	}
}

func TestReportPartsIgnoresReseatRecordsThatCarryAPart(t *testing.T) {
	root := recordRoot(t)
	review := partStep("review", "m", "", 1)
	review["part"] = "reviewer"
	reseat := partStep("reseat", "ignored", "", 1)
	reseat["part"] = "reviewer"
	partsFixture(t, root, "one", "approved", 1, reseat, review)
	rows := partRows(t, root)
	if len(rows) != 1 {
		t.Fatal(rows)
	}
	requireNumbers(t, partRow(t, rows, "reviewer", "m"), map[string]int{"steps": 1})
}

func TestStatusJSONShowsAReseatedChairsNewTerminalOncePlaying(t *testing.T) {
	s := playStanding(t)
	f := &playCAO{}
	observed := 0
	f.afterReply = func(_ string, name string) {
		if !strings.HasPrefix(name, "review-") || !strings.HasSuffix(name, ".md") {
			return
		}
		observed++
		row := statusDataRow(t, filepath.Join(loop.StateDir(), "runs"))
		chairs := dataChairs(t, row)
		var round, ordinal int
		if _, err := fmt.Sscanf(name, "review-%d-%d.md", &round, &ordinal); err != nil {
			t.Error(err)
			return
		}
		for _, chair := range chairs {
			for _, recorded := range s.Chairs {
				if chair["address"] != recorded.Address {
					continue
				}
				want := recorded.TerminalID
				if recorded.Part == "reviewer" {
					index := slices.IndexFunc(s.Chairs[2:4], func(c ensemble.StandingChair) bool { return c.Address == recorded.Address })
					want = fmt.Sprintf("reseated-%d", index+1)
					if index == ordinal-1 && chair["state"] != "playing" {
						t.Errorf("chair is not playing: %v", chair)
					}
				}
				if chair["terminal"] != want {
					t.Errorf("terminal = %v, want %s", chair, want)
				}
			}
		}
	}
	srv := f.serve(t)
	out, err := capturePlay(t, []string{s.Session, "-cao", srv.URL})
	if err != nil || observed != 2 {
		t.Fatalf("observed %d: %v\n%s", observed, err, out)
	}
}

func TestPlayInterruptedDuringAReviewReseatNamesTheSignalAndKeepsEveryChairRecorded(t *testing.T) {
	for _, signal := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM} {
		t.Run((&loop.SignalError{Signal: signal}).Error(), func(t *testing.T) {
			standing := playStanding(t)
			writeFile(t, loop.UserConfigPath(loop.Home()), "[run]\npreflight_timeout = '2s'\n")
			fake := &playCAO{afterBriefing: func(terminal string) bool {
				if terminal != "reseated-2" {
					return true
				}
				if err := syscall.Kill(os.Getpid(), signal); err != nil {
					t.Error(err)
				}
				return false
			}}
			srv := fake.serve(t)
			out, err := capturePlay(t, []string{standing.Session, "-cao", srv.URL})
			if !errors.Is(err, errNotApproved) {
				t.Fatalf("play: %v\n%s", err, out)
			}
			want := (&loop.SignalError{Signal: signal}).Error()
			result := recordedResult(t, onlyRunRecord(t))
			reason, _ := result["reason"].(string)
			if result["outcome"] != loop.Failed || !strings.Contains(reason, want) || !strings.Contains(reason, standing.Chairs[3].Address) || !strings.Contains(out, reason) {
				t.Errorf("reason = %q, want %s and %s\n%s", reason, want, standing.Chairs[3].Address, out)
			}
			for _, reply := range fake.replies {
				if strings.HasPrefix(reply, "review-") {
					t.Errorf("review played after interruption: %v", fake.replies)
				}
			}
			got, err := findStanding(filepath.Join(loop.StateDir(), "ensembles"), standing.Session)
			if err != nil {
				t.Fatal(err)
			}
			for i, chair := range got.Chairs {
				want := standing.Chairs[i]
				if i == 2 {
					want.TerminalID, want.Chair = "reseated-1", want.Binding.String()
				}
				if chair != want || slices.Contains(fake.deleted, chair.TerminalID) {
					t.Errorf("chair = %+v, want existing %+v", chair, want)
				}
			}
			if !slices.Equal(fake.deleted, []string{standing.Chairs[2].TerminalID, "reseated-2"}) {
				t.Errorf("deleted = %v", fake.deleted)
			}
			requirePlayStillStanding(t, standing, fake, out)
		})
	}
}
