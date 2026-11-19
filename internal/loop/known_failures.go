package loop

import (
	"fmt"
	"os"
	"slices"
	"strings"
)

const VerifyStepsEnv = "KAPELLMEISTER_VERIFY_STEPS"

type KnownFailure struct {
	Label  string `json:"label"`
	Passed bool   `json:"passed"`
}

func ReadKnownFailures(steps string) []KnownFailure {
	data, err := os.ReadFile(steps + ".known")
	if err != nil {
		return nil
	}
	var known []KnownFailure
	for _, line := range strings.Split(string(data), "\n") {
		outcome, label, ok := strings.Cut(line, " ")
		if ok && (outcome == "passed" || outcome == "failed") {
			known = append(known, KnownFailure{Label: label, Passed: outcome == "passed"})
		}
	}
	return known
}

func knownFailureBaseline(listed []string, recorded []KnownFailure) (failing, passing []string) {
	for _, label := range listed {
		ran, failed := false, false
		for _, known := range recorded {
			if known.Label == label {
				ran, failed = true, failed || !known.Passed
			}
		}
		if ran && !failed {
			passing = append(passing, label)
		} else {
			failing = append(failing, label)
		}
	}
	return failing, passing
}

func KnownFailureMismatch(listed []string, recorded []KnownFailure) string {
	var reasons []string
	var unlisted []string
	for _, known := range recorded {
		if !slices.Contains(listed, known.Label) && !slices.Contains(unlisted, known.Label) {
			unlisted = append(unlisted, known.Label)
			reasons = append(reasons, fmt.Sprintf("known_failure step %q is not listed in [test] verify_known_failures as read when this run started: only the person sets that list, so make it a run step or ask them to list it", known.Label))
		}
	}
	for _, label := range listed {
		if !slices.ContainsFunc(recorded, func(known KnownFailure) bool { return known.Label == label }) {
			reasons = append(reasons, fmt.Sprintf("known failure %q is listed in [test] verify_known_failures but its step did not run: give it its own step in scripts/verify: known_failure '%s' 'COMMAND', or have the person remove it from the list; a scripts/verify written before km had known_failure must add that function from the wrapper km init writes", label, label))
		}
	}
	return strings.Join(reasons, "; ")
}
