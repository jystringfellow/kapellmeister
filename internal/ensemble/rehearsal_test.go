package ensemble

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jystringfellow/kapellmeister/internal/cao"
)

func rehearsal(t *testing.T, client *cao.Client, wt string) Rehearsal {
	t.Helper()
	return Rehearsal{Client: client, Ensemble: "thorough", Worktree: wt, Members: members(), Timeout: 2 * time.Second, KM: "/opt/km bin/km", Task: "Implement the task.\nPreserve the whole text.", StateDir: t.TempDir()}
}

func TestARehearsalBootsEveryChairInOneCAOSessionWithTheAuditionsLabels(t *testing.T) {
	fake, client := newFakeCAO(t, nil)
	wt := t.TempDir()
	report, err := rehearsal(t, client, wt).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	requireEveryChair(t, report)
	if !report.OK || report.Session == "" {
		t.Fatalf("report = %+v", report)
	}
	if filepath.Dir(filepath.Dir(report.Queue)) != filepath.Join(wt, ".kapellmeister", "runs") || !strings.HasPrefix(filepath.Base(filepath.Dir(report.Queue)), "rehearse-") {
		t.Errorf("queue = %s", report.Queue)
	}
	for _, m := range members() {
		term := fake.byProfile(m.Chair.Profile)
		if term == nil {
			t.Fatalf("%s never booted", m.Name)
		}
		if term.session != report.Session || term.dir != wt {
			t.Errorf("terminal = %+v", term)
		}
		if !slices.Equal(term.group, []string{"thorough", m.Section}) {
			t.Errorf("group = %v", term.group)
		}
		if term.metadata["chair"] != m.Address("thorough") || term.metadata["section"] != m.Section || term.metadata["part"] != m.Part || term.metadata["ensemble"] != "thorough" {
			t.Errorf("metadata = %v", term.metadata)
		}
	}
}

func TestARehearsalBriefsEachChairExactlyOnceWithTheTaskItsAddressItsSectionMatesAndTheQuotedKmCommand(t *testing.T) {
	fake, client := newFakeCAO(t, nil)
	a := rehearsal(t, client, t.TempDir())
	report, err := a.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !report.OK {
		t.Fatalf("report = %+v", report)
	}
	for _, m := range a.Members {
		term := fake.byProfile(m.Chair.Profile)
		if term == nil || len(term.cues) != 1 {
			t.Fatalf("%s did not get exactly one cue: %+v", m.Name, term)
		}
		text := term.cues[0]
		for _, want := range []string{a.Task, m.Address(a.Ensemble), "part " + m.Part, "briefing", "do not change any files yet"} {
			if !strings.Contains(text, want) {
				t.Errorf("cue %q lacks %q", text, want)
			}
		}
		var mates []string
		for _, other := range a.Members {
			if other.Section == m.Section && other.Name != m.Name {
				mates = append(mates, other.Address(a.Ensemble))
			}
			if other.Section != m.Section && strings.Contains(text, other.Address(a.Ensemble)) {
				t.Errorf("cue %q names another section's chair", text)
			}
		}
		wantMates := "none"
		if len(mates) > 0 {
			wantMates = strings.Join(mates, ", ")
		}
		if !strings.Contains(text, "Section-mates: "+wantMates) {
			t.Errorf("section-mates missing: %q", text)
		}
		match := cueCommand.FindStringSubmatch(text)
		if match == nil || match[1] != a.KM || match[2] != report.Queue || match[3] != "rehearse-"+m.Section+"-"+m.Name {
			t.Errorf("quoted closure command = %v, cue = %q", match, text)
		}
	}
}

func TestARehearsalNeverDeletesItsSessionEvenWhenChairsFail(t *testing.T) {
	for _, fails := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "failure"}[fails], func(t *testing.T) {
			behaviours := map[string]behaviour{}
			if fails {
				behaviours["km-claude"] = behaviour{bootFails: true}
				behaviours["km-codex"] = behaviour{ignores: true}
			}
			fake, client := newFakeCAO(t, behaviours)
			a := rehearsal(t, client, t.TempDir())
			a.Timeout = 300 * time.Millisecond
			report, err := a.Run(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if report.OK == fails || report.Session == "" {
				t.Errorf("report = %+v", report)
			}
			if len(fake.deleted) != 0 {
				t.Errorf("deleted sessions = %v", fake.deleted)
			}
		})
	}
}

