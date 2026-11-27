# Launch options are named CAO profile fields, not argv

Use named CAO profile fields for launch options. CAO 2.5.4 provides `native_agent` for Claude Code.

We rejected an open list of per-chair command-line options. It could widen permissions
(`--dangerously-*`, `--permission-mode`, `--settings`, `--add-dir`), load code
(`--plugin-dir`, `--mcp-config`), or override what km owns (`--model`, `--effort`, `--resume`).
