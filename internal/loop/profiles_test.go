package loop

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestAnEffortProfileKeepsTheBasesPermissionMode(t *testing.T) {
	source, err := EffortProfileSource("claude_code", "km-claude", "high", Permissions{PermissionMode: "default"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(source, "\npermissionMode: default\n") {
		t.Fatalf("profile = %s", source)
	}
}

func TestAnEffortProfileKeepsTheBasesAllowedTools(t *testing.T) {
	for _, tools := range [][]string{{"*"}, {"Read", "Bash"}, {}} {
		want := `allowedTools: ["*"]`
		if len(tools) == 0 {
			want = "allowedTools: []"
		} else if len(tools) == 2 {
			want = `allowedTools: ["Read","Bash"]`
		}
		for _, provider := range []string{"claude_code", "codex"} {
			source, err := EffortProfileSource(provider, map[string]string{"claude_code": "km-claude", "codex": "km-codex"}[provider], "high", Permissions{AllowedTools: tools})
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(source, "\n"+want+"\n") {
				t.Fatalf("profile lacks %s: %s", want, source)
			}
		}
	}
}

func TestAnEffortProfileForABaseWithNoPermissionsIsUnchanged(t *testing.T) {
	for _, tt := range []struct{ provider, base, config string }{
		{"claude_code", "km-claude", "claudeConfig:\n  effort: high\n"},
		{"codex", "km-codex", "codexConfig:\n  model_reasoning_effort: high\n"},
	} {
		source, err := EffortProfileSource(tt.provider, tt.base, "high", Permissions{})
		if err != nil {
			t.Fatal(err)
		}
		want := fmt.Sprintf("---\nname: %s-high\ndescription: kapellmeister chair at high effort; kapellmeister passes the model per step\nprovider: %s\nrole: developer\n%s---\n\nYou are a chair in a kapellmeister engineering loop. Follow each step's instructions exactly, write requested files exactly where asked, and keep chat replies short.\n", tt.base, tt.provider, tt.config)
		if source != want {
			t.Fatalf("profile = %q, want %q", source, want)
		}
	}
}

func TestAnEffortProfileKeepsToolRulesContainingPercentSigns(t *testing.T) {
	source, err := EffortProfileSource("claude_code", "km-claude", "high", Permissions{AllowedTools: []string{"Bash(printf %s)"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(source, `allowedTools: ["Bash(printf %s)"]`+"\n") {
		t.Fatalf("profile = %s", source)
	}
}

func TestAnEffortProfileKeepsTheBasesNamedCodexProfile(t *testing.T) {
	for _, profile := range []string{"safe", "policy: #safer", "percent%s", "quote\"and\\slash"} {
		source, err := EffortProfileSource("codex", "km-codex", "high", Permissions{AllowedTools: []string{"Read"}, CodexProfile: profile})
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(profile)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(source, "\ncodexProfile: "+string(encoded)+"\n") {
			t.Errorf("profile name lost: %s", source)
		}
	}
}
