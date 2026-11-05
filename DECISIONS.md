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

Ensembles name agents, and agents depend on the user's accounts, the same reason `[chairs]` is
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

## 2026-11-05: Own less; use CAO's primitives and upstream the gaps

kapellmeister owns only what CAO does not provide. Where CAO has a primitive (sessions, terminals,
group labels, siblings, the inbox, `send_message`), km uses it, even when it has known gaps. What
km must own goes behind a narrow seam shaped like the CAO primitive it stands in for, so it can be
swapped out when CAO ships one, and the gap becomes a proposed contribution to CAO. Workarounds for
CAO bugs live in the CAO stepper and are removed once the fix ships. This scopes the 2026-11-03
decision: km keeps its own loop, the score, but not its own team plumbing. Evidence: the
ensemble spike in `spike/SPIKE.md`, which found CAO covers boot, sections and sibling discovery,
and lacks acknowledged messages, an operator mailbox and restart recovery.

## 2026-11-05: Repositories may check in ensembles as files

This narrows "Ensembles live only in ~/.kapellmeister". Codeowners often know which ensemble gets
the best results in their codebase, so a repository may check in
`.kapellmeister/ensembles/NAME.toml`, which seats chairs in sections. The machine-bound part stays
with the user. A repository file names only agents, each with an optional tier, never profiles or
models. Each chair lists agents in order of preference and plays the first one in the user's
`[agents] available`. This follows OpenRig's split between a portable spec and machine bindings.
A user's own file of the same name shadows the repository's. `[ensembles]` in the repository config
stays rejected. Evidence: `TestARepositoryEnsembleNamesOnlyAgentsWhileAUserEnsembleMayNameAnyChair`,
`TestAChairPlaysTheFirstOfItsAgentsThatTheUserHas`,
`TestAUserEnsembleFileShadowsTheRepositorysOfTheSameName` and
`TestEnsemblesInTheRepositoryConfigAreRejected`.

## 2026-11-05: Name things from orchestral practice

kapellmeister's team-layer words come from the orchestra. km is the conductor, and the other terms
are ensemble, section, chair, occupant, part, score, cue, send, audition, rehearse and play. The
list lives in `AGENTS.md`, so every chair of a km run sees it. Evidence: the first km-built change
(PR #10) named its shared seat-and-cue code `crew`, because no chair had been told the vocabulary;
the planner chose it and neither the reviewer nor the gate flagged it.
