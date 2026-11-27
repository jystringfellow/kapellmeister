---
name: combined
description: "A general agent: use this for a user’s request — it checks paths / and people's work. A general agent: use this for a user’s request — it checks paths / and people's work. A general agent: use this for a user’s request — it checks paths / and people's work. A general agent: use this for a user’s request — it checks paths / and people's work. "
tools: Read, Glob, Grep, Bash, Write
disallowedTools: Bash(git push:*), Bash(rm:*), WebFetch, Bash(git push:*), Bash(rm:*), WebFetch, Bash(git push:*), Bash(rm:*), WebFetch, Bash(git push:*), Bash(rm:*), WebFetch, Bash(git push:*), Bash(rm:*), WebFetch, Bash(git push:*), Bash(rm:*), WebFetch, Bash(git push:*), Bash(rm:*), WebFetch, Bash(git push:*), Bash(rm:*), WebFetch, Bash(git push:*), Bash(rm:*), WebFetch, Bash(git push:*), Bash(rm:*), WebFetch, Bash(git push:*), Bash(rm:*), WebFetch, Bash(git push:*), Bash(rm:*), WebFetch
model: inherit
hooks:
  PreToolUse:
    - matcher: "^(Edit|Write|Bash)"
      hooks:
        - type: command
          command: |
            exit 0
            python3 <<'PY'
            [ text
            { text
            # text
            - text
            """
            key = {...}
            name: other
            text: value
            
                indented
            [ text
            { text
            # text
            - text
            """
            key = {...}
            name: other
            text: value
            
                indented
            [ text
            { text
            # text
            - text
            """
            key = {...}
            name: other
            text: value
            
                indented
            [ text
            { text
            # text
            - text
            """
            key = {...}
            name: other
            text: value
            
                indented
            [ text
            { text
            # text
            - text
            """
            key = {...}
            name: other
            text: value
            
                indented
            [ text
            { text
            # text
            - text
            """
            key = {...}
            name: other
            text: value
            
                indented
            [ text
            { text
            # text
            - text
            """
            key = {...}
            name: other
            text: value
            
                indented
            [ text
            { text
            # text
            - text
            """
            key = {...}
            name: other
            text: value
            
                indented
            [ text
            { text
            # text
            - text
            """
            key = {...}
            name: other
            text: value
            
                indented
            [ text
            { text
            # text
            - text
            """
            key = {...}
            name: other
            text: value
            
                indented
            [ text
            { text
            # text
            - text
            """
            key = {...}
            name: other
            text: value
            
                indented
            [ text
            { text
            # text
            - text
            """
            key = {...}
            name: other
            text: value
            
                indented
            [ text
            { text
            # text
            - text
            """
            key = {...}
            name: other
            text: value
            
                indented
            [ text
            { text
            # text
            - text
            """
            key = {...}
            name: other
            text: value
            
                indented
            [ text
            { text
            # text
            - text
            """
            key = {...}
            name: other
            text: value
            
                indented
            PY
          timeout: 30
          statusMessage: "Checking scope..."
---
