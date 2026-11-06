package ensemble

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jystringfellow/kapellmeister/internal/loop"
)

func standingForReseat(t *testing.T, behaviours map[string]behaviour, index int) (*fakeCAO, Reseat, []byte) {
	t.Helper()
	fake, client := newFakeCAO(t, behaviours)
	a := rehearsal(t, client, t.TempDir())
	if _, err := a.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	records, err := ListStanding(a.StateDir)
	if err != nil || len(records) != 1 {
		t.Fatalf("records = %v, %v", records, err)
	}
	s := records[0]
	data := readStandingBytes(t, a.StateDir, s.Session)
	return fake, Reseat{Client: client, Standing: s, Address: s.Chairs[index].Address, Chair: s.Chairs[index].Binding, Timeout: 300 * time.Millisecond, KM: a.KM, Task: a.Task, StateDir: a.StateDir}, data
}

func readStandingBytes(t *testing.T, dir, session string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, session+".json"))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func requireNoSessionDeleted(t *testing.T, fake *fakeCAO) {
	t.Helper()
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.deleted) != 0 {
		t.Fatalf("deleted sessions = %v", fake.deleted)
	}
}

func TestAReseatCreatesExactlyOneTerminalInTheRecordedSessionWithTheChairsLabels(t *testing.T) {
	fake, a, _ := standingForReseat(t, nil, 2)
	before := len(fake.order)
	result, err := a.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.order) != before+1 {
		t.Fatalf("created %d terminals", len(fake.order)-before)
	}
	term := fake.terminals[result.New]
	c := a.Standing.Chairs[2]
	if term == nil || term.session != a.Standing.Session || term.dir != a.Standing.Worktree {
		t.Fatalf("terminal = %+v", term)
	}
	if !slices.Equal(term.group, []string{a.Standing.Ensemble, c.Section}) {
		t.Errorf("group = %v", term.group)
	}
	want := map[string]any{"chair": c.Address, "section": c.Section, "part": c.Part, "ensemble": a.Standing.Ensemble}
	if !reflect.DeepEqual(term.metadata, want) {
		t.Errorf("metadata = %v", term.metadata)
	}
	if len(fake.deleted) != 0 {
		t.Fatalf("deleted sessions = %v", fake.deleted)
	}
}

