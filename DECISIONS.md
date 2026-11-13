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
field or endpoint on run-step; propose that primitive upstream. Windows use log growth and serialize
steps sharing a chair or an explicitly reused terminal. Opening uses the run context; queue time is
recorded apart from duration and is excluded from the running-step watcher.
Evidence: `TestTwoStepsOnOneReusedTerminalEachGetOnlyTheirOwnUsage`,
`TestConcurrentStepsInOneWorktreeAreAttributedByTheirReplyPath`,
`TestStepsQueuedForOneChairDoNotCountEachOthersUsage`,
`TestAStepQueuedBehindAnotherOnItsChairRecordsTheWaitAsQueueMSAndNotAsDuration`,
`TestAStepStillQueuedForItsChairIsNotWatchedAsRunning` and
`TestCancellingTheRunWhileAStepWaitsForItsChairEndsTheWaitWithoutRunningTheStep`.

## 2026-11-11: Wrong usage is worse than unknown

Exactly one growing log must name the reply path, even when only one grew. Claude message ids are
counted once per transcript across windows; unknown windows commit no ids. Claude subagent logs
under `<session-id>/subagents/` share their parent session's window and need no reply-path match.
A real Claude log had 489 parent output tokens and 502 separate subagent output tokens, so omitting
subagents would under-count usage. Parent and subagent ids commit together only for known windows.
Evidence:
`TestOneGrownLogThatDoesNotNameTheReplyPathIsUnknownAsAmbiguous`,
`TestAStepWithoutAReplyPathNeverGetsUsageFromAGrownLog`,
`TestAClaudeMessageStraddlingTwoWindowsOnOneTerminalIsCountedOnlyOnce` and
`TestAClaudeMessageIsCountedAgainWhenTheEarlierWindowWasUnknown`,
`TestAClaudeStepsUsageIncludesItsSubagentTranscripts`,
`TestClaudeSubagentUsageIsAttributedOnlyThroughItsParentReplyPath` and
`TestAnUnknownClaudeSubagentWindowCommitsNoMessageIDs`.

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


## 2026-11-11: Report never shows unmeasured usage as zero

Report uses only usage recorded in result.json. A partly measured run, such as one with chairs whose
tool keeps no usage log, shows what was measured as a lower bound marked `≥`, because treating it as
unknown hid the usage of every run that seats such a chair; invalid or missing counters are unknown. Ensemble and repository totals exclude unknown runs and
count them explicitly. Search depth and entry budgets bound work on untrusted kept sandbox copies;
skipped paths remain visible. Evidence:
`TestReportShowsUnknownUsageForARunWithoutUsageOrWithUnknownUsageNeverZero`,
`TestReportShowsPartlyMeasuredUsageAsALowerBoundAndSaysHowManyRunsArePartlyMeasured`,
`TestReportTotalsByEnsembleAndRepositorySumOnlyKnownUsageAndCountRunsLeftOut`,
`TestReportStopsBelowTheDepthLimitSaysWhatItSkippedAndCarriesOn`,
`TestReportStopsAtTheEntryLimitSaysWhatItSkippedAndCarriesOn` and
`TestReportDefaultLimitsReachThousandsOfKeptSandboxesAndTwentyLevelsOfDepth`.

## 2026-11-11: Disagreeing run records are listed, not merged

A shared run id does not prove records agree: known starts, repositories and finished outcomes must
match. Prefer the most complete agreeing copy, then lexical path, independently of directory order.
List conflicting versions separately and diagnose both paths. Incomplete copies must not hide a
conflict between other copies. Evidence:
`TestReportListsTwoRecordsOfOneRunIdThatDisagreeAndNamesBothPathsOnStderr`,
`TestReportStillCountsIdenticalCopiesOfARunOnce` and
`TestReportNeverLetsAnIncompleteCopyHideDisagreementBetweenOtherCopies`.

## 2026-11-11: A started run always leaves a result

