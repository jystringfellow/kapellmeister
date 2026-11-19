package loop

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const measuringReview = "The change is correct and I approve it as it stands.\n\nMeasurements for the tester:\n- run the import three more times and compare the timings\n- measure memory with a large input\n\nAPPROVE\n"

func TestReviewersAreToldAMeasurementOnlyRequestIsNotARequiredChange(t *testing.T) {
	e, f := testerEngine(t)
	run(t, e)
	p := testerSteps(f, "review")[0].Prompt
	for _, want := range []string{
		"A request only for more measurements, benchmarks or repeated runs, naming no code or test change, is not a required change and is no reason for CHANGES.",
		"Write each such request on its own line under the heading `Measurements for the tester:`",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("review prompt lacks %q: %s", want, p)
		}
	}
}

func TestTheGateIsToldMeasurementOnlyRequestsNeverBlock(t *testing.T) {
	e, f := testerEngine(t)
	run(t, e)
	p := testerSteps(f, "gate")[0].Prompt
	if want := "A request only for more measurements, benchmarks or repeated runs, naming no code or test change, is never blocking."; !strings.Contains(p, want) {
		t.Errorf("gate prompt lacks %q: %s", want, p)
	}
}

func TestAReviewsMeasurementRequestsReachTheTesterWithoutAnotherImplementRound(t *testing.T) {
	e, f := testerEngine(t)
	f.actions["review"] = func(s Step) { f.write(s.ReplyPath, measuringReview) }
	res := run(t, e)
	if res.Outcome != Approved || res.Rounds != 1 {
		t.Fatal(res)
	}
	if n := len(testerSteps(f, "implement")); n != 1 {
		t.Fatalf("implement steps = %d: %v", n, f.names())
	}
	p := testerSteps(f, "test")[0].Prompt
	for _, want := range []string{"Reviewers asked for these measurements", "- run the import three more times and compare the timings", "- measure memory with a large input"} {
		if !strings.Contains(p, want) {
			t.Errorf("test prompt lacks %q: %s", want, p)
		}
	}
	for _, forbidden := range []string{"review-1", "I approve it as it stands"} {
		if strings.Contains(p, forbidden) {
			t.Errorf("test prompt shows the tester the review: %q", forbidden)
		}
	}
	if len(res.MeasurementsNotRun) != 0 {
		t.Errorf("measurements reported as not run with a tester seated: %v", res.MeasurementsNotRun)
	}
}

func TestTheTestPromptSaysNothingAboutMeasurementsWhenNoReviewAskedForAny(t *testing.T) {
	e, f := testerEngine(t)
	run(t, e)
	if p := testerSteps(f, "test")[0].Prompt; strings.Contains(p, "measurements") {
		t.Error(p)
	}
}

func TestWithoutATesterAnApprovedRunSaysWhichMeasurementRequestsWereNotRun(t *testing.T) {
	e, f, records := newEngine(t, passing)
	var progress bytes.Buffer
	e.Progress = &progress
	f.actions["review"] = func(s Step) { f.write(s.ReplyPath, measuringReview) }
	res := run(t, e)
	if res.Outcome != Approved || res.Rounds != 1 {
		t.Fatal(res)
	}
	want := []string{"run the import three more times and compare the timings", "measure memory with a large input"}
	equal(t, res.MeasurementsNotRun, want)
	for _, text := range []string{progress.String(), records.String()} {
		if !strings.Contains(text, "measurements not run: no tester is seated") || !strings.Contains(text, want[0]) || !strings.Contains(text, want[1]) {
			t.Errorf("output does not list the measurements that were not run: %s", text)
		}
	}
}

func TestAMeasurementRequestReachesTheTestPromptVerbatimEvenWithAPercentSign(t *testing.T) {
	p := testPrompt("task", "plan", "test-plan", "summary", "", VerifyResult{}, "", "scratch", "out", 0, 1, "", "url", "state", []string{"confirm 100%s of rows load"})
	if !strings.Contains(p, "- confirm 100%s of rows load\n") || strings.Contains(p, "MISSING") {
		t.Error(p)
	}
}

func TestMeasurementRequestsAreTheLinesUnderTheHeadingUpToTheFirstBlankLine(t *testing.T) {
	equal(t, measurementRequests(measuringReview), []string{"run the import three more times and compare the timings", "measure memory with a large input"})
	equal(t, measurementRequests("Required changes:\n- fix a.go:3\n\nCHANGES\n"), nil)
	equal(t, measurementRequests("Measurements for the tester:\n* time it twice\nAPPROVE\n"), []string{"time it twice"})
	equal(t, measurementRequests("Measurements for the tester:\nnone\n\nAPPROVE\n"), nil)
}

