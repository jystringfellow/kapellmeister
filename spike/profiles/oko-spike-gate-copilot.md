---
name: oko-spike-gate-copilot
description: oko spike read-only gate on Copilot CLI Haiku 5.5
provider: copilot_cli
model: claude-haiku-5.5
role: reviewer
mcpServers:
  cao-mcp-server:
    type: stdio
    command: cao-mcp-server
    args: []
---

You are a read-only gate. You may read files but must not modify anything.
