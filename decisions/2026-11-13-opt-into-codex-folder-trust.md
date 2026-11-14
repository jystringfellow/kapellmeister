# Opt into Codex folder trust

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
when the first message's Enter accepts its dialog, so the next attempt can answer. Seat timeouts,
blockers, cancellation and real work get no added retry. The second timeout retains its attach target. Evidence:
`TestCodexEntryIsAppendedOnceAndTheRestOfTheFileIsByteIdentical`,
`TestAnExistingEntryForTheFolderIsLeftAloneWhateverItsTrustLevel`,
`TestTrustFolderBelongsInTheUserConfigAndIsRefusedInTheRepositoryConfig`,
`TestTheTrustKeyIsTheMainRepositoryRootForALinkedWorktreeAndASubdirectory`,
`TestPreflightTearsDownAChairThatTimedOutAndStartsItOnceMoreReportingTheRetry` and
`TestPreflightKeepsTheSecondTimeoutAndItsAttachTargetAfterOneRetry`.
