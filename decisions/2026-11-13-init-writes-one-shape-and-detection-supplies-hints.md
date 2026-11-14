# Init writes one shape and detection supplies hints

Across 143 real repositories, detection drafted a command for 84 and wrote nothing for 59.
Root files cannot identify a repository's test target, CI commands or service requirements.
A verify that passes while checking nothing would be trusted by every later step. Init now
writes config pointing to `scripts/verify` and a failing, marked stub everywhere, preserving
existing setup. Only `-verify CMD` supplies a command; detection reports hints without writing
them. Evidence: `TestInitWritesTheConfigAndAStubInARepositoryWithNoRecognisedToolchain`,
`TestInitWritesTheSameFilesWhateverToolchainIsPresent` and
`TestTheStubIsRecognisedByItsMarkerAndNotByItsWording`.
