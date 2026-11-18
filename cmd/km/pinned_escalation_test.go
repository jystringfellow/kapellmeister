package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/jystringfellow/kapellmeister/internal/loop"
)

func TestARunSaysBeforeItStartsThatEscalationOverridesAPinnedImplementerTier(t *testing.T) {
	policy := loop.DefaultPolicy()
	var out bytes.Buffer
	printPinnedEscalation(&out, &policy, loop.Chair{Provider: "codex", Profile: "km-codex", Tier: "medium"})
	want := "       the implementer is pinned to medium, but escalation overrides the pin: it can reach large, one tier after every 2 rounds without approval; set escalate = false in the ensemble to hold the pin\n"
	if out.String() != want {
		t.Fatalf("got %q\nwant %q", out.String(), want)
	}
}

func TestNothingIsSaidAboutAPinnedImplementerTierThatEscalationCannotMove(t *testing.T) {
	off := 0
	for name, tc := range map[string]struct {
		chair  loop.Chair
		adjust func(*loop.Policy)
	}{
		"escalation off":      {loop.Chair{Provider: "codex", Profile: "km-codex", Tier: "medium"}, func(p *loop.Policy) { p.Sizing.EscalateAfter = &off }},
		"bounds hold the pin": {loop.Chair{Provider: "codex", Profile: "km-codex", Tier: "large"}, func(*loop.Policy) {}},
		"pin clamped to the only tier": {loop.Chair{Provider: "codex", Profile: "km-codex", Tier: "small"}, func(p *loop.Policy) {
			p.Steps["implement"] = loop.StepPolicy{Tier: "medium", Min: "medium", Max: "medium"}
		}},
		"not pinned":     {loop.Chair{Provider: "codex", Profile: "km-codex"}, func(*loop.Policy) {}},
		"explicit model": {loop.Chair{Provider: "codex", Profile: "km-codex", Model: "some-model"}, func(*loop.Policy) {}},
	} {
		t.Run(name, func(t *testing.T) {
			policy := loop.DefaultPolicy()
			tc.adjust(&policy)
			var out bytes.Buffer
			printPinnedEscalation(&out, &policy, tc.chair)
			if out.Len() != 0 {
				t.Fatalf("printed %q", out.String())
			}
		})
	}
}

func TestTheOpeningOutputOfARunWithAPinnedImplementerNamesTheEscalationOverride(t *testing.T) {
	f := runBaseFixture(t, false)
	out, err := captureStdout(t, func() error { return runCommand(append(f.args, "-implementer", "codex:km-codex@medium")) })
	if !strings.Contains(out, "the implementer is pinned to medium, but escalation overrides the pin: it can reach large") {
		t.Fatalf("opening output lacks the override line (run error %v):\n%s", err, out)
	}
}