Once telemetry exists, run and play finalize failures, interrupts and panics with a failed outcome
and reason; panics are raised again. An outright kill or a failed result write can still leave an
unfinished record. The shared runEnsemble path owns this guarantee. km status is absent on this
base; the result is also the completion marker that status will need. Evidence:
`TestARunWhoseEngineErrorsAfterTelemetryStartsStillWritesAFailedResultWithItsReason`,
`TestARunWhoseConfigRecordCannotBeWrittenStillWritesAFailedResult`,
`TestARunWhoseWorktreeGitBreaksDuringTheRunStillWritesAFailedResult`,
`TestARunWhoseDiffStatFailsStillWritesAFailedResult`,
`TestARunThatPanicsAfterTelemetryStartsWritesAFailedResultAndPanicsAgain`,
`TestPlayAndRunBothLeaveAResultWhenTheRunFailsAfterItStarted` and
`TestRunDeletesItsSessionOnSuccessFailureAndInterrupt`.

## 2026-11-11: Name the signal that ended a run, and handle SIGTERM

Cancellation carries its signal cause through the score while still matching `context.Canceled`.
SIGTERM receives the same recording and cleanup as SIGINT. After gate approval, a signalled test
step ends `untested` and says the tester did not finish and the approval stands unverified. Both
tester briefings identify the conductor's PID and restrict signals to processes the tester started
and recorded. Evidence: runs lost when a tester signalled the first process named km;
`TestAnInterruptedRunPrintsAndRecordsTheSignalThatEndedIt`,
`TestASignalDuringTheTestStepAfterGateApprovalEndsUntestedAndSaysTheApprovalStandsUnverified`,
`TestTheTesterBriefingNamesTheConductorsPidAndForbidsSignallingItOrProcessesFoundByName`,
`TestASignalDuringParallelReviewsNamesTheSignal` and
`TestASignalDuringVerificationNamesTheSignalInsteadOfACommandExitFailure`.

## 2026-11-11: Seat preflight chairs one at a time and back off on CAO's session-creation race

Preflight serializes session creation while checks remain concurrent. Transient seat errors
(5xx, or a 400 naming the requested session as not found) get three retries after 1, 2 and 4 seconds,
each under a new name. A CAO call timeout is never retried, including when the launch context
expires before the client limit and returns `context.DeadlineExceeded`. This bounds the workaround
to session creation, behind the existing Seat seam. Evidence: CAO logs from failed preflights showed five
creations within 100 ms and tmux object, index and missing-session failures; immediate retries
failed in the same burst. Tests: `TestPreflightSeatsOneChairAtATimeButChecksThemTogether`,
`TestPreflightRetriesAnHTTP500SeatWithGrowingWaitsAndRenamedSessions`,
`TestPreflightRetriesA400ThatSaysTheSessionJustAskedForWasNotFound`,
`TestACancelDuringASeatBackoffEndsWithoutAnotherSeat`,
`TestPreflightDoesNotRetryAChairWhoseCAOCallTimedOut` and
`TestPreflightDoesNotRetryASeatWhenItsLaunchContextExpires`.
The tester reproduced the race on CAO 2.5.4: two of five concurrent session creations returned 500
with tmux object/index errors, while three serial creations returned 201. The earlier all-201
probe missed this intermittent failure. Concurrent terminal additions into one existing session
passed in both probes (five and four additions). Run steps and ensemble seating use this latter
primitive; their creation behavior stays unchanged. See `spike/SPIKE.md`, finding 26.

## 2026-11-11: A second signal kills, SIGHUP is handled, and session deletions take the seating turn

A review of the two entries above found gaps. km stopped listening after the first signal only when
the command returned, so a second SIGTERM during a long teardown did nothing; it now stops listening
at the first signal, and a second one ends the process the default way. SIGHUP is handled like
SIGINT and SIGTERM, because km runs beside tmux and closing its pane or killing the tmux server
sends SIGHUP. A command that is cancelled before a run starts, or whose result is written by the
failure path, now names the signal where it said only "context canceled". In preflight, deleting a
session takes the same one-at-a-time turn as creating one, since a deletion overlapping another
chair's creation is the same kind of concurrent tmux change the serial seating was meant to avoid;
retry waits carry random jitter so two km processes do not retry in step. Not covered by a test: the
second signal, and a deletion waiting for a creation. Two km processes preflighting at once can
still collide on their first attempts; the turn is per process.

