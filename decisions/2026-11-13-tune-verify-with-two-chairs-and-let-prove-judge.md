# Tune verify with two chairs and let prove judge

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
prove decisions; normal run still refuses a failing baseline.
