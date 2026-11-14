# Explicit installation of embedded base chair profiles

`km profiles install` installs bases for available or explicitly named built-in agents. Base profiles
remain a single copy in `profiles/`, embedded from that directory. Installation stays outside runs
because it writes shared CAO state and kits need a command they can call once. Differing km-authored
profiles require `-replace`; user profiles are never replaced. The existing CAO home proof and
live-run guard protect this path too. Real isolated CAO 2.5.4 copied profiles byte-for-byte, silently
overwrote a same-stem profile, and refused another source with the same name while exiting zero.
Therefore km classifies before installing and reads every install back; CAO parses the installed
profile's description for the authorship check. Evidence:
`TestTheEmbeddedBaseProfilesAreByteIdenticalToTheFilesUnderProfiles`,
`TestProfilesInstallRunAgainReportsEveryProfileUnchangedAndRunsNoInstaller`,
`TestProfilesInstallNeverReplacesAProfileKmDidNotAuthorEvenWithReplace`,
`TestProfilesInstallRefusesWhenTheCAOServerDoesNotUseTheCAOHome`,
`TestProfilesInstallRefusesReplacementWhileAnotherRunThatMayDependOnTheProfileIsAlive` and
`TestProfilesInstallFailsWhenCaoExitsZeroButTheProfileIsNotWhatKmSent`.