## 2026-11-12: Propose lessons only where a person reviews them

Lessons collection is off by default: it costs up to five minutes after every run, and nothing in
km reads the proposals back. This repository opts in with `[run] lessons = true`; a person reviews
proposals before checking useful lessons in under `.kapellmeister/learned/`. This refines
"Collect lessons in shadow before reading them back". Evidence:
`TestLessonsAreOffByDefaultWithAFiveMinuteLimitWhenTurnedOn`,
`TestThisRepositorysConfigTurnsLessonsOn` and `TestADefaultRunMakesNoLessonsStep`.

## 2026-11-12: The base branch follows the repository

`km run -repo` chooses `-base`, then `[run] base` committed on the remote's recorded default branch,
then `origin/HEAD`, then `main` with a note when Git does not know the default. It fetches only the
branches needed to resolve that base, without pruning the clone's remote-tracking branches, and
creates retained worktrees under the user's `[run] worktree_dir` or the system temp directory.
Preflight reads screens through CAO and derives seating time from `preflight_timeout`.
Evidence: `TestRunWithoutBaseStartsFromAnOriginWhoseDefaultBranchIsNotMain`,
`TestRunNotesAndFallsBackToMainWhenGitDoesNotRecordOriginsDefaultBranch`,
`TestRepositoryConfigRunBaseChoosesTheBaseAndDashBaseWinsOverIt`,
`TestRunLeavesTheClonesRemoteTrackingBranchesAlone`,
`TestRunMakesItsWorktreeUnderTheConfiguredDirectoryAndTheSystemTempByDefault`,
`TestCAOStepperReadsAChairsScreenThroughCAOWithoutTmux` and
`TestPreflightGivesTheSeatADeadlineDerivedFromItsTimeout`.


## 2026-11-11: Protect occupied chair profiles and prove the CAO installation home

The suspected cause is confirmed as a reproducible mechanism, not as proof of the historical
incident's exact command. Against an isolated fake CAO reporting `permissionMode: bypassPermissions`
and `allowedTools: []`, the unmodified km binary's `preflight -agents claude -tier medium -timeout 0`
called the installed CAO 2.5.4 CLI and overwrote a healthy scratch `km-claude-high.md` containing
Bash, Read and Write permissions with `allowedTools: []`. The server URL did not select the
installation home: `cao install` used the calling shell's `CAO_HOME_DIR`. The fixed binary refused
the same scenario and preserved the healthy profile. All homes, state, API ports and tmux sockets
were isolated under scratch; only recorded server PIDs were stopped.

Before installing, km creates a random `km-probe-*` profile with POST `/agents/profiles`, verifies
its exact bytes in the local home, and deletes it with DELETE `/agents/profiles/{name}`. CAO 2.5.4
provided both routes (201/204); a real scratch server and the built km confirmed matching-home
installation and different-home refusal. Health and settings do not expose the CAO home, and
comparing existing source text cannot distinguish two homes with identical profiles. This probe
establishes where the server writes at that moment; it cannot protect against a subsequent restart
with another home, concurrent profile writers, or a dishonest server with access to the local home.
Unavailable or forbidden write routes fail closed. Cleanup uses a separate bounded context and
names any probe that could remain. The probe is a temporary API write even when a later profile
guard refuses; it does not install a chair or provider context. No probe occurs without pending
installations. CAO's content-write API does not materialize agent context/provider artifacts, while
its install API takes a name or HTTPS URL rather than supplied content. Propose installation from
supplied content through that API upstream so km can stop owning this home proof.

