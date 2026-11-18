package loop

import (
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
	for _, known := range recorded {
		if known.Passed && !slices.Contains(passing, known.Label) {
			passing = append(passing, known.Label)
		}
	}
	for _, label := range listed {
		if !slices.Contains(passing, label) {
			failing = append(failing, label)
		}
	}
	return failing, passing
}
