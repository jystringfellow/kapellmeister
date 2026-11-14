# Lessons reach chairs only through checked-in, reviewed files

Lessons come only from files committed under `.kapellmeister/learned/`, read once at the run's
base commit. A person reviews them through the same pull request process as code; km never writes
there or reads lessons from agent-written memory. This prevents a bad run from teaching later
steps or runs without review. Evidence: `TestLessonsAreReadFromTheBaseCommitNotTheWorkingFiles`,
`TestALessonWrittenByAStepIsNotSeenByALaterStepOfTheSameRun` and
`TestKmNeverWritesUnderTheLearnedDirectory`.

Chairs receive exactly the reviewed bytes. Committed lessons are read from git's stdout only.
Files with invalid UTF-8, invisible or direction-changing characters or section markers are
refused rather than escaped: a skipped file stays visible in warnings and records, while escaping
would give chairs text that differs from what was reviewed. Evidence:
`TestUnsafeLessonFilesAreSkippedWithAWarning` and
`TestALessonFileContainingAMarkerCannotEndTheSectionEarly`.