All pending profile replacements are checked before any variant is written or installed. A
replacement is refused when another unfinished run recorded in the state directory is alive, using
the same unfinished selection and `runLiveness` as status. Self, finished, abandoned and unknown
runs do not block; new or byte-identical profiles remain allowed. This protects only recorded runs
in the selected state directory, not arbitrary CAO users. Evidence:
`TestAProfileReplacementIsRefusedNamingTheProfileAndTheLiveRunThatMayDependOnIt`,
`TestEveryPendingProfileIsCheckedBeforeAnyVariantIsWrittenOrInstalled` and
`TestInstallingIsRefusedWhenTheCAOServerDoesNotWriteIntoTheCAOHomeCaoInstallWritesTo`.

## 2026-11-11: Give the tester an environment for nested conductors

CAO 2.5.4's installed constants and real scratch server confirmed `CAO_HOME_DIR`, `CAO_API_HOST`
and `CAO_API_PORT`; `CAO_API_BASE_URL` selects km's URL, not the server's bind port. `cao install`
also used the overridden home in the reproduction above. A shell file generated by km was sourced
before the real checks; the nested tmux socket appeared under its `TMUX_TMPDIR`. CAO uses
`libtmux.Server()` without socket flags. Clear inherited `TMUX` and `TMUX_PANE`, because tmux can
otherwise follow the conductor's socket despite a different `TMUX_TMPDIR`; clear `CAO_TERMINAL_ID`
to avoid inheriting the chair's identity. Keep all paths under scratch. Rather than escape a long
scratch path through an external symlink, refuse sourcing when the socket path exceeds 100 bytes,
below both macOS and Linux limits. A kernel-selected free port is chosen once per tester and can
race with a later binder. Tester briefings name the shared CAO URL and state directory as off
limits for writes and require sourcing before any nested km, CAO or tmux command. Other parts'
prompts are unchanged. Evidence:
`TestTheTesterEnvFileMovesKmCaoAndTmuxUnderTheScratchDirectory`,
`TestTheTesterEnvFileRefusesToBeSourcedWhenTheTmuxSocketPathWouldBeTooLong`,
`TestRunAndPlayWriteTheTesterEnvFileOnlyWhenATesterIsSeated` and
`TestTheTesterBriefingNamesTheConductorsCAOURLAndStateDirectoryAsOffLimitsAndTheEnvFileToSource`.

## 2026-11-12: Bind agent names in user config and derive chairs from available agents

User-only `[agents.define]` maps short names to `provider:profile`, beside the ordered
`[agents] available` list. It reuses the chair grammar, keeps strict unknown-key checks, and avoids
colliding with `available` when a user names an agent that. Built-ins remain defaults for definitions
and can be overridden. Repositories may name these agents in ensemble files but cannot define them:
profiles and accounts are machine bindings, as in the 2026-11-05 ensemble decision. Evidence:
`TestTheUserConfigDefinesAnAgentAsProviderAndProfile`, `TestADefinitionOverridesABuiltInAgent`,
`TestAnAgentMayBeDefinedInTheUserConfigOnly` and `TestARepositoryEnsembleMayNameAUserDefinedAgent`.

Available agents have no implicit default. Ignoring duplicates, the first plans, gates and classifies,
the second implements (or the first when alone), and the third reviews (or the first with two).
This gives independent review whenever there is a choice. When claude, codex and copilot are all
available, preserve their existing assignments regardless of order or extras. Explicit chairs,
ensembles and flags override derived defaults. Missing required chairs fail before CAO. Evidence:
`TestOneAvailableAgentPlaysEveryChair`, `TestWithTwoAgentsTheReviewerIsNotTheImplementersAgent`,
`TestClaudeCodexAndCopilotKeepTodaysChairsInAnyOrder` and
`TestRunRefusesWithOneLineBeforeAnyCAOCallWhenNoAgentsAndNoChairsAreConfigured`.

## 2026-11-12: Generate effort variants from any installed base, protecting existing profiles

