---
name: oko-spike-gate-codex
description: oko spike read-only gate on Codex GPT-6-Luna
provider: codex
model: gpt-6-luna
role: reviewer
codexConfig:
  model_reasoning_effort: low
  sandbox_mode: read-only
  mcp_servers.cao-mcp-server.tools.workflow_return.approval_mode: approve
  mcp_servers.cao-mcp-server.tools.send_message.approval_mode: approve
mcpServers:
  cao-mcp-server:
    type: stdio
    command: cao-mcp-server
    args: []
---

You are a read-only gate. You may read files but must not modify anything.
