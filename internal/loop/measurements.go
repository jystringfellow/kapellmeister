package loop

import (
	"slices"
	"strings"
)

const measurementsHeading = "Measurements for the tester:"

const measurementRule = "A request only for more measurements, benchmarks or repeated runs, naming no code or test change,"

func measurementRequests(review string) []string {
	var requests []string
	inSection := false
	for _, line := range strings.Split(review, "\n") {
		line = strings.TrimSpace(line)
		if !inSection {
			inSection = strings.Trim(line, "#* ") == measurementsHeading
			continue
		}
		if line == "" || line == "APPROVE" || line == "CHANGES" {
			break
		}
		request := strings.TrimSpace(strings.TrimLeft(line, "-* "))
		if request != "" && !strings.EqualFold(strings.TrimRight(request, "."), "none") {
			requests = append(requests, request)
		}
	}
	return requests
}

func addMeasurementRequests(known []string, review string) []string {
	for _, request := range measurementRequests(review) {
		if !slices.Contains(known, request) {
			known = append(known, request)
		}
	}
	return known
}

func measurementsBlock(requests []string) string {
	if len(requests) == 0 {
		return ""
	}
	return "Reviewers asked for these measurements, which no code change depends on. Run them after the test plan's scenarios, as time allows, and report what you measured. One you could not run is reported as not run and is not by itself a reason for CHANGES.\n- " + strings.Join(requests, "\n- ") + "\n"
}
