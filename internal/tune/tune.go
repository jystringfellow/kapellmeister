package tune

import (
	"fmt"
	"slices"
	"strings"
)

const VerifyProcedure = `1. Read what the repository already says about passing: CI workflow files, a Makefile or task runner, the README's test instructions, km init's hints.
2. Write the command into scripts/verify, keeping the bounded-output wrapper. This verify mirrors CI and may take as long as CI: run every suite CI requires and report the measured time. Speed is the job of check, so leave nothing out to save time. Leave a test out only for an observed reason: it failed or hung in a run made during this tune (say which run and what it printed); or it needs something the clean copy cannot have (network, a service, a credential, another machine). A test that looks timing-dependent is not a reason. Each omission says which of those reasons applies, with its evidence, what was left out and how to run it by hand: a flaky verify is worse than a narrower one. Every verify runs on a clean copy: dependency installation counts, so use the lockfile and the package manager's cache.
3. Write at least two break patches outside the checkout: one that breaks source code a test covers, one that breaks a test's expectation.
4. Run ` + "`km prove`" + ` with those patches until it reports proved. If it cannot be proved, say why.`

const mirrorsCI = "This verify mirrors CI and may take as long as CI: run every suite CI requires and report the measured time. Speed is the job of check, so leave nothing out to save time. "
const timeTarget = "Aim for about two minutes on a clean copy and never more than five; report the measured time. When the whole suite fits that target, the verify runs the whole suite; narrow it only when it does not fit. When CI takes longer, choose the fast checks CI runs (format, lint, type check, build) plus unit tests for the code most often changed or a fast marked subset. Set verify_time_bounded = true in [test]. "
const noTimeReason = "; or it needs something the clean copy cannot have (network, a service, a credential, another machine). "
const timeReason = "; it needs something the clean copy cannot have (network, a service, a credential, another machine); or the suite does not fit the time target and this is the part cut (say the measured time). "

type Requirements struct {
	Suites      []string
	TimeBounded bool
}

func Require(config, flags []string, timeBounded bool) Requirements {
	suites := slices.Clone(config)
	for _, suite := range flags {
		if !slices.Contains(suites, suite) {
			suites = append(suites, suite)
		}
	}
	return Requirements{Suites: suites, TimeBounded: timeBounded}
}

func (r Requirements) KeepsSlowTests() string {
	if len(r.Suites) > 0 {
		return "required suites are never left out for time"
	}
	if !r.TimeBounded {
		return "a verify is never narrowed for time"
	}
	return ""
}

func (r Requirements) Procedure() string {
	text := VerifyProcedure
	if r.TimeBounded {
		text = strings.Replace(strings.Replace(text, mirrorsCI, timeTarget, 1), noTimeReason, timeReason, 1)
	}
	if len(r.Suites) > 0 {
		text = strings.Replace(text, "\n3. ", " Required suites: "+strings.Join(r.Suites, ", ")+". Give each its own `run` step labelled exactly with its name and list them in [test] verify_required. Never leave a required suite out or narrow it, for time or any other reason: km raises the verify timeout for a slow one, and when one cannot pass on a clean copy, say so in your reply instead of narrowing it. At least one break patch must make verify fail in the slowest required suite's step, so write one that only that suite catches.\n3. ", 1)
	}
	return text
}

func (r Requirements) ReviewRules() string {
	rules := "This verify may take as long as CI: time is not a permitted reason to leave a test out, so ask for changes when anything is left out to save time.\n"
	if r.TimeBounded {
		rules = "This verify is time-bounded: aim for about two minutes on a clean copy and never more than five. A suite that does not fit that target may be cut, with the measured time as its evidence; ask for changes when the whole suite would fit the target.\n"
	}
	if len(r.Suites) > 0 {
		rules += "Required suites: " + strings.Join(r.Suites, ", ") + ". Ask for changes when one is narrowed or its run step does not run it; no reason permits leaving one out.\n"
	}
	return rules
}

func Task(checkout, hints, command, patches string, required Requirements) string {
	return fmt.Sprintf("Tune this repository's verify.\nCheckout: %s\nHints from km init:\n%s\nCurrent verify: %s\nBreak patch directory: %s\n\n%s\n\nOnly scripts/verify and .kapellmeister/config.toml may change in the checkout. Keep the bounded-output wrapper and remove the unfinished marker. Set [test] verify to scripts/verify so later runs use that wrapper. Keep break patches in the directory above. Leave changes uncommitted.\nThe conductor runs km prove itself; a chair's approval cannot prove the result.\n", checkout, hints, command, patches, required.Procedure())
}
