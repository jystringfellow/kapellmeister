# What a repository tells its ensemble lives in the repository, not in km

How to run this repository's software live is knowledge about this repository, so it is checked in
here rather than built into km. Rules that must hold go in `AGENTS.md`, "Exercising km live", which
every chair is told to read and follow. `scripts/nested-km-env` writes the isolated environment
those rules require. `.kapellmeister/learned/` holds advice for one part and points to the rules
rather than repeating them, because lessons rank below instruction files and the budget can cut
them. This follows "Name things from orchestral practice" (2026-11-05), which put the vocabulary in
`AGENTS.md` so that every chair sees it. Evidence: two incidents while km ran on its own
repository. A tester signalled the first process named km and ended the run conducting it. The profiles live chairs were playing from were overwritten;
`AGENTS.md` then asked a chair to isolate only `KAPELLMEISTER_STATE_DIR`, and an isolated run
showed that `cao install` writes to the calling shell's `CAO_HOME_DIR` whatever the server URL
(`spike/SPIKE.md`, finding 27). Both come from running a conductor under a conductor, which other
repositories do not do. Tests:
`TestTheEnvFileMovesKmCaoAndTmuxUnderTheScratchDirectoryAndClearsWhatTheChairInherited`,
`TestTheEnvFileQuotesScratchPathsWithSpacesQuotesAndDollars` and
`TestATmuxSocketPathOver100BytesIsRefusedAndNoEnvFileIsWritten`.
