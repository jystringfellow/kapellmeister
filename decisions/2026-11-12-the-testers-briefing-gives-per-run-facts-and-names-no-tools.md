# The tester's briefing gives per-run facts and names no tools

km's briefing gives the tester the conductor's PID, its orchestrator URL and its state directory,
identified as outside the test and protected from signals, stops and changes. Signals are
restricted to processes the tester started and recorded in `<scratch>/pids`. The briefing names no
tools or process-finding commands, and km writes no environment file for the tester. A run was
lost when a tester signalled the first process named km.

Nested-conductor setup and tool-specific safety rules live in `AGENTS.md`, "Exercising km live",
`scripts/nested-km-env` and `.kapellmeister/learned/tester.md`, because they are knowledge about
this repository. The CAO-home probe and the live-run profile guard remain in km because they hold
for any user. Evidence:
`TestTheTesterBriefingGivesTheConductorsPidOrchestratorURLAndStateDirectory`,
`TestTheTesterBriefingNamesNoToolsOrFileToSource`,
`TestNoPromptKmGivesAChairNamesCaoServerTmuxPgrepPkillOrKillall` and
`TestRunAndPlayLeaveTheScratchDirectoryEmptyOfAnythingKmWrote`.
