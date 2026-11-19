package loop

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func knownVerifier(runs ...[]KnownFailure) Verifier {
	call := 0
	return func(context.Context, string) (VerifyResult, error) {
		known := runs[min(call, len(runs)-1)]
		call++
		return VerifyResult{Output: "all steps passed\n", Known: known}, nil
	}
}

func TestARoundsVerifyFailsWhenAKnownFailureStepIsNotInTheListReadAtRunStart(t *testing.T) {
	listed := []KnownFailure{{Label: "legacy"}}
	e, _, _ := newEngine(t, knownVerifier(listed, append([]KnownFailure{{Label: "hidden"}}, listed...)))
	e.Config.KnownFailures = []string{"legacy"}
	res := run(t, e)
	if res.Outcome != RoundsExhausted {
		t.Fatalf("got %+v", res)
	}
	const want = `known_failure step "hidden" is not listed in [test] verify_known_failures as read when this run started`
	for _, round := range []string{"verify-1.txt", "verify-2.txt"} {
		if out := readRel(t, e, e.rel(round)); !strings.Contains(out, "verify refused: "+want) {
			t.Fatal(round, out)
		}
	}
	if strings.Join(res.KnownFailures, ",") != "legacy" {
		t.Fatal(res.KnownFailures)
	}
}

func TestARoundsVerifyFailsWhenAListedKnownFailureStepStopsRunning(t *testing.T) {
	e, _, _ := newEngine(t, knownVerifier([]KnownFailure{{Label: "legacy"}}, nil))
	e.Config.KnownFailures = []string{"legacy"}
	res := run(t, e)
	if res.Outcome != RoundsExhausted {
		t.Fatalf("got %+v", res)
	}
	if out := readRel(t, e, e.rel("verify-1.txt")); !strings.Contains(out, `known failure "legacy" is listed in [test] verify_known_failures but its step did not run`) {
		t.Fatal(out)
	}
}

func TestABaselineThatDoesNotMatchTheKnownFailureListEndsTheRunBeforeAnyChairPlays(t *testing.T) {
	e, f, _ := newEngine(t, knownVerifier(nil))
	e.Config.KnownFailures = []string{"legacy"}
	res := run(t, e)
	if res.Outcome != BaselineMismatch || len(f.steps) != 0 || len(res.KnownFailures) != 0 || !strings.Contains(res.Reason, "its step did not run") {
		t.Fatalf("got %+v after %v", res, f.steps)
	}
}

func TestAKnownFailureRecordedOnceFailedAndOncePassedIsFailing(t *testing.T) {
	for name, recorded := range map[string][]KnownFailure{
		"failed first": {{Label: "legacy"}, {Label: "legacy", Passed: true}},
		"passed first": {{Label: "legacy", Passed: true}, {Label: "legacy"}},
	} {
		failing, passing := knownFailureBaseline([]string{"legacy"}, recorded)
		if strings.Join(failing, ",") != "legacy" || len(passing) != 0 {
			t.Fatal(name, failing, passing)
		}
	}
	if failing, passing := knownFailureBaseline([]string{"legacy"}, []KnownFailure{{Label: "legacy", Passed: true}}); len(failing) != 0 || strings.Join(passing, ",") != "legacy" {
		t.Fatal(failing, passing)
	}
}

func readRel(t *testing.T, e *Engine, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(e.Config.Worktree, rel))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
