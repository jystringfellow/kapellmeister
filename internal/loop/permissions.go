package loop

import "slices"

type Permissions struct {
	PermissionMode string
	CodexProfile   string
	AllowedTools   []string
}

func PermissionLabel(provider string, p Permissions) (string, bool) {
	switch provider {
	case "claude_code":
		switch p.PermissionMode {
		case "":
			return "skip-permissions (CAO default)", true
		case "default", "acceptEdits", "plan", "auto", "bypassPermissions":
			return "permission-mode " + p.PermissionMode, p.PermissionMode == "bypassPermissions"
		default:
			return "unknown", false
		}
	case "copilot_cli":
		return "allow-all (fixed by CAO)", true
	case "antigravity_cli":
		return "skip-permissions (fixed by CAO)", true
	case "codex":
		if p.CodexProfile == "" || slices.Contains(p.AllowedTools, "*") {
			return "yolo", true
		}
		return "approvals", false
	default:
		return "unknown", false
	}
}
