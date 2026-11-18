package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/jystringfellow/kapellmeister/internal/loop"
)

func TestTheFinalSummaryListsMeasurementRequestsThatWereNotRun(t *testing.T) {
	var out bytes.Buffer
	printOutcome(&out, ensembleSummary{Result: loop.Result{Outcome: loop.RoundsExhausted, Rounds: 3, MeasurementsNotRun: []string{"time it twice", "measure memory"}}})
	if want := "measurements not run:\n  - time it twice\n  - measure memory\n"; !strings.Contains(out.String(), want) {
		t.Errorf("summary = %q, want it to contain %q", out.String(), want)
	}
}

func TestTheFinalSummarySaysNothingAboutMeasurementsWhenNoneWereLeftUnrun(t *testing.T) {
	var out bytes.Buffer
	printOutcome(&out, ensembleSummary{Result: loop.Result{Outcome: loop.Approved, Rounds: 1}})
	if strings.Contains(out.String(), "measurements") {
		t.Error(out.String())
	}
}