func TestMeasurementRequestsAreReadAfterABlankLineWithoutTheColonAndFromTheHeadingsOwnLine(t *testing.T) {
	equal(t, measurementRequests("Looks right.\n\nMeasurements for the tester:\n\n- time it twice\n- measure memory\n\nAPPROVE\n"), []string{"time it twice", "measure memory"})
	equal(t, measurementRequests("## Measurements for the tester\n- time it twice\n\nAPPROVE\n"), []string{"time it twice"})
	equal(t, measurementRequests("**Measurements for the tester:** time it twice\n\nAPPROVE\n"), []string{"time it twice"})
	equal(t, measurementRequests("Measurements for the tester: time it twice\n- measure memory\n\nAPPROVE\n"), []string{"time it twice", "measure memory"})
	equal(t, measurementRequests("Measurements for the tester are not needed here.\n- fix a.go:3\n\nCHANGES\n"), nil)
}

func TestARequiredChangeAfterAMeasurementsHeadingStaysInTheImplementersCopy(t *testing.T) {
	for name, c := range map[string]struct {
		review   string
		requests []string
		rest     string
	}{
		"an empty heading followed by another section": {
			"Measurements for the tester:\n\n## Required changes\n- fix a.go:3\n\nCHANGES\n", nil,
			"## Required changes\n- fix a.go:3\n\nCHANGES\n",
		},
		"a section that starts with no blank line between": {
			"Measurements for the tester:\n- rerun the benchmark\nRequired changes:\n- fix a.go:3\n\nCHANGES\n", []string{"rerun the benchmark"},
			"Required changes:\n- fix a.go:3\n\nCHANGES\n",
		},
		"none on the heading's line followed directly by changes": {
			"Measurements for the tester: none\n- fix a.go:3\n\nCHANGES\n", nil,
			"- fix a.go:3\n\nCHANGES\n",
		},
		"none as the only item followed directly by changes": {
			"Measurements for the tester:\n- None.\n- fix a.go:3\n\nCHANGES\n", nil,
			"- fix a.go:3\n\nCHANGES\n",
		},
		"a line that is not a list item": {
			"Measurements for the tester:\nthe parser must also accept empty input\n\nCHANGES\n", nil,
			"the parser must also accept empty input\n\nCHANGES\n",
		},
		"a bold heading after the list": {
			"Measurements for the tester:\n* time it twice\n**Required changes**\n* fix a.go:3\n\nCHANGES\n", []string{"time it twice"},
			"**Required changes**\n* fix a.go:3\n\nCHANGES\n",
		},
	} {
		section := readMeasurements(c.review)
		if section.rest != c.rest {
			t.Errorf("%s: implementer's copy = %q, want %q", name, section.rest, c.rest)
		}
		if len(section.requests) != len(c.requests) || len(c.requests) > 0 && section.requests[0] != c.requests[0] {
			t.Errorf("%s: requests = %q, want %q", name, section.requests, c.requests)
		}
	}
}

func TestAMeasurementsHeadingThatYieldsNoRequestsIsReportedByItsReviewFile(t *testing.T) {
	e, f, records := newEngine(t, passing)
	var progress bytes.Buffer
	e.Progress = &progress
	f.actions["review"] = func(s Step) {
		f.write(s.ReplyPath, "Fine as it stands.\n\nMeasurements for the tester:\n\nAPPROVE\n")
	}
	res := run(t, e)
	if res.Outcome != Approved || len(res.MeasurementsNotRun) != 0 {
		t.Fatal(res)
	}
	want := "has the heading `Measurements for the tester:` with no requests under it"
	for _, text := range []string{progress.String(), records.String()} {
		if !strings.Contains(text, want) || !strings.Contains(text, "review-1.md") {
			t.Errorf("output does not report the empty heading: %s", text)
		}
	}
}

func TestAnExplicitNoneUnderTheMeasurementsHeadingIsNotReported(t *testing.T) {
	e, f, records := newEngine(t, passing)
	f.actions["review"] = func(s Step) {
		f.write(s.ReplyPath, "Fine.\n\nMeasurements for the tester:\nnone\n\nAPPROVE\n")
	}
	run(t, e)
	if strings.Contains(records.String(), "no requests under it") {
		t.Error(records.String())
	}
}

const changesWithMeasurements = "Required changes:\n- fix a.go:3\n\nMeasurements for the tester:\n- time it twice\n\nNothing else.\n\nCHANGES\n"

