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

## 2026-11-05: Play keeps each part in its chair's occupant and runs without sizing

`km play` reuses each chair's recorded occupant through CAO's `reuse_terminal_id`. Sizing and tier
escalation are off, and the stepper rejects any binding mismatch. Steps sharing an occupant run
one at a time, and play never deletes an occupant. Evidence:
`TestEachPartAndEachReviewerIsRoutedToItsOwnStandingOccupant`,
`TestClassifyStepsSharingOneOccupantNeverOverlap`,
`TestAStepWhoseBindingDiffersFromItsOccupantFailsNamingTheAddressAndBothBindings` and
`TestPlayKeepsEachChairsRecordedBindingEvenWhenSizingAndEscalationAreConfigured`.

## 2026-11-06: The tester part tests the change live, blind to the implementation

Reviewers read the diff and verify runs unit tests, leaving real use unexercised. An optional
tester writes a blind test plan alongside round 1's implementation, then plays it back after the
gate approves. Failures send the work back for another round, starting with failing regression
tests. Parts keep plain names; chairs may carry orchestral names, such as `tonmeister`.
Evidence: two km-built PRs passed checks and won reviewer and gate approval, yet live use found
`km play` starting on a half-briefed ensemble and CAO answering DELETE with 409 for a terminal
whose GET was already 404. Tests:
`TestATestPlanStartsAfterThePlanAndRunsAlongsideRoundOnesImplementer` and
`TestATestChangesStartsAnotherRoundWithTheTestReportLeadingTheFeedback`.

## 2026-11-06: One CAO session per run, watched for blocked chairs

`km run` creates an idle podium terminal because CAO cannot create an empty session, adds each
step to that session, and deletes the session on completion or interrupt. `km play` watches its
standing session and keeps it. The watcher polls `GET /sessions/{name}`, because the terminals
listing lacks live status, and reads output only for waiting or processing terminals. It sits
behind the two read calls so CAO status events can replace polling. Evidence:
`TestRunCreatesOnePodiumSessionAndPassesItOnEveryNewRunStep`,
`TestRunDeletesItsSessionOnSuccessFailureAndInterrupt`,
`TestPlayPollsTheStandingSessionAndNeverDeletesIt` and
`TestABlockedTerminalIsReportedOnceWhileItPersistsAndAgainWhenItChanges`;
CAO 2.5.4 live findings in `spike/SPIKE.md`.

## 2026-11-06: Report chair permissions and inherit them in effort profiles

km reports permission modes without enforcing them. Labels come from CAO's profile HTTP endpoint
and model each provider's real launch: Claude's `permissionMode`, Copilot and Antigravity's fixed
skip flags, and Codex's `codexProfile`, without which CAO launches Codex with `--yolo` even when
`allowedTools` lacks `*`. Generated effort profiles copy the base's `permissionMode`,
`allowedTools` and `codexProfile`, and installed variants are reinstalled when they differ, because
a variant otherwise silently drops the base's choice. Evidence:
`TestPermissionLabelNamesEachProvidersRealLaunchMode`,
`TestCodexWithoutANamedProfileRunsUnguardedEvenWithoutWildcardTools`,
`TestAnEffortProfileKeepsTheBasesPermissionMode`,
`TestEnsureProfilesReinstallsAVariantWhosePermissionsDifferFromItsBase`,
`TestEnsureProfilesKeepsAndSynchronizesTheBasesNamedCodexProfile` and
`TestConfigShowsTheBasePermissionsThatTheNextRunWillSynchronize`; CAO 2.5.4 findings 21 and 22 in
`spike/SPIKE.md`.

## 2026-11-10: Seat preflight chairs before cueing them

Preflight creates uniquely named CAO sessions before using `reuse_terminal_id`, so an interrupt
cannot lose track of a chair still answering `run-step`. Seating waits for its bounded launch
response, and cancellation deletes owned sessions on a fresh, bounded context. A failed launch
without a returned identity is cleaned up by its exact km-chosen, already-prefixed CAO name,
including on retry; a missing session is harmless. Ordinary blocked chairs keep their session
for attaching. Evidence:
`TestACancelledPreflightAgainstCAODeletesExactlyTheSessionsItCreated`,
`TestACancelDuringSeatingStillTakesDownTheSessionCAOCreated`,
`TestPreflightTeardownIsBoundedWhenCAOStopsAnswering`,
`TestPreflightDeletesTheChosenNameWhenSeatingFailsWithoutASession` and
`TestPreflightAgainstCAODeletesTheChosenNameWhenSeatingReturnsNoIdentity`.

## 2026-11-11: Collect lessons in shadow before reading them back

The gate proposes candidate lessons after the score ends; km records them in state without feeding
them into later runs. Collection may never change a run's outcome or exit code, including on timeout,
invalid replies or a failed write. Evidence:
`TestARunHasTheSameOutcomeRoundsAndReasonWithAndWithoutTheLessonsStep`,
`TestARunCommandKeepsItsExitCodeWhenTheLessonsStepFailsOrIsOff`,
`TestAFailedLessonsWriteDoesNotFailTheRun` and `TestKmNeverReadsLessonsJSONBack`.

## 2026-11-11: Read token usage behind a per-step window

km reads vendor session logs because CAO 2.5.4 exposes no usage. The meter seam opens and closes a
usage window around one run-step attempt, so CAO can replace log reading with a per-terminal usage
field or endpoint on run-step; propose that primitive upstream. Windows use log growth, distinguish
concurrent chairs by reply path, and serialize steps sharing a chair or an explicitly reused terminal.
Evidence: `TestTwoStepsOnOneReusedTerminalEachGetOnlyTheirOwnUsage`,
`TestConcurrentStepsInOneWorktreeAreAttributedByTheirReplyPath`,
`TestStepsQueuedForOneChairDoNotCountEachOthersUsage` and
`TestRunRecordsEachStepsOwnUsageForClaudeCodexAndUnknownAgentsAndTotalsThemInResultJSON`.

## 2026-11-11: Lessons reach chairs only through checked-in, reviewed files

Lessons come only from files committed under `.kapellmeister/learned/`, read once at the run's
base commit. A person reviews them through the same pull request process as code; km never writes
there or reads lessons from agent-written memory. This prevents a bad run from teaching later
steps or runs without review. Evidence: `TestLessonsAreReadFromTheBaseCommitNotTheWorkingFiles`,
`TestALessonWrittenByAStepIsNotSeenByALaterStepOfTheSameRun` and
`TestKmNeverWritesUnderTheLearnedDirectory`.

## 2026-11-11: Chairs receive exactly the reviewed lesson bytes

Read committed lessons from git's stdout only. Refuse invalid UTF-8, invisible or direction-changing
characters and section markers rather than escaping: skipped files remain visible in warnings and
records, while escaping would give chairs text that differs from what was reviewed. Name the
checked-in budget `learned_budget` to distinguish it from gate collection's `lessons` and
`lessons_timeout`. Evidence:
`TestAWarningGitPrintsToStderrWhileSucceedingNeverReachesLessonsOrTheirHash`,
`TestUnsafeLessonFilesAreSkippedWithAWarning`,
`TestALessonFileContainingAMarkerCannotEndTheSectionEarly` and
`TestTheOldLessonsBudgetKeyIsAnErrorNamingLearnedBudget`.
