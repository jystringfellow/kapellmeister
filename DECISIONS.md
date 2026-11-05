# Decisions

Decisions that span many changes. Add an entry when a choice constrains future work; reverse one
only with new evidence, and add the reversal as a new entry.

## 2026-11-03: Own loop over CAO run-step, not CAO workflows

kapellmeister drives plan, implement, review and gate itself through `/terminals/run-step`. CAO
workflows were tried twice and rejected: every new worktree needs `cao workflow approve`, script
workflows have no `workflow_return`, and runs record almost no telemetry. Rechecked against CAO
main on 2026-11-04. Evidence: `spike/SPIKE.md`, findings 9, 10, 12 and 13.

## 2026-11-04: Name kapellmeister, binary km

`oko` clashed with an existing agent CLI, and `ostinato` with a Debian binary. Config lives in
`~/.kapellmeister` and `<repo>/.kapellmeister/`; CAO profiles are `km-<agent>`.

## 2026-11-04: Windows through WSL2

CAO refuses to run on native Windows, so kapellmeister supports Windows only under WSL2.

## 2026-11-04: Tests are the spec

No spec documents. Behavior lives in tests, reasons in commits and PRs, and decisions that span
changes here. Checked-in specs became a second source of truth that drifted from the code.

## 2026-11-04: Ensembles live only in ~/.kapellmeister

Ensembles name agents, and agents depend on the user's accounts, the same reason `[seats]` is
machine-only. The repository config rejects `[ensembles]`; user config accepts it. Evidence:
`TestEnsemblesInTheRepositoryConfigAreRejected` and `TestANamedEnsembleLoadsFromTheUserConfig`.

## 2026-11-04: Parallel reviewers, weighed by the gate

Reviewers share the worktree and are checked read-only as a group, with one snapshot before
starting and one after all finish. A failed reviewer informs the gate instead of failing the run;
if every reviewer fails, the run fails. The gate checks the diff and decides which findings block
and which are nits. Evidence: `TestReviewersRunInParallelAndTheGateReadsEveryReview`,
`TestAReviewerThatModifiesTheWorktreeFailsTheRunAndTheErrorNamesTheGroup`,
`TestAFailedReviewerIsRecordedAsFailedNamedToTheGateAndTheRoundContinues`, and
`TestTheGateSortsFindingsIntoBlockingAndNitsAndApprovesWhenNothingBlocks`.
