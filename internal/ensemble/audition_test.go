package ensemble

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jystringfellow/kapellmeister/internal/cao"
	"github.com/jystringfellow/kapellmeister/internal/loop"
)

type behaviour struct {
	bootFails          bool
	bootLeavesTerminal bool
	groupFails         bool
	metadataFails      bool
	ignores            bool
	screen             string
	screenAfterCue     string
}

type fakeTerminal struct {
	id, session, profile, dir string
	group                     []string
	metadata                  map[string]any
	cues                      []string
}

type fakeCAO struct {
	t                *testing.T
	mu               sync.Mutex
	behaviours       map[string]behaviour
	terminals        map[string]*fakeTerminal
	order            []string
	deleted          []string
	deletedTerminals []string
	deleteStatus     map[string]int
	events           []string
	next             int
	onCue            func()
}

var cueCommand = regexp.MustCompile(`'([^']+)' cue done -queue '([^']+)' (\S+)`)

func newFakeCAO(t *testing.T, behaviours map[string]behaviour) (*fakeCAO, *cao.Client) {
	f := &fakeCAO{t: t, behaviours: behaviours, terminals: map[string]*fakeTerminal{}}
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	return f, cao.NewClient(srv.URL)
}

func (f *fakeCAO) settle() {
	f.mu.Lock()
	f.mu.Unlock()
}

func (f *fakeCAO) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	q := r.URL.Query()
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/sessions":
		f.boot(w, "cao-"+q.Get("session_name"), q)
	case r.Method == http.MethodPost && len(parts) == 3 && parts[0] == "sessions" && parts[2] == "terminals":
		f.boot(w, parts[1], q)
	case r.Method == http.MethodDelete && len(parts) == 2 && parts[0] == "sessions":
		f.deleted = append(f.deleted, parts[1])
		fmt.Fprint(w, `{"success":true}`)
	case r.Method == http.MethodDelete && len(parts) == 2 && parts[0] == "terminals":
		f.deletedTerminals = append(f.deletedTerminals, parts[1])
		f.events = append(f.events, "delete "+parts[1])
		if status := f.deleteStatus[parts[1]]; status != 0 {
			w.WriteHeader(status)
			return
		}
		delete(f.terminals, parts[1])
		fmt.Fprint(w, `{}`)
	case r.Method == http.MethodPatch && len(parts) == 3 && parts[2] == "group":
		if f.behaviours[f.terminals[parts[1]].profile].groupFails {
			http.Error(w, "group failed", 500)
			return
		}
		var body struct{ Group []string }
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.terminals[parts[1]].group = body.Group
		fmt.Fprint(w, `{}`)
	case r.Method == http.MethodPatch && len(parts) == 3 && parts[2] == "metadata":
		if f.behaviours[f.terminals[parts[1]].profile].metadataFails {
			http.Error(w, "metadata failed", 500)
			return
		}
		var body struct{ Metadata map[string]any }
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.terminals[parts[1]].metadata = body.Metadata
		fmt.Fprint(w, `{}`)
	case r.Method == http.MethodPost && len(parts) == 4 && parts[2] == "inbox":
		term := f.terminals[parts[1]]
		if q.Get("sender_id") != "km" {
			f.t.Errorf("cue to %s sent as %q, want km", term.profile, q.Get("sender_id"))
		}
		term.cues = append(term.cues, q.Get("message"))
		if f.onCue != nil {
			f.onCue()
		}
		if !f.behaviours[term.profile].ignores {
			m := cueCommand.FindStringSubmatch(q.Get("message"))
			if m == nil {
				f.t.Errorf("cue to %s does not carry a km cue done command: %q", term.profile, q.Get("message"))
			} else if err := Close(m[2], m[3], term.id, ""); err != nil {
				f.t.Errorf("closing cue: %v", err)
			} else {
				f.events = append(f.events, "closed "+term.id)
			}
		}
		fmt.Fprint(w, `{"success":true}`)
	case r.Method == http.MethodGet && len(parts) == 3 && parts[2] == "output":
		term := f.terminals[parts[1]]
		b := f.behaviours[term.profile]
		screen := b.screen
		if len(term.cues) > 0 && b.screenAfterCue != "" {
			screen = b.screenAfterCue
		}
		json.NewEncoder(w).Encode(map[string]string{"output": screen})
	case r.Method == http.MethodGet && len(parts) == 2 && parts[0] == "terminals":
		term := f.terminals[parts[1]]
		if term == nil {
			http.NotFound(w, r)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"id": term.id, "session_name": term.session, "agent_profile": term.profile, "status": "idle"})
	default:
		f.t.Errorf("unexpected CAO call %s %s", r.Method, r.URL)
		http.NotFound(w, r)
	}
}