func TestAReseatBriefsTheNewOccupantOnceWithTheTaskItsAddressItsSectionMatesAndTheQuotedKmCommand(t *testing.T) {
	for _, index := range []int{0, 2} {
		fake, a, _ := standingForReseat(t, nil, index)
		result, err := a.Run(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		term := fake.terminals[result.New]
		if len(term.cues) != 1 {
			t.Fatalf("cues = %v", term.cues)
		}
		text := term.cues[0]
		c := a.Standing.Chairs[index]
		mates := "none"
		if index == 2 {
			mates = a.Standing.Chairs[3].Address
		}
		for _, want := range []string{a.Task, c.Address, "part " + c.Part, "Section-mates: " + mates, "do not change any files yet"} {
			if !strings.Contains(text, want) {
				t.Errorf("briefing %q lacks %q", text, want)
			}
		}
		match := cueCommand.FindStringSubmatch(text)
		if match == nil || match[1] != a.KM || match[3] != "reseat-"+c.Section+"-"+c.Name {
			t.Fatalf("closure command = %v", match)
		}
		if filepath.Dir(filepath.Dir(match[2])) != filepath.Join(a.Standing.Worktree, ".kapellmeister", "runs") || !strings.HasPrefix(filepath.Base(filepath.Dir(match[2])), "reseat-") {
			t.Errorf("queue = %s", match[2])
		}
		requireNoSessionDeleted(t, fake)
	}
}

func TestAReseatDeletesTheOldTerminalOnlyAfterTheNewBriefingClosedAndUpdatesOnlyThatChairInTheRecord(t *testing.T) {
	fake, a, _ := standingForReseat(t, nil, 2)
	old := a.Standing.Chairs[2].TerminalID
	fake.events = nil
	fake.onCue = func() {
		if !bytes.Equal(readStandingBytes(t, a.StateDir, a.Standing.Session), mustStandingBytes(t, a.Standing)) {
			t.Error("record changed before briefing closed")
		}
		if len(fake.deletedTerminals) != 0 {
			t.Error("old terminal deleted before briefing closed")
		}
	}
	result, err := a.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Address != a.Address || result.Old != old || result.New == old {
		t.Fatalf("result = %+v", result)
	}
	if !slices.Equal(fake.events, []string{"closed " + result.New, "delete " + old}) {
		t.Errorf("events = %v", fake.events)
	}
	want := a.Standing
	want.Chairs = slices.Clone(want.Chairs)
	want.Chairs[2].TerminalID = result.New
	want.Chairs[2].Binding = a.Chair
	want.Chairs[2].Chair = a.Chair.String()
	want.Chairs[2].Briefed = true
	if got := readStandingBytes(t, a.StateDir, want.Session); !bytes.Equal(got, mustStandingBytes(t, want)) {
		t.Errorf("record = %s, want %s", got, mustStandingBytes(t, want))
	}
	if a.Standing.Chairs[2].TerminalID != old {
		t.Error("input record mutated")
	}
	requireNoSessionDeleted(t, fake)
}

func mustStandingBytes(t *testing.T, s Standing) []byte {
	t.Helper()
	dir := t.TempDir()
	if err := s.Save(dir); err != nil {
		t.Fatal(err)
	}
	return readStandingBytes(t, dir, s.Session)
}

func TestAReseatWithAnotherBindingRecordsTheNewBindingAndChairString(t *testing.T) {
	fake, a, _ := standingForReseat(t, nil, 2)
	a.Chair = loop.Chair{Provider: "codex", Profile: "other", Model: "model-x"}
	result, err := a.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	records, err := ListStanding(a.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	want := a.Chair
	want.Address = a.Address
	got := records[0].Chairs[2]
	if got.Binding != want || got.Chair != want.String() || got.TerminalID != result.New || !got.Briefed {
		t.Fatalf("chair = %+v", got)
	}
	requireNoSessionDeleted(t, fake)
}

func TestAnOldTerminalThatCAOReportsGoneStillLetsTheReseatSucceed(t *testing.T) {
	fake, a, _ := standingForReseat(t, nil, 2)
	fake.deleteStatus = map[string]int{a.Standing.Chairs[2].TerminalID: 404}
	result, err := a.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	records, _ := ListStanding(a.StateDir)
	if records[0].Chairs[2].TerminalID != result.New {
		t.Fatal("new terminal was not recorded")
	}
	requireNoSessionDeleted(t, fake)
}

func TestAReseatThatFailsToBootDeletesNothingAndLeavesTheRecordByteForByteUnchanged(t *testing.T) {
	fake, a, before := standingForReseat(t, nil, 2)
	fake.behaviours = map[string]behaviour{a.Chair.Profile: {bootFails: true}}
	_, err := a.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), a.Address) || !strings.Contains(err.Error(), "boot") {
		t.Fatalf("error = %v", err)
	}
	if len(fake.deletedTerminals) != 0 {
		t.Errorf("deleted terminals = %v", fake.deletedTerminals)
	}
	if !bytes.Equal(before, readStandingBytes(t, a.StateDir, a.Standing.Session)) {
		t.Error("record changed")
	}
	requireNoSessionDeleted(t, fake)
}

