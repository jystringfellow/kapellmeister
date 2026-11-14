# Repositories check in ensembles as files that name only agents

Ensembles name agents, and agents depend on the user's accounts, the same reason `[chairs]` is
machine-only. So `[ensembles]` in the repository config is rejected; the user config accepts it.

Codeowners often know which ensemble gets the best results in their codebase, so a repository may
check in `.kapellmeister/ensembles/NAME.toml`, which seats chairs in sections. The machine-bound
part stays with the user. A repository file names only agents, each with an optional tier, never
profiles or models. Each chair lists agents in order of preference and plays the first one in the
user's `[agents] available`. This follows OpenRig's split between a portable spec and machine
bindings. A user's own file of the same name shadows the repository's. Evidence:
`TestARepositoryEnsembleNamesOnlyAgentsWhileAUserEnsembleMayNameAnyChair`,
`TestAChairPlaysTheFirstOfItsAgentsThatTheUserHas`,
`TestAUserEnsembleFileShadowsTheRepositorysOfTheSameName` and
`TestEnsemblesInTheRepositoryConfigAreRejected`.
