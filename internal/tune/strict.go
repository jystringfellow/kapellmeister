package tune

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jystringfellow/kapellmeister/internal/loop"
)

type Strict struct {
	Excluded    []loop.Exclusion
	TimeBounded bool
	Timeout     time.Duration
}

var ProbeReasons = []string{"network", "service", "credential", "machine"}

func (s Strict) NeedProbe() []string {
	var suites []string
	for _, entry := range s.Excluded {
		if slices.Contains(ProbeReasons, entry.Reason) {
			suites = append(suites, fmt.Sprintf("%q: %s", entry.Suite, entry.Reason))
		}
	}
	return suites
}

func (s Strict) Procedure() string {
	var text strings.Builder
	text.WriteString("1. Read what CI runs: workflow files, a Makefile or task runner, the README's test instructions. Read scripts/verify and [test] in .kapellmeister/config.toml: verify is written and proved, and it is not yours to change.\n")
	text.WriteString("2. Write scripts/strict, keeping the bounded-output wrapper. Strict holds what verify had to leave out, so that verify and strict together run everything CI runs. Give each suite its own run step and do not repeat what verify already runs. Strict has no time target: nothing is left out of it to save time.")
	if len(s.Excluded) > 0 {
		text.WriteString(" These suites are excluded from verify, and strict must run every one in a run step labelled exactly with its name:")
		for _, entry := range s.Excluded {
			fmt.Fprintf(&text, "\n   - %s (%s): %s", entry.Suite, entry.Reason, entry.Run)
		}
		text.WriteString("\n  ")
	}
	if s.TimeBounded {
		text.WriteString(" This verify is time-bounded, so it was allowed to cut what did not fit its time target: find what CI runs that scripts/verify does not, and give it a step.")
	}
	text.WriteString(" Leave a test out of both only when no machine a developer has can run it; say which and why, with a by-hand command.")
	if needing := s.NeedProbe(); len(needing) > 0 {
		fmt.Fprintf(&text, " km has decided this strict needs a readiness probe, because it holds a suite that needs something a machine may lack (%s): write scripts/strict-ready, an executable script that exits 0 when what those suites need is there (the network can be reached, the service answers, the credential is set), and otherwise prints what is missing and exits non-zero. It finishes in seconds and tests nothing else. Set strict_ready = \"scripts/strict-ready\" in [test].", strings.Join(needing, ", "))
	} else {
		text.WriteString(" Do not write a strict_ready probe: km decides when one is needed, and this strict does not need one.")
	}
	if s.Timeout > 0 {
		fmt.Fprintf(&text, " Set strict_timeout = %q in [test]: the caller passed that limit.", s.Timeout.String())
	}
	text.WriteString("\n3. Write at least two break patches outside the checkout: one that breaks source code a strict step covers, one that breaks the expectation of a test a strict step runs. Each must make scripts/strict fail.\n")
	text.WriteString("4. Run scripts/strict once to see it pass. Do not run `km prove` yourself: km runs it with your patches after this step, where it has the time, and sends the result back when it is not proved. If strict cannot pass, say why.")
	return text.String()
}

func (s Strict) ReviewBrief() string {
	brief := "Read scripts/strict and scripts/verify against CI workflows, the Makefile or task runner and README test instructions.\nVerify and strict together must run everything CI runs: ask for changes when a suite CI runs is in neither, or when a suite in [[test.excluded]] has no run step of its own in scripts/strict. Time is not a reason to leave anything out of strict. Check that each test left out of both has a reason and a by-hand command. Check that the source and test patches exercise what a strict step runs.\n"
	if len(s.NeedProbe()) > 0 {
		brief += "km decided this strict needs a readiness probe: ask for changes when scripts/strict-ready tests anything but whether what those suites need is there.\n"
	}
	return brief
}

func StrictTask(checkout, patches string, s Strict) string {
	return fmt.Sprintf("Tune this repository's strict.\nCheckout: %s\nBreak patch directory: %s\n\n%s\n\nOnly scripts/strict, scripts/strict-ready and the [test] strict, strict_ready and strict_timeout keys of .kapellmeister/config.toml may change in the checkout. Keep the bounded-output wrapper and remove the unfinished marker. Set strict = \"scripts/strict\" in [test] so later runs use that wrapper. Keep break patches in the directory above. Leave changes uncommitted.\nThe conductor runs km prove itself; a chair's approval cannot prove the result.\n", checkout, patches, s.Procedure())
}