func TestAFailedReseatDeletesTheNewTerminalKeepsTheOldOneAndLeavesTheRecordUnchanged(t *testing.T) {
	for _, problem := range []string{"unclosed", "interrupted", "group", "metadata", "boot"} {
		t.Run(problem, func(t *testing.T) {
			fake, a, before := standingForReseat(t, nil, 2)
			b := behaviour{}
			want := problem
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch problem {
			case "unclosed":
				b.ignores = true
				b.screenAfterCue = "Hooks\n  ⚠ 1 hook needs review before it can run.\n  t trust all · enter review · esc close"
				want = "hook needs review"
			case "interrupted":
				b.ignores = true
				fake.onCue = cancel
				want = "canceled"
			case "group":
				b.groupFails = true
			case "metadata":
				b.metadataFails = true
			case "boot":
				b.bootLeavesTerminal = true
			}
			fake.behaviours = map[string]behaviour{a.Chair.Profile: b}
			old := a.Standing.Chairs[2].TerminalID
			_, err := a.Run(ctx)
			if err == nil || !strings.Contains(err.Error(), a.Address) || !strings.Contains(err.Error(), want) {
				t.Fatalf("error = %v", err)
			}
			if len(fake.deletedTerminals) != 1 || fake.deletedTerminals[0] == old || fake.terminals[old] == nil {
				t.Fatalf("deleted terminals = %v", fake.deletedTerminals)
			}
			if !bytes.Equal(before, readStandingBytes(t, a.StateDir, a.Standing.Session)) {
				t.Error("record changed")
			}
			requireNoSessionDeleted(t, fake)
		})
	}
}

func TestAChairRecordedWithNoTerminalCanBeReseatedAndBecomesBriefed(t *testing.T) {
	fake, a, _ := standingForReseat(t, map[string]behaviour{"km-claude": {bootFails: true}}, 0)
	if a.Standing.Chairs[0].TerminalID != "" {
		t.Fatal("chair already has a terminal")
	}
	fake.behaviours = nil
	result, err := a.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	records, _ := ListStanding(a.StateDir)
	c := records[0].Chairs[0]
	if !c.Briefed || c.TerminalID != result.New || result.Old != "" {
		t.Fatalf("chair = %+v, result = %+v", c, result)
	}
	if len(fake.deletedTerminals) != 0 {
		t.Errorf("deleted terminals = %v", fake.deletedTerminals)
	}
	requireNoSessionDeleted(t, fake)
}

func TestAnOldTerminalWhoseDeleteCAODefersButWhoseLookupIsGoneLetsTheReseatSucceed(t *testing.T) {
	fake, a, _ := standingForReseat(t, nil, 2)
	old := a.Standing.Chairs[2].TerminalID
	fake.deleteStatus = map[string]int{old: 409}
	fake.mu.Lock()
	delete(fake.terminals, old)
	fake.mu.Unlock()
	result, err := a.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	records, _ := ListStanding(a.StateDir)
	if records[0].Chairs[2].TerminalID != result.New || result.New == "" {
		t.Fatalf("result = %+v, record = %+v", result, records[0])
	}
	requireNoSessionDeleted(t, fake)
}

func TestAReseatThatCannotDeleteTheOldTerminalStillRecordsTheNewOccupantAndNamesTheOldOne(t *testing.T) {
	fake, a, _ := standingForReseat(t, nil, 2)
	old := a.Standing.Chairs[2].TerminalID
	fake.deleteStatus = map[string]int{old: 500}
	result, err := a.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), old) || !strings.Contains(err.Error(), a.Address) {
		t.Fatalf("error = %v", err)
	}
	records, _ := ListStanding(a.StateDir)
	if records[0].Chairs[2].TerminalID != result.New || result.New == "" {
		t.Fatalf("result = %+v, record = %+v", result, records[0])
	}
	if !slices.Equal(fake.deletedTerminals, []string{old}) {
		t.Errorf("deleted = %v", fake.deletedTerminals)
	}
	requireNoSessionDeleted(t, fake)
}

func TestAnInterruptedReseatReportsTheBlockerOnTheNewOccupantsScreen(t *testing.T) {
	fake, a, before := standingForReseat(t, nil, 2)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fake.onCue = cancel
	fake.behaviours = map[string]behaviour{a.Chair.Profile: {ignores: true, screenAfterCue: "Hooks\n  ⚠ 1 hook needs review before it can run.\n  t trust all · enter review · esc close"}}
	_, err := a.Run(ctx)
	if err == nil || !strings.Contains(err.Error(), "canceled") || !strings.Contains(err.Error(), "hook needs review") {
		t.Fatalf("error = %v", err)
	}
	if !bytes.Equal(before, readStandingBytes(t, a.StateDir, a.Standing.Session)) {
		t.Error("record changed")
	}
	requireNoSessionDeleted(t, fake)
}
