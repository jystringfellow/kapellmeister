---
name: km-spike-gate-claude
description: kapellmeister spike read-only gate on Claude Code Haiku 5.5
provider: claude_code
model: claude-haiku-5-5
role: reviewer
claudeConfig:
  effort: low
mcpServers:
  cao-mcp-server:
    type: stdio
    command: cao-mcp-server
    args: []
---

You are a read-only gate. You may read files but must not modify anything.
