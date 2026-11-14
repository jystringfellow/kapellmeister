# Report chair permissions and inherit them in effort profiles

km reports permission modes without enforcing them. Labels come from CAO's profile HTTP endpoint
and model each provider's real launch: Claude's `permissionMode`, Copilot and Antigravity's fixed
skip flags, and Codex's `codexProfile`, without which CAO launches Codex with `--yolo` even when
`allowedTools` lacks `*`. Generated effort profiles copy the base's `permissionMode`,
`allowedTools` and `codexProfile`, and installed variants are reinstalled when they differ, because
a variant otherwise silently drops the base's choice. Evidence:
`TestPermissionLabelNamesEachProvidersRealLaunchMode`,
`TestCodexWithoutANamedProfileRunsUnguardedEvenWithoutWildcardTools`,
`TestAnEffortProfileKeepsTheBasesPermissionMode`,
`TestEnsureProfilesReinstallsAVariantWhosePermissionsDifferFromItsBase`,
`TestEnsureProfilesKeepsAndSynchronizesTheBasesNamedCodexProfile` and
`TestConfigShowsTheBasePermissionsThatTheNextRunWillSynchronize`; CAO 2.5.4 findings 21 and 22 in
`spike/SPIKE.md`.