func (f *fakeCAO) boot(w http.ResponseWriter, session string, q map[string][]string) {
	profile := q["agent_profile"][0]
	if f.behaviours[profile].bootFails {
		http.Error(w, `{"detail":"provider did not reach idle"}`, http.StatusInternalServerError)
		return
	}
	f.next++
	id := fmt.Sprintf("%08x", f.next)
	f.terminals[id] = &fakeTerminal{id: id, session: session, profile: profile, dir: q["working_directory"][0]}
	f.order = append(f.order, id)
	if f.behaviours[profile].bootLeavesTerminal {
		http.Error(w, fmt.Sprintf(`{"detail":{"terminal_id":%q}}`, id), 500)
		return
	}
	json.NewEncoder(w).Encode(map[string]any{"id": id, "session_name": session, "agent_profile": profile, "status": "idle"})
}

func (f *fakeCAO) byProfile(profile string) *fakeTerminal {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, id := range f.order {
		if f.terminals[id].profile == profile {
			return f.terminals[id]
		}
	}
	return nil
}

func members() []Member {
	return []Member{
		{Section: "planner", Part: "planner", Name: "planner", Chair: loop.Chair{Provider: "claude_code", Profile: "km-claude"}},
		{Section: "implementer", Part: "implementer", Name: "implementer", Chair: loop.Chair{Provider: "codex", Profile: "km-codex"}},
		{Section: "review", Part: "reviewer", Name: "reviewer-1", Chair: loop.Chair{Provider: "copilot_cli", Profile: "km-copilot"}},
		{Section: "review", Part: "reviewer", Name: "reviewer-2", Chair: loop.Chair{Provider: "antigravity_cli", Profile: "km-agy", Model: "gemini-3.8-flash-low"}},
	}
}

func requireEveryChair(t *testing.T, report Report) {
	t.Helper()
	if len(report.Chairs) != len(members()) {
		t.Fatalf("report has %d chairs, want %d: %+v", len(report.Chairs), len(members()), report)
	}
}

func audition(client *cao.Client, worktree string) Audition {
	return Audition{Client: client, Ensemble: "thorough", Worktree: worktree, Members: members(), Timeout: 2 * time.Second, KM: "/opt/km bin/km"}
}

func TestAnAuditionBootsEveryChairInOneCAOSessionGroupedByEnsembleAndSection(t *testing.T) {
	fake, client := newFakeCAO(t, nil)
	wt := t.TempDir()
	report, err := audition(client, wt).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !report.OK {
		t.Fatalf("report = %+v, want every chair ok", report)
	}
	sessions := map[string]bool{}
	for _, id := range fake.order {
		sessions[fake.terminals[id].session] = true
	}
	if len(sessions) != 1 || !sessions[report.Session] {
		t.Fatalf("sessions = %v, want one session %q", sessions, report.Session)
	}
	for _, m := range members() {
		term := fake.byProfile(m.Chair.Profile)
		if term == nil {
			t.Fatalf("%s was not booted", m.Name)
		}
		if term.dir != wt {
			t.Errorf("%s started in %q, want the worktree %q", m.Name, term.dir, wt)
		}
		if want := []string{"thorough", m.Section}; !slices.Equal(term.group, want) {
			t.Errorf("%s group = %v, want %v", m.Name, term.group, want)
		}
		if got, want := term.metadata["chair"], m.Section+"."+m.Name+"@thorough"; got != want {
			t.Errorf("%s chair address = %v, want %s", m.Name, got, want)
		}
		if term.metadata["part"] != m.Part || term.metadata["section"] != m.Section {
			t.Errorf("%s metadata = %v, want its part and section", m.Name, term.metadata)
		}
	}
}

func TestAnAuditionCuesEachChairOnceWithTheKmCommandThatClosesTheCue(t *testing.T) {
	fake, client := newFakeCAO(t, nil)
	report, err := audition(client, t.TempDir()).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	requireEveryChair(t, report)
	for _, m := range members() {
		term := fake.byProfile(m.Chair.Profile)
		if term == nil || len(term.cues) != 1 {
			t.Fatalf("%s got %d cues, want 1", m.Name, len(term.cues))
		}
		match := cueCommand.FindStringSubmatch(term.cues[0])
		if match == nil || match[1] != "/opt/km bin/km" {
			t.Errorf("%s cue = %q, want the quoted km binary", m.Name, term.cues[0])
		}
		if !strings.Contains(term.cues[0], m.Section+"."+m.Name+"@thorough") {
			t.Errorf("%s cue does not name its chair: %q", m.Name, term.cues[0])
		}
	}
	for _, c := range report.Chairs {
		if !c.OK || c.Problem != "" {
			t.Errorf("%s = %+v, want ok", c.Address, c)
		}
	}
}

