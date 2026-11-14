# A number never looks more complete than it is

Wrong usage is worse than unknown. A step's usage is recorded only when exactly one growing log
names its reply path, even when only one grew; anything else is unknown. Claude subagent logs
count with their parent session: a real Claude log had 489 parent output tokens and 502 separate
subagent output tokens, so omitting subagents would under-count.

Report uses only usage recorded in result.json and never shows unmeasured usage as zero. A partly
measured run, such as one with a chair whose tool keeps no usage log, shows what was measured as a
lower bound marked `≥`, because treating it as unknown hid the usage of every run that seats such
a chair. Invalid or missing counters are unknown. Totals exclude unknown runs and count them
explicitly. Evidence:
`TestOneGrownLogThatDoesNotNameTheReplyPathIsUnknownAsAmbiguous`,
`TestAClaudeStepsUsageIncludesItsSubagentTranscripts`,
`TestReportShowsUnknownUsageForARunWithoutUsageOrWithUnknownUsageNeverZero`,
`TestReportShowsPartlyMeasuredUsageAsALowerBoundAndSaysHowManyRunsArePartlyMeasured` and
`TestReportTotalsByEnsembleAndRepositorySumOnlyKnownUsageAndCountRunsLeftOut`.