An isolated CAO 2.5.4 installed `my-reviewer-high` from the user-named `my-reviewer` base and exposed
its permissions and effort through the profile endpoint; missing bases returned 404. It refused a
name installed from another source filename but allowed the matching filename; the refusal printed
an error while exiting zero. CAO's filename check alone cannot protect hand-installed variants. km synchronizes matching-provider effort variants
from any installed base, checks its generated description before replacing a stale installed variant,
and retains the CAO home proof and live-run guards for every pending installation. Colliding variant
names from different providers are refused. Variants inherit permissions only (`permissionMode`,
`allowedTools`, `codexProfile`); the prompt remains km's chair instructions, and MCP, skills and other
base fields are not copied. `inherit` uses the base without reading or installing variants. Evidence:
`TestAUserNamedProfileGetsItsEffortVariantInstalledFromItsBase`,
`TestAVariantKapellmeisterDidNotGenerateIsNotReplaced`,
`TestACustomBasesVariantReplacementIsRefusedWhileAnotherRunIsAlive`,
`TestACustomBasesVariantIsNotInstalledWhenTheServerHomeDiffers` and
`TestAUserDefinedAgentIsSeatedAtATieredChairWithoutAnyKmProfile`.
Keep profile generation behind its narrow seam; effort on CAO run-step and installation from supplied
content are upstream gaps that would let km own less, following the 2026-11-05 decision.

## 2026-11-11: What a repository tells its ensemble lives in the repository, not in km

How to run this repository's software live is knowledge about this repository, so it is checked in
here rather than built into km. Rules that must hold go in `AGENTS.md`, "Exercising km live", which
every chair is told to read and follow. `scripts/nested-km-env` writes the isolated environment
those rules require. `.kapellmeister/learned/` holds advice for one part and points to the rules
rather than repeating them, because lessons rank below instruction files and the budget can cut
them. This follows "Name things from orchestral practice" (2026-11-05), which put the vocabulary in
`AGENTS.md` so that every chair sees it. Evidence: two incidents while km ran on its own
repository. A tester signalled the first process named km and ended the run conducting it (see
"Name the signal that ended a run"). The profiles live chairs were playing from were overwritten;
`AGENTS.md` then asked a chair to isolate only `KAPELLMEISTER_STATE_DIR`, and an isolated run
showed that `cao install` writes to the calling shell's `CAO_HOME_DIR` whatever the server URL
(`spike/SPIKE.md`, finding 27). Both come from running a conductor under a conductor, which other
repositories do not do. Tests:
`TestTheEnvFileMovesKmCaoAndTmuxUnderTheScratchDirectoryAndClearsWhatTheChairInherited`,
`TestTheEnvFileQuotesScratchPathsWithSpacesQuotesAndDollars` and
`TestATmuxSocketPathOver100BytesIsRefusedAndNoEnvFileIsWritten`.

## 2026-11-12: The tester's briefing gives per-run facts and names no tools

This supersedes "Give the tester an environment for nested conductors" (2026-11-11): km no longer
writes a tester environment file, the `TesterEnvFile` setting is removed, and neither tester
briefing tells a chair to source anything. It narrows the briefing wording of "Name the signal
that ended a run, and handle SIGTERM" (2026-11-11): the conductor's PID remains, with its
orchestrator URL and state directory identified as outside the test and protected from signals,
stops and changes; signals are restricted to processes the tester started and recorded in
`<scratch>/pids`. The briefing names no tools or process-finding commands.

Nested-conductor setup and tool-specific safety rules live in `AGENTS.md`, "Exercising km live",
`scripts/nested-km-env` and `.kapellmeister/learned/tester.md`, following "What a repository tells
its ensemble lives in the repository, not in km" (2026-11-11). The CAO-home probe and the live-run
profile guard remain because they hold for any user. Evidence:
`TestTheTesterBriefingGivesTheConductorsPidOrchestratorURLAndStateDirectory`,
`TestTheTesterBriefingNamesNoToolsOrFileToSource`,
`TestNoPromptKmGivesAChairNamesCaoServerTmuxPgrepPkillOrKillall` and
`TestRunAndPlayLeaveTheScratchDirectoryEmptyOfAnythingKmWrote`. The test step must also record a
real nested run and unchanged conductor agent-store checksums, session list and process liveness
in this run's test report.

