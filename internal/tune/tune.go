package tune

import "fmt"

const VerifyProcedure = `1. Read what the repository already says about passing: CI workflow files, a Makefile or task runner, the README's test instructions, km init's hints.
2. Write the command into scripts/verify, keeping the bounded-output wrapper. Aim for about two minutes on a clean copy and never more than five; report the measured time. When the whole suite fits that target, the verify runs the whole suite; narrow it only when it does not fit. When CI takes longer, choose the fast checks CI runs (format, lint, type check, build) plus unit tests for the code most often changed or a fast marked subset. Leave a test out only for an observed reason: it failed or hung in a run made during this tune (say which run and what it printed); it needs something the clean copy cannot have (network, a service, a credential, another machine); or the suite does not fit the time target and this is the part cut (say the measured time). A test that looks timing-dependent is not a reason. Each omission says which of those reasons applies, with its evidence, what was left out and how to run it by hand: a flaky verify is worse than a narrower one. Every verify runs on a clean copy: dependency installation counts, so use the lockfile and the package manager's cache.
3. Write at least two break patches outside the checkout: one that breaks source code a test covers, one that breaks a test's expectation.
4. Run ` + "`km prove`" + ` with those patches until it reports proved. If it cannot be proved, say why.`

func Task(checkout, hints, command, patches string) string {
	return fmt.Sprintf("Tune this repository's verify.\nCheckout: %s\nHints from km init:\n%s\nCurrent verify: %s\nBreak patch directory: %s\n\n%s\n\nOnly scripts/verify and .kapellmeister/config.toml may change in the checkout. Keep the bounded-output wrapper and remove the unfinished marker. Set [test] verify to scripts/verify so later runs use that wrapper. Keep break patches in the directory above. Leave changes uncommitted.\nThe conductor runs km prove itself; a chair's approval cannot prove the result.\n", checkout, hints, command, patches, VerifyProcedure)
}
