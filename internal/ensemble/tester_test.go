package ensemble

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jystringfellow/kapellmeister/internal/loop"
)

const tonmeister = `
[sections.leads.chairs.tonmeister]
part = "tester"
agents = ["claude"]
`

func TestATesterPartBindsFromAnEnsembleFile(t *testing.T) {
	home, wt := repoEnsemble(t, "thorough", thorough+tonmeister)
	members := bind(t, find(t, "thorough", home, wt), everyAgent)
	chairs, err := ChairsOf(members)
	if err != nil {
		t.Fatal(err)
	}
	if chairs.Tester == nil || chairs.Tester.Profile != "km-claude" || len(chairs.Reviewers) != 2 {
		t.Fatalf("chairs: %+v", chairs)
	}
}

func TestTwoTestersAreAnErrorNamingBoth(t *testing.T) {
	ms := standingScore().Members()
	ms = append(ms,
		Member{Section: "a", Name: "x", Part: "tester"},
		Member{Section: "b", Name: "y", Part: "tester"})
	_, err := ChairsOf(ms)
	if err == nil || err.Error() != "ensemble seats 2 testers (a.x, b.y); the score has one" {
		t.Fatal(err)
	}
}

func TestChairsOfLeavesTheTesterNilWhenNoneIsSeated(t *testing.T) {
	chairs, err := ChairsOf(standingScore().Members())
	if err != nil || chairs.Tester != nil {
		t.Fatalf("chairs = %+v, err = %v", chairs, err)
	}
}

func TestMembersOfSeatsTheTesterOnlyWhenOneIsSet(t *testing.T) {
	chairs, err := ChairsOf(standingScore().Members())
	if err != nil {
		t.Fatal(err)
	}
	before := MembersOf(chairs)
	chair := loop.Chair{Provider: "claude_code", Profile: "km-tonmeister"}
	chairs.Tester = &chair
	after := MembersOf(chairs)
	if len(after) != len(before)+1 {
		t.Fatalf("members = %+v", after)
	}
	want := Member{Section: "tester", Name: "tester", Part: "tester", Chair: chair}
	if after[len(after)-1] != want {
		t.Fatalf("tester = %+v", after[len(after)-1])
	}
	rebuilt, err := ChairsOf(after)
	if err != nil || rebuilt.Tester == nil || *rebuilt.Tester != chair {
		t.Fatalf("rebuilt = %+v, %v", rebuilt, err)
	}
}

func TestAStandingRecordWithATesterRebuildsIt(t *testing.T) {
	var standing Standing
	for _, m := range append(standingScore().Members(), Member{Section: "leads", Name: "tonmeister", Part: "tester", Chair: loop.Chair{Provider: "claude_code", Profile: "km-tonmeister"}}) {
		standing.Chairs = append(standing.Chairs, StandingChair{
			Address: m.Address("thorough"), Section: m.Section, Name: m.Name, Part: m.Part, Binding: m.Chair,
		})
	}
	dir := t.TempDir()
	standing.Session = "cao-tester"
	if err := standing.Save(dir); err != nil {
		t.Fatal(err)
	}
	records, err := ListStanding(dir)
	if err != nil || len(records) != 1 {
		t.Fatalf("records = %v, %v", records, err)
	}
	chairs, err := records[0].RebuildChairs()
	if err != nil || chairs.Tester == nil || chairs.Tester.Address != "leads.tonmeister@thorough" {
		t.Fatalf("chairs = %+v, %v", chairs, err)
	}
}

func TestAuditionAndRehearsalSeatAndCueATesterChair(t *testing.T) {
	for _, rehearse := range []bool{false, true} {
		t.Run(map[bool]string{false: "audition", true: "rehearsal"}[rehearse], func(t *testing.T) {
			fake, client := newFakeCAO(t, nil)
			chairs, err := ChairsOf(standingScore().Members())
			if err != nil {
				t.Fatal(err)
			}
			chairs.Tester = &loop.Chair{Provider: "claude_code", Profile: "km-tonmeister"}
			ms := MembersOf(chairs)
			wt := t.TempDir()
			var report Report
			if rehearse {
				a := rehearsal(t, client, wt)
				a.Members = ms
				report, err = a.Run(context.Background())
			} else {
				a := Audition{Client: client, Ensemble: "thorough", Worktree: wt, Members: ms, KM: "/opt/km", Timeout: 2 * time.Second}
				report, err = a.Run(context.Background())
			}
			if err != nil || !report.OK || len(report.Chairs) != len(ms) {
				t.Fatalf("report = %+v, %v", report, err)
			}
			term := fake.byProfile("km-tonmeister")
			if term == nil || len(term.cues) != 1 || !slices.Equal(term.group, []string{"thorough", "tester"}) || !strings.Contains(term.cues[0], "tester.tester@thorough") {
				t.Fatalf("tester terminal = %+v", term)
			}
			if filepath.Dir(filepath.Dir(report.Queue)) != filepath.Join(wt, ".kapellmeister", "runs") {
				t.Fatal(report.Queue)
			}
		})
	}
}