## 2026-11-12: Read Copilot and Antigravity usage from per-request session records

Copilot CLI 1.0.109 records per-request tokens alongside cumulative checkpoint and shutdown totals;
only requests are counted. Its input includes cache reads and writes, which must be subtracted.
Copilot output already includes reasoning: a real 1.0.109 session's request outputs summed to 362,
matching its footer and shutdown output total, with 45 reasoning tokens shown as a subset. Output
is kept unchanged so reasoning is counted once.
Antigravity 1.5.6 transcripts record requests with input already excluding cache reads, but no model,
cache writes or separate thinking count. Resumed real sessions confirmed the append-only windows.
Evidence: `TestCopilotAndAntigravityStepsSumTheirRequestsWithoutCountingCumulativeRecords`,
`TestResumedCopilotAndAntigravityStepsCountOnlyRequestsAppendedDuringTheStep`,
`TestInvalidCopilotRequestCountsMakeTheWholeStepUnknown`,
`TestACopilotStepsOutputIncludesReasoningWithoutAddingItAgain` and
`TestConcurrentCopilotStepsKeepTheirOwnOutputIncludingReasoning`.

## 2026-11-12: Explicit installation of embedded base chair profiles

`km profiles install` installs bases for available or explicitly named built-in agents. Base profiles
remain a single copy in `profiles/`, embedded from that directory. Installation stays outside runs
because it writes shared CAO state and kits need a command they can call once. Differing km-authored
profiles require `-replace`; user profiles are never replaced. The existing CAO home proof and
live-run guard protect this path too. Real isolated CAO 2.5.4 copied profiles byte-for-byte, silently
overwrote a same-stem profile, and refused another source with the same name while exiting zero.
Therefore km classifies before installing and reads every install back; CAO parses the installed
profile's description for the authorship check. Evidence:
`TestTheEmbeddedBaseProfilesAreByteIdenticalToTheFilesUnderProfiles`,
`TestProfilesInstallRunAgainReportsEveryProfileUnchangedAndRunsNoInstaller`,
`TestProfilesInstallNeverReplacesAProfileKmDidNotAuthorEvenWithReplace`,
`TestProfilesInstallRefusesWhenTheCAOServerDoesNotUseTheCAOHome`,
`TestProfilesInstallRefusesReplacementWhileAnotherRunThatMayDependOnTheProfileIsAlive` and
`TestProfilesInstallFailsWhenCaoExitsZeroButTheProfileIsNotWhatKmSent`.

## 2026-11-12: Opt in to reporting parts and models from telemetry

`km report -parts` appends part/provider/model rows and adds a JSON `parts` object after the
same copy resolution and filters. Plain report keeps its output and first-line-only telemetry
reads; always reading full telemetry would add cost to report, status and profile guards.
A separate command would duplicate scanning and filtering. Rows prefer the configured chair model,
except when CAO explicitly reported that it ran another model. Verdict attribution matches subject,
round and chair name, with the last record winning; downstream facts can use unattributed verdicts.
A recorded review step missing its verdict prevents claiming unanimous approval.
Planner and implementer outcomes apply only when one model/provider played that part in the run;
escalations across models count as split runs. Counts and denominators describe facts, with no score
or ranking. Evidence: `TestPlainReportIgnoresTheStepRecordsAndPrintsWhatItAlwaysPrinted`,
`TestReportPartsShowsSeparateRowsForTwoModelsPlayingTheSamePart`,
`TestReportPartsAttributesVerdictsByRoundAndChairAndCountsDownstreamFacts`,
`TestReportPartsCountsRunOutcomesOnlyForPartsPlayedByOneModel` and
`TestReportPartsReadsARunRecordedByTheLoopItself`.


