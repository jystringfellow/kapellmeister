package loop

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

func scoreChair(part string) Chair {
	return Chair{Provider: "p", Profile: part}
}

func scoreEngine(t *testing.T) (*Engine, *fakeStepper, *bytes.Buffer) {
	t.Helper()
	e, f, records := newEngine(t, passing)
	tester := scoreChair("tester")
	e.Config.Chairs = Chairs{
		Planner: scoreChair("planner"), Implementer: scoreChair("implementer"),
		Reviewers: []Chair{scoreChair("reviewer-a"), scoreChair("reviewer-b")},
		Gate:      scoreChair("gate"), Classifier: scoreChair("classifier"), Tester: &tester,
	}
	e.Config.CheckCommand = "go test {args}"
	f.actions["implement"] = func(s Step) {
		f.write("change.txt", fmt.Sprintf("round %d\n", s.Round))
		f.write(s.ReplyPath, "implemented\n")
	}
	f.actions["review"] = func(s Step) {
		verdict := "APPROVE"
		if s.Round == 1 && s.Chair.Profile == "reviewer-a" {
			verdict = "CHANGES"
		}
		f.write(s.ReplyPath, "findings\n"+verdict+"\n")
	}
	f.actions["gate"] = func(s Step) {
		if s.Round == 1 {
			f.write(s.ReplyPath, "the gate did not end with a verdict\n")
			return
		}
		f.write(s.ReplyPath, "all good\nAPPROVE\n")
	}
	f.verdicts["gate"] = []string{"CHANGES"}
	f.actions["test"] = func(s Step) { f.write(s.ReplyPath, "scenarios passed\nAPPROVE\n") }
	return e, f, records
}

func scorePlaces(e *Engine, text string) string {
	worktree, _ := filepath.EvalSymlinks(e.Config.Worktree)
	for _, place := range []struct{ path, name string }{{e.Config.ScratchDir, "<scratch>"}, {e.Config.Worktree, "<worktree>"}, {worktree, "<worktree>"}} {
		text = strings.ReplaceAll(text, place.path, place.name)
	}
	return strings.ReplaceAll(text, fmt.Sprintf("The process %d is", os.Getpid()), "The process <pid> is")
}

func scoreSteps(f *fakeStepper) []Step {
	f.mu.Lock()
	steps := append([]Step(nil), f.steps...)
	f.mu.Unlock()
	rank := func(s Step) int {
		switch s.Name {
		case "plan":
			return 0
		case "test-plan":
			return 1
		}
		return 2
	}
	sort.SliceStable(steps, func(i, j int) bool {
		a, b := steps[i], steps[j]
		if rank(a) != rank(b) || rank(a) < 2 {
			return rank(a) < rank(b)
		}
		return a.Name == "review" && b.Name == "review" && a.Round == b.Round && a.Chair.Profile < b.Chair.Profile
	})
	return steps
}

func scoreGolden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if os.Getenv("KM_UPDATE_GOLDEN") != "" {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Fatalf("%s differs from what the default score does now; run with KM_UPDATE_GOLDEN=1 only when the change is meant\n%s", path, got)
	}
}

func TestTheDefaultScorePlaysItsStepsInThisOrderWithTheseChairsAndReplyFiles(t *testing.T) {
	e, f, _ := scoreEngine(t)
	res := run(t, e)
	if res.Outcome != Approved || res.Rounds != 2 {
		t.Fatalf("got %+v", res)
	}
	var out strings.Builder
	for _, s := range scoreSteps(f) {
		terminal := "fresh terminal"
		switch {
		case s.ReuseTerminal != "":
			terminal = "same terminal as before"
		case s.KeepTerminal:
			terminal = "terminal kept for later"
		}
		fmt.Fprintf(&out, "%-22s %-12s %-12s %s; %s\n", label(s.Name, s.Round), s.ChairName, s.Chair.Profile, scorePlaces(e, s.ReplyPath), terminal)
	}
	scoreGolden(t, "default_score_steps.golden", out.String())
}

