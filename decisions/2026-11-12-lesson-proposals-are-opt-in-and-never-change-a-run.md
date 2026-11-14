# Lesson proposals are opt-in and never change a run

The gate proposes candidate lessons after the score ends; km records them in state and never
reads them back. Collection may never change a run's outcome or exit code, including on timeout,
invalid replies or a failed write.

Collection is off by default: it costs up to five minutes after every run, and nothing in km reads
the proposals. This repository opts in with `[run] lessons = true`; a person reviews proposals
before checking useful lessons in under `.kapellmeister/learned/`. Evidence:
`TestARunHasTheSameOutcomeRoundsAndReasonWithAndWithoutTheLessonsStep`,
`TestAFailedLessonsWriteDoesNotFailTheRun`, `TestKmNeverReadsLessonsJSONBack`,
`TestLessonsAreOffByDefaultWithAFiveMinuteLimitWhenTurnedOn` and
`TestADefaultRunMakesNoLessonsStep`.