## 2026-11-13: Detach one run process and wait on its record

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


## 2026-11-13: Require a captured run id before waiting

The detached-run live test found `km wait ""` could report an unrelated run's success when a
script failed to capture its id. Reject blank RUN values before discovery and require at least
two characters for prefixes; exact ids still win, including short ids in older records. This
narrows the prefix rule above. Evidence:
`TestWaitRefusesAnEmptyRunInsteadOfReportingAnotherRunsResult`,
`TestWaitRefusesAOneCharacterPrefixEvenWhenOnlyOneRunMatches` and
`TestWaitStillAcceptsTwoCharacterPrefixesAndExactOneCharacterRunIds`.


## 2026-11-13: Keep MCP stdio behind a small protocol seam without a dependency

Five run tools use the existing command implementations through `internal/mcp`, which owns only
stdio framing, lifecycle, tool dispatch and cancellation. No dependency is added: the maintained
official Go MCP SDK includes transport, authentication and schema surfaces beyond these tools.
This follows the 2026-11-05 narrow-seam decision; reconsider the SDK when km needs resources,
prompts, sampling, a non-stdio transport or authentication. Evidence: protocol tests in
`internal/mcp/server_test.go` and subprocess tool tests in `cmd/km/mcp_test.go`; Claude Code
2.1.323 negotiated 2025-11-25, listed the tools and called status in an isolated live check.
The specification site required network approval, so supported revisions follow the tested client
and the plan's fallback list (2025-11-25, 2025-06-18, 2025-03-26, 2024-11-05).


## 2026-11-13: Report directories add to the state runs directory

This reverses the documented choice that a non-empty `[report] dirs` replaced `<state>/runs`.
Adding kept records hid a person's own runs, and a copied user config naming missing directories
left bare commands unable to find local runs. Bare report, status, wait and their MCP tools now
search the default first, then the configured additions, counting the default once even if listed.
A missing default stays silent; missing configured additions remain visible. Named directories
still replace both without reading user config. Errors retain the source of the failing root.
Evidence: `TestConfiguredReportDirsAreSearchedInAdditionToTheStateRuns`,
`TestWaitFindsARunInTheStateRunsWhenReportDirsNamesAnotherDirectory`,
`TestListingTheStateRunsInReportDirsSearchesItOnceAndCountsEachRunOnce`,
`TestAMissingStateRunsIsSilentWhetherOrNotReportDirsIsSet`,
`TestAnErrorFromTheStateRunsIsNotAttributedToReportDirs` and
`TestMcpToolsFindTheStateRunsWhenReportDirsNamesAMissingDirectory`.

## 2026-11-13: Init writes one shape and detection supplies hints

Across 143 real repositories, detection drafted a command for 84 and wrote nothing for 59.
Root files cannot identify a repository's test target, CI commands or service requirements.
A verify that passes while checking nothing would be trusted by every later step. Init now
writes config pointing to `scripts/verify` and a failing, marked stub everywhere, preserving
existing setup. Only `-verify CMD` supplies a command; detection reports hints without writing
them. Evidence: `TestInitWritesTheConfigAndAStubInARepositoryWithNoRecognisedToolchain`,
`TestInitWritesTheSameFilesWhateverToolchainIsPresent` and
`TestTheStubIsRecognisedByItsMarkerAndNotByItsWording`.

## 2026-11-13: Prove verify with two clean passes and deliberate breaks

`km prove` uses fresh copies of HEAD with only the current config and verify script overlaid.
Two passes expose flakiness; at least one break patch must make verify fail before it is proved.
Local clone and detached checkout preserve a real Git repository without network or identity,
and avoid archive export-ignore/export-subst rewriting. A snapshot before and after applying
patches protects setup files, including renames, additions, deletions, symlinks and mode changes,
without owning a patch parser. Copies and temporary logs are removed even after a handled signal.