func TestAChairThatNeverClosesItsCueFailsTheAuditionWithTheBlockerOnItsScreen(t *testing.T) {
	_, client := newFakeCAO(t, map[string]behaviour{
		"km-codex": {ignores: true, screen: "Hooks\n  ⚠ 1 hook needs review before it can run.\n  t trust all · enter review · esc close"},
	})
	report, err := audition(client, t.TempDir()).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if report.OK {
		t.Fatal("audition passed with a chair that never closed its cue")
	}
	requireEveryChair(t, report)
	for _, c := range report.Chairs {
		if c.Address == "implementer.implementer@thorough" {
			if c.OK || !strings.Contains(c.Problem, "hook needs review") {
				t.Errorf("implementer = %+v, want a failure naming the hook review screen", c)
			}
		} else if !c.OK {
			t.Errorf("%s = %+v, want the other chairs unaffected", c.Address, c)
		}
	}
}

func TestAChairThatFailsToBootIsReportedAndTheRestOfTheEnsembleStillAuditions(t *testing.T) {
	fake, client := newFakeCAO(t, map[string]behaviour{"km-claude": {bootFails: true}})
	report, err := audition(client, t.TempDir()).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if report.OK {
		t.Fatal("audition passed with a chair that never booted")
	}
	requireEveryChair(t, report)
	for _, c := range report.Chairs {
		if c.Address == "planner.planner@thorough" {
			if c.OK || !strings.Contains(c.Problem, "did not reach idle") {
				t.Errorf("planner = %+v, want the boot failure", c)
			}
		} else if !c.OK {
			t.Errorf("%s = %+v, want ok", c.Address, c)
		}
	}
	if len(fake.order) != 3 {
		t.Errorf("booted %d chairs, want the 3 that can boot", len(fake.order))
	}
}

func TestAnAuditionTearsDownItsSessionUnlessKept(t *testing.T) {
	for _, keep := range []bool{false, true} {
		fake, client := newFakeCAO(t, nil)
		a := audition(client, t.TempDir())
		a.Keep = keep
		report, err := a.Run(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if deleted := slices.Contains(fake.deleted, report.Session); deleted == keep {
			t.Errorf("keep=%v: session deleted = %v", keep, deleted)
		}
	}
}

func TestCloseAppendsOneClosurePerCueToTheQueue(t *testing.T) {
	queue := filepath.Join(t.TempDir(), "runs", "cues.jsonl")
	if err := Close(queue, "cue-1", "0000000a", "fine"); err != nil {
		t.Fatal(err)
	}
	if err := Close(queue, "cue-2", "0000000b", ""); err != nil {
		t.Fatal(err)
	}
	closed, err := Closures(queue)
	if err != nil {
		t.Fatal(err)
	}
	if c := closed["cue-1"]; c.Terminal != "0000000a" || c.Note != "fine" || c.At.IsZero() {
		t.Errorf("cue-1 = %+v", c)
	}
	if _, ok := closed["cue-2"]; !ok || len(closed) != 2 {
		t.Errorf("closures = %+v, want cue-1 and cue-2", closed)
	}
}

func TestMembersOfSeatEachPartInASectionOfItsOwnAndNumberSeveralReviewers(t *testing.T) {
	c := loop.Chair{Provider: "claude_code", Profile: "km-claude"}
	got := MembersOf(loop.Chairs{Planner: c, Implementer: c, Reviewers: []loop.Chair{c, c}, Gate: c, Classifier: c})
	var names []string
	for _, m := range got {
		names = append(names, m.Section+"."+m.Name+":"+m.Part)
	}
	want := []string{"planner.planner:planner", "implementer.implementer:implementer", "reviewer.reviewer-1:reviewer", "reviewer.reviewer-2:reviewer", "gate.gate:gate", "classifier.classifier:classifier"}
	if !slices.Equal(names, want) {
		t.Errorf("members = %v, want %v", names, want)
	}
}

func TestASilentChairFailsWhenItsCueTimesOutWithNothingOnItsScreen(t *testing.T) {
	_, client := newFakeCAO(t, map[string]behaviour{"km-copilot": {ignores: true, screen: "❯ "}})
	a := audition(client, t.TempDir())
	a.Timeout = 300 * time.Millisecond
	report, err := a.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	requireEveryChair(t, report)
	for _, c := range report.Chairs {
		if c.Address == "review.reviewer-1@thorough" && (c.OK || c.Problem != "cue not closed within 300ms") {
			t.Errorf("reviewer-1 = %+v, want a plain timeout", c)
		}
	}
}