func TestAChairThatNeverClosesItsBriefingIsReportedWithTheBlockerOnItsScreen(t *testing.T) {
	fake, client := newFakeCAO(t, map[string]behaviour{"km-codex": {ignores: true, screenAfterCue: "Hooks\n  ⚠ 1 hook needs review before it can run.\n  t trust all · enter review · esc close"}})
	a := rehearsal(t, client, t.TempDir())
	a.Timeout = 300 * time.Millisecond
	report, err := a.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if report.OK {
		t.Fatal("rehearsal passed despite an unclosed briefing")
	}
	if len(fake.byProfile("km-codex").cues) != 1 {
		t.Fatal("implementer was not briefed")
	}
	for _, c := range report.Chairs {
		if c.Address == "implementer.implementer@thorough" {
			if c.OK || !strings.Contains(c.Problem, "cue not closed within") || !strings.Contains(c.Problem, "hook needs review") {
				t.Errorf("implementer = %+v", c)
			}
		} else if !c.OK {
			t.Errorf("other chair failed: %+v", c)
		}
	}
}

func TestARehearsalRecordsTheStandingEnsembleForKmPlayBeforeBriefing(t *testing.T) {
	for _, taskFile := range []bool{false, true} {
		t.Run(map[bool]string{false: "text", true: "file"}[taskFile], func(t *testing.T) {
			fake, client := newFakeCAO(t, map[string]behaviour{"km-claude": {bootFails: true}})
			a := rehearsal(t, client, t.TempDir())
			if taskFile {
				a.TaskFile = filepath.Join(t.TempDir(), "task.md")
			}
			fake.onCue = func() {
				records, err := ListStanding(a.StateDir)
				if err != nil || len(records) != 1 {
					t.Errorf("standing ensemble missing before briefing: %+v, %v", records, err)
				}
			}
			report, err := a.Run(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			records, err := ListStanding(a.StateDir)
			if err != nil || len(records) != 1 {
				t.Fatalf("records = %+v, %v", records, err)
			}
			s := records[0]
			if s.Ensemble != a.Ensemble || s.Session != report.Session || s.Worktree != a.Worktree || s.Queue != report.Queue {
				t.Errorf("record = %+v", s)
			}
			if taskFile {
				if s.TaskFile != a.TaskFile || s.Task != "" {
					t.Errorf("task file record = %+v", s)
				}
			} else if s.Task != a.Task || s.TaskFile != "" {
				t.Errorf("task text record = %+v", s)
			}
			if len(s.Chairs) != 3 {
				t.Fatalf("recorded chairs = %+v", s.Chairs)
			}
			for i, c := range s.Chairs {
				m := a.Members[i+1]
				term := fake.byProfile(m.Chair.Profile)
				if c.Address != m.Address(a.Ensemble) || c.Section != m.Section || c.Part != m.Part || c.Chair != m.Chair.String() || c.TerminalID != term.id {
					t.Errorf("standing chair = %+v, member = %+v", c, m)
				}
			}
		})
	}
}

func TestARehearsalRecordsNothingWhenNoChairBoots(t *testing.T) {
	behaviours := map[string]behaviour{}
	for _, m := range members() {
		behaviours[m.Chair.Profile] = behaviour{bootFails: true}
	}
	_, client := newFakeCAO(t, behaviours)
	a := rehearsal(t, client, t.TempDir())
	report, err := a.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	records, err := ListStanding(a.StateDir)
	if err != nil || len(records) != 0 || report.Session != "" || report.OK {
		t.Fatalf("report = %+v; records = %+v, %v", report, records, err)
	}
}

func TestARehearsalNamesItsRunningSessionIfItsStandingRecordCannotBeWritten(t *testing.T) {
	fake, client := newFakeCAO(t, nil)
	a := rehearsal(t, client, t.TempDir())
	path := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	a.StateDir = path
	report, err := a.Run(context.Background())
	if err == nil || report.Session == "" || !strings.Contains(err.Error(), report.Session) || !strings.Contains(err.Error(), "still running") {
		t.Fatalf("report = %+v, error = %v", report, err)
	}
	if len(fake.deleted) != 0 {
		t.Errorf("session deleted: %v", fake.deleted)
	}
	for _, term := range fake.terminals {
		if len(term.cues) > 0 {
			t.Errorf("briefed without a record: %+v", term)
		}
	}
}

func TestAnInterruptedRehearsalKeepsItsSessionAndStandingRecord(t *testing.T) {
	fake, client := newFakeCAO(t, nil)
	a := rehearsal(t, client, t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fake.onCue = cancel
	report, err := a.Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	records, err := ListStanding(a.StateDir)
	if err != nil || len(records) != 1 || records[0].Session != report.Session {
		t.Fatalf("standing record after interruption = %+v, %v", records, err)
	}
	if len(fake.deleted) != 0 {
		t.Errorf("deleted session on interruption: %v", fake.deleted)
	}
}
