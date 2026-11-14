# The tester part tests the change live, blind to the implementation

Reviewers read the diff and verify runs unit tests, leaving real use unexercised. An optional
tester writes a blind test plan alongside round 1's implementation, then plays it back after the
gate approves. Failures send the work back for another round, starting with failing regression
tests. Parts keep plain names; chairs may carry orchestral names, such as `tonmeister`.
Evidence: two km-built PRs passed checks and won reviewer and gate approval, yet live use found
`km play` starting on a half-briefed ensemble and CAO answering DELETE with 409 for a terminal
whose GET was already 404. Tests:
`TestATestPlanStartsAfterThePlanAndRunsAlongsideRoundOnesImplementer` and
`TestATestChangesStartsAnotherRoundWithTheTestReportLeadingTheFeedback`.