func TestTheImplementerIsHandedAReviewWithoutItsMeasurementRequestsAndTheReviewFileStaysWhole(t *testing.T) {
	e, f, _ := newEngine(t, passing)
	f.actions["review"] = func(s Step) {
		if s.Round == 1 {
			f.write(s.ReplyPath, changesWithMeasurements)
			return
		}
		f.write(s.ReplyPath, "Fixed.\n\nAPPROVE\n")
	}
	res := run(t, e)
	if res.Outcome != Approved || res.Rounds != 2 {
		t.Fatal(res)
	}
	p := testerSteps(f, "implement")[1].Prompt
	review, handed := e.rel("review-1.md"), e.rel("required-1.md")
	if strings.Contains(p, review) || !strings.Contains(p, handed) {
		t.Fatalf("implement prompt names the whole review, not the copy without measurement requests: %s", p)
	}
	copied, err := os.ReadFile(filepath.Join(e.Config.Worktree, handed))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(copied), "km moved 1 measurement request from this review to the tester; the full review is "+review+".\n\nRequired changes:\n- fix a.go:3\n\nNothing else.\n\nCHANGES\n"; got != want {
		t.Errorf("copy for the implementer = %q, want %q", got, want)
	}
	whole, _ := os.ReadFile(filepath.Join(e.Config.Worktree, review))
	if string(whole) != changesWithMeasurements {
		t.Errorf("review file changed: %q", whole)
	}
}

func TestAReviewWithoutMeasurementRequestsIsHandedToTheImplementerAsItIs(t *testing.T) {
	e, f, _ := newEngine(t, passing)
	f.verdicts["review"] = []string{"CHANGES"}
	run(t, e)
	if p := testerSteps(f, "implement")[1].Prompt; !strings.Contains(p, e.rel("review-1.md")) || strings.Contains(p, "required-1") {
		t.Error(p)
	}
}

func TestARunThatEndsWithoutApprovalSaysWhichMeasurementRequestsWereNotRun(t *testing.T) {
	e, f := testerEngine(t)
	var progress, records bytes.Buffer
	e.Progress, e.Records = &progress, &records
	e.Config.MaxRounds = 1
	f.actions["review"] = func(s Step) { f.write(s.ReplyPath, changesWithMeasurements) }
	res := run(t, e)
	if res.Outcome != RoundsExhausted {
		t.Fatal(res)
	}
	equal(t, res.MeasurementsNotRun, []string{"time it twice"})
	for _, text := range []string{progress.String(), records.String()} {
		if !strings.Contains(text, "measurements not run: the run ended before a test step ran them: time it twice") {
			t.Errorf("output does not list the measurements that were not run: %s", text)
		}
	}
}

func TestMeasurementRequestsHandedToATestStepThatFinishedAreNotReportedAsNotRun(t *testing.T) {
	e, f := testerEngine(t)
	e.Config.MaxRounds = 1
	f.verdicts["test"] = []string{"CHANGES"}
	f.actions["review"] = func(s Step) { f.write(s.ReplyPath, measuringReview) }
	res := run(t, e)
	if res.Outcome != RoundsExhausted || len(res.MeasurementsNotRun) != 0 {
		t.Fatal(res)
	}
}

func TestAWrappedMeasurementRequestIsOneRequestAndDoesNotEndTheSection(t *testing.T) {
	section := readMeasurements("Measurements for the tester:\n- run the import three more times\n  and compare the timings\n- measure memory\n\nRequired changes:\n- fix a.go:3\n")
	if want := []string{"run the import three more times and compare the timings", "measure memory"}; strings.Join(section.requests, "|") != strings.Join(want, "|") {
		t.Fatalf("%q", section.requests)
	}
	if section.rest != "Required changes:\n- fix a.go:3\n" {
		t.Fatalf("%q", section.rest)
	}
}

func TestAFencedCommandUnderAMeasurementRequestStaysWithItAndDoesNotEndTheSection(t *testing.T) {
	const fence = "```"
	section := readMeasurements("Measurements for the tester:\n- run the benchmark:\n  " + fence + "sh\n  go test -bench . -count 5\n  " + fence + "\n- measure memory\n\nCHANGES\n")
	if len(section.requests) != 2 || section.requests[0] != "run the benchmark:\n  go test -bench . -count 5" || section.requests[1] != "measure memory" {
		t.Fatalf("%q", section.requests)
	}
	if section.rest != "CHANGES\n" {
		t.Fatalf("%q", section.rest)
	}
}

func TestAFenceThatNeverClosesStaysInTheImplementersCopy(t *testing.T) {
	const fence = "```"
	section := readMeasurements("Measurements for the tester:\n- run the benchmark:\n" + fence + "\ngo test -bench .\nRequired changes:\n- fix a.go:3\n")
	if len(section.requests) != 1 || section.requests[0] != "run the benchmark:" {
		t.Fatalf("%q", section.requests)
	}
	if section.rest != fence+"\ngo test -bench .\nRequired changes:\n- fix a.go:3\n" {
		t.Fatalf("%q", section.rest)
	}
}

func TestAnUnindentedLineAfterARequestStillEndsTheSection(t *testing.T) {
	section := readMeasurements("Measurements for the tester:\n- rerun the benchmark\nRequired changes:\n- fix a.go:3\n")
	if len(section.requests) != 1 || section.rest != "Required changes:\n- fix a.go:3\n" {
		t.Fatalf("%q %q", section.requests, section.rest)
	}
}
