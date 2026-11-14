# Prove verify with two clean passes and deliberate breaks

`km prove` uses fresh copies of HEAD with only the current config and verify script overlaid.
Two passes expose flakiness; at least one break patch must make verify fail before it is proved.
Local clone and detached checkout preserve a real Git repository without network or identity,
and avoid archive export-ignore/export-subst rewriting. A snapshot before and after applying
patches protects setup files, including renames, additions, deletions, symlinks and mode changes,
without owning a patch parser. Copies and temporary logs are removed even after a handled signal.

Exit statuses: 0 proved; 1 command/Git/filesystem error or interrupted; 3 timeout; 10 refused
(stub, missing verify, invalid checkout or bad/setup-touching patch); 11 passes but not proved
(no break patch); 12 clean failure or flaky; 13 a break patch left verify passing. Interruption
and timeout stop immediately, refusal takes precedence over surviving patches, and clean
failure skips patches. Evidence: `TestProveProvesWhenEveryBreakPatchMakesVerifyFail`,
`TestProveReportsAVerifyThatPassesOnlyOnceAsFlaky`,
`TestProveRefusesAPatchThatTouchesKapellmeisterOrScriptsVerify`,
`TestProveLeavesTheCheckoutUnchangedAndRemovesItsScratchCopies` and
`TestProveRemovesItsScratchCopiesWhenInterrupted`.
