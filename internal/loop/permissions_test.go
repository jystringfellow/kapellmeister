package loop

import "testing"

func TestPermissionLabelNamesEachProvidersRealLaunchMode(t *testing.T) {
	for _, tt := range []struct {
		name, provider string
		permissions    Permissions
		label          string
		unguarded      bool
	}{
		{"claude default", "claude_code", Permissions{}, "skip-permissions (CAO default)", true},
		{"claude wildcard", "claude_code", Permissions{AllowedTools: []string{"*"}}, "skip-permissions (CAO default)", true},
		{"claude guarded", "claude_code", Permissions{PermissionMode: "default"}, "permission-mode default", false},
		{"claude edits", "claude_code", Permissions{PermissionMode: "acceptEdits"}, "permission-mode acceptEdits", false},
		{"claude plan", "claude_code", Permissions{PermissionMode: "plan"}, "permission-mode plan", false},
		{"claude auto", "claude_code", Permissions{PermissionMode: "auto"}, "permission-mode auto", false},
		{"claude bypass", "claude_code", Permissions{PermissionMode: "bypassPermissions"}, "permission-mode bypassPermissions", true},
		{"copilot", "copilot_cli", Permissions{PermissionMode: "default", AllowedTools: []string{}}, "allow-all (fixed by CAO)", true},
		{"antigravity", "antigravity_cli", Permissions{PermissionMode: "default"}, "skip-permissions (fixed by CAO)", true},
		{"codex absent", "codex", Permissions{}, "yolo", true},
		{"codex empty", "codex", Permissions{AllowedTools: []string{}}, "yolo", true},
		{"codex restricted", "codex", Permissions{AllowedTools: []string{"Read"}}, "yolo", true},
		{"codex named profile", "codex", Permissions{CodexProfile: "safe", AllowedTools: []string{"Read"}}, "approvals", false},
		{"codex named profile without tools", "codex", Permissions{CodexProfile: "safe"}, "approvals", false},
		{"codex named profile with empty tools", "codex", Permissions{CodexProfile: "safe", AllowedTools: []string{}}, "approvals", false},
		{"codex wildcard overrides named profile", "codex", Permissions{CodexProfile: "safe", AllowedTools: []string{"*"}}, "yolo", true},
		{"codex wildcard", "codex", Permissions{AllowedTools: []string{"Read", "*"}}, "yolo", true},
		{"unknown", "other", Permissions{AllowedTools: []string{"*"}}, "unknown", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			label, unguarded := PermissionLabel(tt.provider, tt.permissions)
			if label != tt.label || unguarded != tt.unguarded {
				t.Fatalf("permissions = %q, %t; want %q, %t", label, unguarded, tt.label, tt.unguarded)
			}
		})
	}
}

func TestCodexWithoutANamedProfileRunsUnguardedEvenWithoutWildcardTools(t *testing.T) {
	for _, tools := range [][]string{nil, {}, {"Read"}} {
		label, unguarded := PermissionLabel("codex", Permissions{AllowedTools: tools})
		if label != "yolo" || !unguarded {
			t.Errorf("tools %v: label = %q, unguarded = %t; want yolo, true", tools, label, unguarded)
		}
	}
}

func TestUnknownClaudePermissionModesAreReportedAsUnknown(t *testing.T) {
	for _, mode := range []string{"yolo", "unrecognised", "default\n", "default\x1b[31m"} {
		label, _ := PermissionLabel("claude_code", Permissions{PermissionMode: mode})
		if label != "unknown" {
			t.Errorf("mode %q: label = %q, want unknown", mode, label)
		}
	}
}
