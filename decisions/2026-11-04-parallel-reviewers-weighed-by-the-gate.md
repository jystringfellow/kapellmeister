# Parallel reviewers, weighed by the gate

Reviewers share the worktree and are checked read-only as a group, with one snapshot before
starting and one after all finish. A failed reviewer informs the gate instead of failing the run;
if every reviewer fails, the run fails. The gate checks the diff and decides which findings block
and which are nits. Evidence: `TestReviewersRunInParallelAndTheGateReadsEveryReview`,
`TestAReviewerThatModifiesTheWorktreeFailsTheRunAndTheErrorNamesTheGroup`,
`TestAFailedReviewerIsRecordedAsFailedNamedToTheGateAndTheRoundContinues`, and
`TestTheGateSortsFindingsIntoBlockingAndNitsAndApprovesWhenNothingBlocks`.
