---
name: hooks
description: A simple agent
hooks:
  PreToolUse:
    - matcher: "^(Edit|Write|Bash)"
      hooks:
        - type: command
          command: "$CLAUDE_PROJECT_DIR/.claude/hooks/x.sh"
          timeout: 60
          statusMessage: "Checking scope..."
---
