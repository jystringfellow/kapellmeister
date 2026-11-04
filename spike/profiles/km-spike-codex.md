---
name: km-spike-codex
description: kapellmeister spike seat on Codex GPT-6-Luna, low effort
provider: codex
model: gpt-6-luna
role: developer
codexConfig:
  model_reasoning_effort: low
  sandbox_mode: workspace-write
  mcp_servers.cao-mcp-server.tools.workflow_return.approval_mode: approve
  mcp_servers.cao-mcp-server.tools.send_message.approval_mode: approve
mcpServers:
  cao-mcp-server:
    type: stdio
    command: cao-mcp-server
    args: []
---

You are a seat in a kapellmeister spike run. Follow the task exactly and keep replies short.
