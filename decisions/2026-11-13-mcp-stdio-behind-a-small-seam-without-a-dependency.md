# Keep MCP stdio behind a small protocol seam without a dependency

Five run tools use the existing command implementations through `internal/mcp`, which owns only
stdio framing, lifecycle, tool dispatch and cancellation. No dependency is added: the maintained
official Go MCP SDK includes transport, authentication and schema surfaces beyond these tools.
This follows the 2026-11-05 narrow-seam decision; reconsider the SDK when km needs resources,
prompts, sampling, a non-stdio transport or authentication. Evidence: protocol tests in
`internal/mcp/server_test.go` and subprocess tool tests in `cmd/km/mcp_test.go`; Claude Code
2.1.323 negotiated 2025-11-25, listed the tools and called status in an isolated live check.
The specification site required network approval, so supported revisions follow the tested client
and the plan's fallback list (2025-11-25, 2025-06-18, 2025-03-26, 2024-11-05).
