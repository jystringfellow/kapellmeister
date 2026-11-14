# Restore the work before retrying a read-only chair

The gate wipe on 2026-11-13 showed detection alone loses an uncommitted implementation.
Reviewers, gate, tester, tune review and lessons now share snapshot, confirmed restoration
and one retry. A second violation, timeout or cancellation leaves the saved work intact;
lessons still discard invalid proposals without changing the outcome. HEAD moves fail without
rewriting the commit.

Snapshots are Git trees in a private temporary object directory with the repository objects
as alternates, plus the raw index. A temporary manifest in the run record is their only
reference; no refs, stash entries or snapshot objects enter the repository. This keeps linked
worktrees and staged state intact without copying unchanged repository contents. The directory
and manifest are removed at run end, except when restoration cannot be confirmed: the error
names the retained directory and tree. Ignored files, empty directories and nested repository
contents are outside this recovery.

Evidence: real Git regression tests
`TestTheGateThatResetsAndCleansTheWorktreeIsRestoredAndRetriedOnce`,
`TestASecondViolationRestoresAgainAndFailsTheRunWithTheWorkIntact`,
`TestRestoreBringsBackNewUntrackedFilesDeletionsStagedOnlyChangesModeChangesSymlinksAndTypeChangesExactly`,
`TestNothingIsLeftBehindInTheRepositoryAfterARun` and
`TestALessonsStepThatWipesTheWorktreeIsRestoredRetriedAndNeverFailsTheRun`.
