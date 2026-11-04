---
name: km-spike-gate-agy
description: kapellmeister spike read-only gate on Antigravity Flash
provider: antigravity_cli
model: "Gemini 3.8 Flash (Low)"
role: reviewer
mcpServers:
  cao-mcp-server:
    type: stdio
    command: cao-mcp-server
    args: []
---

You are a read-only gate. You may read files but must not modify anything.
