package tune

import (
	"strings"
	"testing"
)

func TestTheTasksProcedureIsTheCarriedGuideText(t *testing.T) {
	task := Task("checkout", "hints", "scripts/verify", "/breaks")
	if strings.Count(task, VerifyProcedure) != 1 {
		t.Fatal(task)
	}
	for _, want := range []string{"1. Read", "CI workflow", "Makefile", "README", "2. Write", "bounded-output wrapper", "left out", "3. Write at least two", "outside the checkout", "source code a test covers", "test's expectation", "4. Run `km prove`", "until it reports proved"} {
		if !strings.Contains(VerifyProcedure, want) {
			t.Fatal(want, VerifyProcedure)
		}
	}
}

func TestTheProcedureKeepsTheTimeTargetAndStableChecksIncludingCleanInstallationCost(t *testing.T) {
	for _, want := range []string{"about two minutes", "never more than five", "measured time", "fast checks CI runs", "unit tests for the code most often changed", "fast marked subset", "by hand", "flaky", "narrower", "lockfile", "cache", "dependency installation"} {
		if !strings.Contains(VerifyProcedure, want) {
			t.Errorf("missing %q", want)
		}
	}
}

func TestTheProcedureRunsTheWholeSuiteWhenItFitsAndNarrowsOnlyWhenItDoesNot(t *testing.T) {
	for _, want := range []string{"When the whole suite fits that target, the verify runs the whole suite", "narrow it only when it does not fit"} {
		if !strings.Contains(VerifyProcedure, want) {
			t.Errorf("missing %q", want)
		}
	}
}

func TestTheProcedureAllowsAnOmissionOnlyForThreeObservedReasonsEachWithEvidence(t *testing.T) {
	for _, want := range []string{
		"Leave a test out only for an observed reason:",
		"failed or hung in a run made during this tune", "which run", "what it printed",
		"needs something the clean copy cannot have", "network", "a service", "a credential", "another machine",
		"the suite does not fit the time target and this is the part cut (say the measured time)",
		"Each omission says which of those reasons applies, with its evidence", "how to run it by hand",
	} {
		if !strings.Contains(VerifyProcedure, want) {
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
	if len(VerifyProcedure) >= 2200 {
		t.Fatalf("procedure is %d bytes; keep it below 2200", len(VerifyProcedure))
	}
}
