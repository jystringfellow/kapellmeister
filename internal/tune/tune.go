package tune

import (
	"fmt"
	"slices"
	"strings"
)

const VerifyProcedure = `1. Read what the repository already says about passing: CI workflow files, a Makefile or task runner, the README's test instructions, km init's hints.
2. Write the command into scripts/verify, keeping the bounded-output wrapper. Aim for about two minutes on a clean copy and never more than five; report the measured time. When the whole suite fits that target, the verify runs the whole suite; narrow it only when it does not fit. When CI takes longer, choose the fast checks CI runs (format, lint, type check, build) plus unit tests for the code most often changed or a fast marked subset. Leave a test out only for an observed reason: it failed or hung in a run made during this tune (say which run and what it printed); it needs something the clean copy cannot have (network, a service, a credential, another machine); or the suite does not fit the time target and this is the part cut (say the measured time). A test that looks timing-dependent is not a reason. Each omission says which of those reasons applies, with its evidence, what was left out and how to run it by hand: a flaky verify is worse than a narrower one. Every verify runs on a clean copy: dependency installation counts, so use the lockfile and the package manager's cache.
3. Write at least two break patches outside the checkout: one that breaks source code a test covers, one that breaks a test's expectation.
4. Run ` + "`km prove`" + ` with those patches until it reports proved. If it cannot be proved, say why.`

const timeTarget = "Aim for about two minutes on a clean copy and never more than five; report the measured time. When the whole suite fits that target, the verify runs the whole suite; narrow it only when it does not fit. When CI takes longer, choose the fast checks CI runs (format, lint, type check, build) plus unit tests for the code most often changed or a fast marked subset. "
const slowTarget = "This verify mirrors CI and may take as long as CI: run every suite CI requires and report the measured time. Speed is the job of check, so leave nothing out to save time. Set slow = true in [test]. "
const timeReason = "; it needs something the clean copy cannot have (network, a service, a credential, another machine); or the suite does not fit the time target and this is the part cut (say the measured time). "
const noTimeReason = "; or it needs something the clean copy cannot have (network, a service, a credential, another machine). "

type Requirements struct {
	Suites []string
	Slow   bool
}

func Require(config, flags []string, slow bool) Requirements {
	suites := slices.Clone(config)
	for _, suite := range flags {
		if !slices.Contains(suites, suite) {
			suites = append(suites, suite)
		}
	}
	return Requirements{Suites: suites, Slow: slow}
}

func (r Requirements) KeepsSlowTests() string {
	if len(r.Suites) > 0 {
		return "required suites are never left out for time"
	}
	if r.Slow {
		return "a slow verify is never narrowed for time"
	}
	return ""
}

func (r Requirements) Procedure() string {
	text := VerifyProcedure
	if r.Slow {
		text = strings.Replace(strings.Replace(text, timeTarget, slowTarget, 1), timeReason, noTimeReason, 1)
	}
	if len(r.Suites) > 0 {
		text = strings.Replace(text, "\n3. ", " Required suites: "+strings.Join(r.Suites, ", ")+". Give each its own `run` step labelled exactly with its name and list them in [test] required. Never leave a required suite out or narrow it, for time or any other reason: km raises the verify timeout for a slow one, and when one cannot pass on a clean copy, say so in your reply instead of narrowing it.\n3. ", 1)
	}
	return text
}

func (r Requirements) ReviewRules() string {
	rules := ""
	if r.Slow {
		rules += "This verify may take as long as CI, which overrides the time target above: time is not a permitted reason to leave a test out, so ask for changes when anything is left out to save time.\n"
	}
	if len(r.Suites) > 0 {
		rules += "Required suites: " + strings.Join(r.Suites, ", ") + ". Ask for changes when one is narrowed or its run step does not run it; no reason permits leaving one out.\n"
	}
	return rules
}

func Task(checkout, hints, command, patches string, required Requirements) string {
	return fmt.Sprintf("Tune this repository's verify.\nCheckout: %s\nHints from km init:\n%s\nCurrent verify: %s\nBreak patch directory: %s\n\n%s\n\nOnly scripts/verify and .kapellmeister/config.toml may change in the checkout. Keep the bounded-output wrapper and remove the unfinished marker. Set [test] verify to scripts/verify so later runs use that wrapper. Keep break patches in the directory above. Leave changes uncommitted.\nThe conductor runs km prove itself; a chair's approval cannot prove the result.\n", checkout, hints, command, patches, required.Procedure())
}
