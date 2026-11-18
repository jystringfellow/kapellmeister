package loop

import (
	"bytes"
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
