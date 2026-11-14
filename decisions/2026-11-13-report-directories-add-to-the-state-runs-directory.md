# Report directories add to the state runs directory

This reverses the documented choice that a non-empty `[report] dirs` replaced `<state>/runs`.
Adding kept records hid a person's own runs, and a copied user config naming missing directories
left bare commands unable to find local runs. Bare report, status, wait and their MCP tools now
search the default first, then the configured additions, counting the default once even if listed.
A missing default stays silent; missing configured additions remain visible. Named directories
still replace both without reading user config. Errors retain the source of the failing root.
Evidence: `TestConfiguredReportDirsAreSearchedInAdditionToTheStateRuns`,
`TestWaitFindsARunInTheStateRunsWhenReportDirsNamesAnotherDirectory`,
`TestListingTheStateRunsInReportDirsSearchesItOnceAndCountsEachRunOnce`,
`TestAMissingStateRunsIsSilentWhetherOrNotReportDirsIsSet`,
`TestAnErrorFromTheStateRunsIsNotAttributedToReportDirs` and
`TestMcpToolsFindTheStateRunsWhenReportDirsNamesAMissingDirectory`.
