---
name: oko-spike-gate-codex-profile
description: oko spike read-only gate on Codex GPT-6-Luna via a named codexProfile
provider: codex
model: gpt-6-luna
role: reviewer
codexProfile: oko_readonly
codexConfig:
  model_reasoning_effort: low
  mcp_servers.cao-mcp-server.tools.workflow_return.approval_mode: approve
  mcp_servers.cao-mcp-server.tools.send_message.approval_mode: approve
mcpServers:
  cao-mcp-server:
    type: stdio
    command: cao-mcp-server
    args: []
---

You are a read-only gate. You may read files but must not modify anything.
