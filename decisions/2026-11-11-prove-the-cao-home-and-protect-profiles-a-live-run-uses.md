# Prove the CAO home and protect profiles a live run uses

`cao install` writes into the calling shell's `CAO_HOME_DIR`, whatever server URL is in use.
Against an isolated CAO 2.5.4, km's preflight overwrote a healthy `km-claude-high.md` that live
chairs could have been playing from, replacing its Bash, Read and Write permissions with
`allowedTools: []`.

Before installing, km creates a random `km-probe-*` profile through the server (POST
`/agents/profiles`), checks its exact bytes in the local home, and deletes it. Health and settings
do not expose the CAO home, and comparing existing profiles cannot tell two homes with identical
profiles apart. Unavailable or forbidden write routes fail closed. No probe occurs without pending
installations. The probe shows where the server writes at that moment; it cannot protect against a
later restart with another home or a concurrent writer.

All pending profile replacements are checked before any is written. A replacement is refused while
another unfinished run recorded in the state directory is alive; new or byte-identical profiles
remain allowed. This protects only recorded runs in the selected state directory, not arbitrary CAO
users.

CAO's content-write API does not materialize provider artifacts, and its install API takes a name
or URL rather than supplied content. Propose installation from supplied content upstream so km can
stop owning this proof. Evidence:
`TestAProfileReplacementIsRefusedNamingTheProfileAndTheLiveRunThatMayDependOnIt`,
`TestEveryPendingProfileIsCheckedBeforeAnyVariantIsWrittenOrInstalled` and
`TestInstallingIsRefusedWhenTheCAOServerDoesNotWriteIntoTheCAOHomeCaoInstallWritesTo`;
`spike/SPIKE.md`, finding 27.
