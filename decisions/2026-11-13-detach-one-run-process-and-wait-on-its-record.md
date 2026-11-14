# Detach one run process and wait on its record

`km run -detach` re-executes km in its own session with null stdin and stdout/stderr in the
run's `output.log`, after the foreground refusals and worktree/profile preparation. No daemon
or listening port is added. The launcher returns once `current.json` makes the run visible;
preflight and baseline verify follow in the run process because they can take minutes and need
CAO and the chairs. `launch.json` retains the resolved worktree/base and task text. Result writes
use atomic rename so readers cannot observe partial records. Evidence:
`TestDetachPrintsTheRunIdPidWorktreeAndOutputPathAndReturnsWhileTheRunIsStillGoing`,
`TestADetachedRunFinishesAfterTheCallerHasExitedAndWaitPrintsItsSummaryAndStatus`,
`TestADetachedRunKeepsTheLaunchersWholeOutputAndMakesOnlyOneWorktree` and
`TestADetachedRunStopsOnSIGTERMToItsPidAndTearsTheEnsembleDown`.

`km wait` shares status's discovery and process identity checks, accepts exact ids or unique
prefixes, and prints the run's final summary. Exit statuses are 0 approved, 1 failed or command
error, 3 timeout and 4 unfinished with no result and a gone or unverifiable process. Timeout
never stops the run; output is read from a file to keep waiting independent from following it.
Evidence: `TestWaitTimesOutWithItsOwnStatusAndLeavesTheRunAlone`,
`TestWaitReportsARunWhoseProcessIsGoneOrUnknownAsUnfinishedNeverApproved`,
`TestWaitAcceptsAUniquePrefixAndRefusesAnAmbiguousOrUnknownRun` and
`TestRunWithoutDetachWritesNoOutputLogOrLaunchFileAndExitsAsBefore`.
