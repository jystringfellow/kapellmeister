# Bind agent names in user config and derive chairs from available agents

User-only `[agents.define]` maps short names to `provider:profile`, beside the ordered
`[agents] available` list. It reuses the chair grammar, keeps strict unknown-key checks, and avoids
colliding with `available` when a user names an agent that. Built-ins remain defaults for definitions
and can be overridden. Repositories may name these agents in ensemble files but cannot define them:
profiles and accounts are machine bindings, as in the 2026-11-05 ensemble decision. Evidence:
`TestTheUserConfigDefinesAnAgentAsProviderAndProfile`, `TestADefinitionOverridesABuiltInAgent`,
`TestAnAgentMayBeDefinedInTheUserConfigOnly` and `TestARepositoryEnsembleMayNameAUserDefinedAgent`.

Available agents have no implicit default. Ignoring duplicates, the first plans, gates and classifies,
the second implements (or the first when alone), and the third reviews (or the first with two).
This gives independent review whenever there is a choice. When claude, codex and copilot are all
available, preserve their existing assignments regardless of order or extras. Explicit chairs,
ensembles and flags override derived defaults. Missing required chairs fail before CAO. Evidence:
`TestOneAvailableAgentPlaysEveryChair`, `TestWithTwoAgentsTheReviewerIsNotTheImplementersAgent`,
`TestClaudeCodexAndCopilotKeepTodaysChairsInAnyOrder` and
`TestRunRefusesWithOneLineBeforeAnyCAOCallWhenNoAgentsAndNoChairsAreConfigured`.
