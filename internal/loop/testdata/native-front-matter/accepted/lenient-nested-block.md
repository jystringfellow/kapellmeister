---
name: lenient-nested-block
description: Use this agent when: the user asks for a review
hooks:
  PreToolUse:
    - matcher: "Edit|Write|Bash"
      hooks:
        - type: command
          command: |
            set -eu
            [ -n "$1" ] || exit 0
            # check: scope
            exit 0
          timeout: 30
meta:
  note: >-
    folded text
---
Body.
