package tune

import (
	"strings"
	"testing"
)

func TestTheTasksProcedureIsTheCarriedGuideText(t *testing.T) {
	task := Task("checkout", "hints", "scripts/verify", "/breaks", Requirements{})
	if strings.Count(task, VerifyProcedure) != 1 {
		t.Fatal(task)
	}
	for _, want := range []string{"1. Read", "CI workflow", "Makefile", "README", "2. Write", "bounded-output wrapper", "left out", "3. Write at least two", "outside the checkout", "source code a test covers", "test's expectation", "4. Run `km prove`", "until it reports proved"} {
		if !strings.Contains(VerifyProcedure, want) {
			t.Fatal(want, VerifyProcedure)
		}
	}
}

var timeBounded = Requirements{TimeBounded: true}.Procedure()

func TestATimeBoundedProcedureKeepsTheTimeTargetAndStableChecksIncludingCleanInstallationCost(t *testing.T) {
	for _, want := range []string{"about two minutes", "never more than five", "measured time", "fast checks CI runs", "unit tests for the code most often changed", "fast marked subset", "by hand", "flaky", "narrower", "lockfile", "cache", "dependency installation"} {
		if !strings.Contains(timeBounded, want) {
			t.Errorf("missing %q", want)
		}
	}
}

func TestATimeBoundedProcedureRunsTheWholeSuiteWhenItFitsAndNarrowsOnlyWhenItDoesNot(t *testing.T) {
	for _, want := range []string{"When the whole suite fits that target, the verify runs the whole suite", "narrow it only when it does not fit"} {
		if !strings.Contains(timeBounded, want) {
			t.Errorf("missing %q", want)
		}
	}
}

func TestATimeBoundedProcedureAllowsAnOmissionOnlyForThreeObservedReasonsEachWithEvidence(t *testing.T) {
	for _, want := range []string{
		"Leave a test out only for an observed reason:",
		"failed or hung in a run made during this tune", "which run", "what it printed",
		"needs something the clean copy cannot have", "network", "a service", "a credential", "another machine",
		"the suite does not fit the time target and this is the part cut (say the measured time)",
		"Each omission says which of those reasons applies, with its evidence", "how to run it by hand",
	} {
		if !strings.Contains(timeBounded, want) {
			t.Errorf("missing %q", want)
		}
	}
}

func TestTheProcedureRefusesSuspicionAloneAsAReasonToLeaveATestOut(t *testing.T) {
	if !strings.Contains(VerifyProcedure, "A test that looks timing-dependent is not a reason.") {
		t.Error("suspicion alone must not justify omitting a test")
	}
	for _, unwanted := range []string{"tests depending on timing", "anything seen to fail once", "not the whole suite"} {
		if strings.Contains(VerifyProcedure, unwanted) {
			t.Errorf("contains %q", unwanted)
		}
	}
}

func TestTheProcedureStaysShortEnoughToRead(t *testing.T) {
	for _, procedure := range []string{VerifyProcedure, timeBounded} {
		if len(procedure) >= 2200 {
			t.Fatalf("procedure is %d bytes; keep it below 2200", len(procedure))
		}
	}
}

func TestByDefaultTheProcedureMirrorsCIWithNoTimeTargetAndNoTimeReasonToLeaveATestOut(t *testing.T) {
	procedure := Requirements{}.Procedure()
	if procedure != VerifyProcedure {
		t.Fatal(procedure)
	}
	for _, unwanted := range []string{"two minutes", "never more than five", "time target", "does not fit", "fast marked subset", "verify_time_bounded"} {
		if strings.Contains(procedure, unwanted) {
			t.Errorf("contains %q", unwanted)
		}
	}
	for _, want := range []string{"mirrors CI and may take as long as CI", "run every suite CI requires", "report the measured time", "Speed is the job of check", "leave nothing out to save time", "failed or hung in a run made during this tune", "or it needs something the clean copy cannot have", "A test that looks timing-dependent is not a reason."} {
		if !strings.Contains(procedure, want) {
			t.Errorf("missing %q", want)
		}
	}
	if rules := (Requirements{}).ReviewRules(); !strings.Contains(rules, "time is not a permitted reason to leave a test out") {
		t.Fatal(rules)
	}
	if why := (Requirements{}).KeepsSlowTests(); why == "" {
		t.Fatal("by default a verify is not narrowed for time")
	}
}

func TestATimeBoundedProcedureCarriesTheTimeTargetAndAllowsCuttingWhatDoesNotFit(t *testing.T) {
	bounded := Requirements{TimeBounded: true}
	procedure := bounded.Procedure()
	for _, want := range []string{"about two minutes on a clean copy and never more than five", "narrow it only when it does not fit", "fast marked subset", "or the suite does not fit the time target and this is the part cut (say the measured time)", "verify_time_bounded = true in [test]"} {
		if !strings.Contains(procedure, want) {
			t.Errorf("missing %q", want)
		}
	}
	if strings.Contains(procedure, "may take as long as CI") {
		t.Error(procedure)
	}
	if rules := bounded.ReviewRules(); !strings.Contains(rules, "does not fit that target may be cut") || !strings.Contains(rules, "when the whole suite would fit the target") {
		t.Fatal(rules)
	}
	if why := bounded.KeepsSlowTests(); why != "" {
		t.Fatal(why)
	}
}

func TestRequiredSuitesAreRunStepsThatAreNeverLeftOutOrNarrowedEvenWhenVerifyIsTimeBounded(t *testing.T) {
	required := Require([]string{"unit"}, []string{"integration", "unit"}, true)
	text := required.Procedure()
	for _, want := range []string{"about two minutes", "Required suites: unit, integration.", "its own `run` step labelled exactly with its name", "list them in [test] verify_required", "Never leave a required suite out or narrow it, for time or any other reason", "km raises the verify timeout", "say so in your reply instead of narrowing it", "\n3. Write at least two"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q", want)
		}
	}
	if rules := required.ReviewRules(); !strings.Contains(rules, "Required suites: unit, integration.") || !strings.Contains(rules, "no reason permits leaving one out") {
		t.Fatal(rules)
	}
	if why := required.KeepsSlowTests(); why != "required suites are never left out for time" {
		t.Fatal(why)
	}
}
