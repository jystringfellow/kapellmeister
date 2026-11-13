package tune

import "fmt"

const VerifyProcedure = `1. Read what the repository already says about passing: CI workflow files, a Makefile or task runner, the README's test instructions, km init's hints.
2. Write the command into scripts/verify, keeping the bounded-output wrapper. Prefer what CI runs. Leave out what cannot run in a clean checkout with no services, and say what was left out.
3. Write at least two break patches outside the checkout: one that breaks source code a test covers, one that breaks a test's expectation.
4. Run ` + "`km prove`" + ` with those patches until it reports proved. If it cannot be proved, say why.`

func Task(checkout, hints, command, patches string) string {
	return fmt.Sprintf("Tune this repository's verify.\nCheckout: %s\nHints from km init:\n%s\nCurrent verify: %s\nBreak patch directory: %s\n\n%s\n\nOnly scripts/verify and .kapellmeister/config.toml may change in the checkout. Keep the bounded-output wrapper and remove the unfinished marker. Set [test] verify to scripts/verify so later runs use that wrapper. Keep break patches in the directory above. Leave changes uncommitted.\nThe conductor runs km prove itself; a chair's approval cannot prove the result.\n", checkout, hints, command, patches, VerifyProcedure)
}