func TestEachPartOfTheDefaultScoreReceivesThisPrompt(t *testing.T) {
	e, f, _ := scoreEngine(t)
	e.Config.Lessons, e.Config.LessonsTimeout = true, time.Minute
	f.actions["lessons"] = func(s Step) { f.write(s.ReplyPath, "[]") }
	run(t, e)
	var out strings.Builder
	for _, s := range scoreSteps(f) {
		fmt.Fprintf(&out, "=== %s %s\n%s\n", label(s.Name, s.Round), s.Chair.Profile, scorePlaces(e, s.Prompt))
	}
	scoreGolden(t, "default_score_prompts.golden", out.String())
}

func TestTheDefaultScoreLeavesTheseStateFilesAndRecords(t *testing.T) {
	e, _, records := scoreEngine(t)
	res := run(t, e)
	var out strings.Builder
	entries, err := os.ReadDir(res.RunDir)
	if err != nil {
		t.Fatal(err)
	}
	out.WriteString("files:\n")
	for _, entry := range entries {
		fmt.Fprintf(&out, "  %s\n", entry.Name())
	}
	var lines []string
	for _, line := range strings.Split(strings.TrimSpace(records.String()), "\n") {
		var rec struct {
			Step, Verdict, Reply string
			Round                int
			ChairName            string `json:"chair_name"`
			VerdictSource        string `json:"verdict_source"`
		}
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatal(err, line)
		}
		lines = append(lines, strings.TrimRight(fmt.Sprintf("  round %d %-16s %-12s %-8s %-10s %s", rec.Round, rec.Step, rec.ChairName, rec.Verdict, rec.VerdictSource, rec.Reply), " "))
	}
	sort.Strings(lines)
	out.WriteString("records, sorted:\n" + strings.Join(lines, "\n") + "\n")
	scoreGolden(t, "default_score_state.golden", out.String())
}

func TestTheImplementerKeepsItsTerminalAcrossRoundsAndEveryOtherStepReleasesIts(t *testing.T) {
	e, f, _ := scoreEngine(t)
	run(t, e)
	terminals := map[string]bool{}
	for _, s := range scoreSteps(f) {
		if s.Name != "implement" {
			if s.KeepTerminal || s.ReuseTerminal != "" && !strings.HasSuffix(s.Name, "-nudge") {
				t.Fatalf("%s kept or reused a terminal: %+v", s.Name, s)
			}
			continue
		}
		if !s.KeepTerminal || (s.Round > 1) != (s.ReuseTerminal != "") {
			t.Fatalf("implement[%d]: %+v", s.Round, s)
		}
		terminals[s.ReuseTerminal] = true
	}
	if len(terminals) != 2 {
		t.Fatalf("the implementer played in more than one terminal: %v", terminals)
	}
}

func TestAnImplementerWhoseTierRisesLeavesItsTerminalAndStartsAFreshOne(t *testing.T) {
	e, f, records := tieredEngine(t, SizingOff)
	f.verdicts["review"] = []string{"CHANGES", "CHANGES"}
	res := run(t, e)
	var out strings.Builder
	fmt.Fprintf(&out, "outcome %s after %d rounds\n", res.Outcome, res.Rounds)
	for _, s := range scoreSteps(f) {
		if s.Name != "implement" {
			continue
		}
		terminal := "starts a fresh terminal"
		if s.ReuseTerminal != "" {
			terminal = "plays in terminal " + s.ReuseTerminal
		}
		fmt.Fprintf(&out, "%s: model %s, tier %s (%s), %s, kept for later %t\n", label(s.Name, s.Round), s.Chair.Model, s.Assignment.Tier, s.Assignment.Source, terminal, s.KeepTerminal)
	}
	fmt.Fprintf(&out, "released, in order: %s\n", strings.Join(f.released, " "))
	for _, rec := range recordsOf(t, records, "escalate") {
		fmt.Fprintf(&out, "escalate record: round %d, tier %s, %s\n", rec.Round, rec.Tier, rec.Detail)
	}
	fmt.Fprintf(&out, "=== implement[3] prompt\n%s\n", scorePlaces(e, stepNamed(t, f, "implement", 3).Prompt))
	scoreGolden(t, "default_score_escalation.golden", out.String())
}