Exit statuses: 0 proved; 1 command/Git/filesystem error or interrupted; 3 timeout; 10 refused
(stub, missing verify, invalid checkout or bad/setup-touching patch); 11 passes but not proved
(no break patch); 12 clean failure or flaky; 13 a break patch left verify passing. Interruption
and timeout stop immediately, refusal takes precedence over surviving patches, and clean
failure skips patches. Evidence: `TestProveProvesWhenEveryBreakPatchMakesVerifyFail`,
`TestProveReportsAVerifyThatPassesOnlyOnceAsFlaky`,
`TestProveRefusesAPatchThatTouchesKapellmeisterOrScriptsVerify`,
`TestProveLeavesTheCheckoutUnchangedAndRemovesItsScratchCopies` and
`TestProveRemovesItsScratchCopiesWhenInterrupted`.

## 2026-11-13: Tune verify with two chairs and let prove judge

Tune seats the configured implementer and first reviewer; one available agent can play both.
Writing the command and reading it against CI need judgement, while planner, gate and classifier
add no judgement that `km prove` needs. Approval requires km's own proof and the reviewer's
approval in the same round. Tune carries one procedure for the guide and task; the Claude Code
skill reads the guide. Work starts from HEAD in a detached worktree with current setup overlaid.
Only `scripts/verify` and `.kapellmeister/config.toml` are copied back, after checking that the
proved setup and the person's setup have not changed. Break patches stay in the run record.
The agent-step timeout is configurable; each proof verification retains prove's 10m default.
Evidence: `TestTuneVerifyIsApprovedOnlyWhenKmProveReportsProvedAndLeavesOnlySetupUncommitted`,
`TestAnUnprovedOrOutOfScopeTuneIsRefusedAndLeavesTheCheckoutUntouched`,
`TestTuneVerifyPreflightFailsFirstAndNamesOneFixPerFailure`,
`TestATuneIsRecordedAsARunThatStatusWaitAndReportSee` and real Claude runs
`20261113-130007-8c6a0d` (detached) and `20261113-130600-d56805` (foreground, `-again`), both
approved with two clean passes and failing source and test breaks. This extends the init and
prove decisions above; normal run still refuses a failing baseline.


## 2026-11-13: Opt into Codex folder trust and retry a preflight check timeout once

`[run] trust_folder` defaults off because editing agent configuration and granting trust require
a person's opt-in. It is user-only: a repository must not grant itself trust. The CAO client
launch seam covers all chair starts. km appends only, preserving existing bytes, entries and
unparseable files; it records the main repository root because live Codex 0.167.0 probes on
2026-11-13 wrote that key from linked worktrees and subdirectories, resolving symlinks.
Claude Code, Copilot CLI and Antigravity need no km change because CAO 2.5.4 accepts their dialogs.
Codex's per-launch override cannot pass CAO's scalar-only `codexConfig` and restricted keys.
Off cannot promise a folder stays untrusted: km cannot stop CAO accepting recognised dialogs.
For Codex, off retains the first-message loss in awslabs/cli-agent-orchestrator#1319.

A preflight check timeout now tears down that chair and seats it once more; Codex saves trust
when the first message's Enter accepts its dialog, so the next attempt can answer. This narrows
the 2026-11-11 no-timeout-retry decision: seat timeouts, blockers, cancellation and real work
still get no added retry. The second timeout retains its attach target. Evidence:
`TestCodexEntryIsAppendedOnceAndTheRestOfTheFileIsByteIdentical`,
`TestAnExistingEntryForTheFolderIsLeftAloneWhateverItsTrustLevel`,
`TestTrustFolderBelongsInTheUserConfigAndIsRefusedInTheRepositoryConfig`,
`TestTheTrustKeyIsTheMainRepositoryRootForALinkedWorktreeAndASubdirectory`,
`TestPreflightTearsDownAChairThatTimedOutAndStartsItOnceMoreReportingTheRetry` and
`TestPreflightKeepsTheSecondTimeoutAndItsAttachTargetAfterOneRetry`.
