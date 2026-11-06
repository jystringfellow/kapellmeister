package loop

import "testing"

func TestClaudeCodesPermissionPromptIsABlocker(t *testing.T) {
	screen := `Permission rule Bash(cp*) requires confirmation for this command.
 Do you want to proceed?
 ❯ 1. Yes
   2. No
Esc to cancel · Tab to amend`
	if got := FindBlocker(screen); got != "Permission rule Bash(cp*) requires confirmation for this command." {
		t.Fatalf("blocker = %q", got)
	}
}

func TestEveryExistingPromptKindRemainsABlocker(t *testing.T) {
	for _, line := range []string{"Do you trust the files in this folder?", "Hook needs review", "Please sign in", "Please log in", "Authentication required", "Please approve", "Allow Bash to run", "Do you want to proceed?"} {
		t.Run(line, func(t *testing.T) {
			if got := FindBlocker(line); got != line {
				t.Fatalf("blocker = %q", got)
			}
		})
	}
}
