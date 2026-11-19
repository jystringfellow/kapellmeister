package loop

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

const measurementsHeading = "Measurements for the tester:"

const measurementRule = "A request only for more measurements, benchmarks or repeated runs, naming no code or test change,"

type measurementSection struct {
	found    bool
	requests []string
	rest     string
}

func measurementHeading(line string) (inline string, ok bool) {
	name := strings.TrimSuffix(measurementsHeading, ":")
	text := strings.TrimLeft(strings.TrimSpace(line), "#* ")
	if len(text) < len(name) || !strings.EqualFold(text[:len(name)], name) {
		return "", false
	}
	after := strings.TrimLeft(text[len(name):], "* ")
	if after == "" {
		return "", true
	}
	if !strings.HasPrefix(after, ":") {
		return "", false
	}
	return strings.TrimSpace(strings.TrimLeft(after[1:], "* ")), true
}

var measurementMarker = regexp.MustCompile(`^(?:[-*+]|\d+[.)])\s+(\S.*)$`)

func measurementItem(line string) (string, bool) {
	match := measurementMarker.FindStringSubmatch(line)
	if match == nil {
		return "", false
	}
	return strings.TrimSpace(match[1]), true
}

func readMeasurements(review string) measurementSection {
	var section measurementSection
	var rest []string
	state, declined := "before", false
	add := func(request string) string {
		if strings.EqualFold(strings.TrimRight(request, "."), "none") {
			declined = true
			return "after"
		}
		section.requests = append(section.requests, request)
		return "list"
	}
	for _, line := range strings.SplitAfter(review, "\n") {
		trimmed := strings.TrimSpace(line)
		switch state {
		case "before":
			inline, ok := measurementHeading(line)
			if !ok {
				rest = append(rest, line)
				continue
			}
			section.found = true
			state = "heading"
			if inline != "" {
				state = add(inline)
			}
		case "heading", "list":
			item, listed := measurementItem(trimmed)
			switch {
			case trimmed == "" && state == "heading":
			case trimmed == "":
				state = "after"
			case listed:
				state = add(item)
			case state == "heading" && strings.EqualFold(strings.TrimRight(trimmed, "."), "none"):
				declined, state = true, "after"
			default:
				rest = append(rest, line)
				state = "after"
			}
		default:
			rest = append(rest, line)
		}
	}
	section.rest = strings.Join(rest, "")
	if declined && len(section.requests) == 0 {
		section.found = false
	}
	return section
}

func measurementRequests(review string) []string {
	return readMeasurements(review).requests
}

func addMeasurementRequests(known, requests []string) []string {
	for _, request := range requests {
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

func (e *Engine) readReview(round int, path string) (handed string, ok bool) {
	data, err := os.ReadFile(filepath.Join(e.Config.Worktree, path))
	if err != nil {
		return "", false
	}
	section := readMeasurements(string(data))
	e.measurements = addMeasurementRequests(e.measurements, section.requests)
	if section.found && len(section.requests) == 0 {
		detail := path + " has the heading `" + measurementsHeading + "` with no requests under it"
		e.progress("  %s", detail)
		e.record(Record{RunID: e.Config.RunID, Round: round, Step: "measurements-empty", Detail: detail})
	}
	if section.rest == string(data) {
		return path, true
	}
	required := filepath.Join(filepath.Dir(path), "required-"+strings.TrimPrefix(filepath.Base(path), "review-"))
	if err := os.WriteFile(filepath.Join(e.Config.Worktree, required), []byte(section.rest), 0o644); err != nil {
		return path, true
	}
	return required, true
}

func (e *Engine) reportUnrunMeasurements(res *Result) {
	unrun := e.measurements[min(e.measured, len(e.measurements)):]
	if len(unrun) == 0 {
		return
	}
	why := "the run ended before a test step ran them"
	if e.Config.Chairs.Tester == nil {
		why = "no tester is seated"
	}
	res.MeasurementsNotRun = unrun
	detail := "measurements not run: " + why + ": " + strings.Join(unrun, "; ")
	e.progress("  %s", detail)
	e.record(Record{RunID: e.Config.RunID, Round: res.Rounds, Step: "measurements-not-run", Detail: detail})
}
