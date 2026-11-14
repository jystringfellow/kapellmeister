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

func TestTheProcedureTargetsAFastStableSubsetIncludingCleanInstallationCost(t *testing.T) {
	for _, want := range []string{"about two minutes", "never more than five", "measured time", "fast checks CI runs", "unit tests for the code most often changed", "fast marked subset", "not the whole suite", "by hand", "timing", "network", "services", "wall clock", "fail once", "flaky", "narrower", "lockfile", "cache", "dependency installation"} {
		if !strings.Contains(VerifyProcedure, want) {
			t.Errorf("missing %q", want)
		}
	}
}
