package loop

import (
	"encoding/json"
	"fmt"
	"slices"
)

const chairInstructions = "You are a chair in a kapellmeister engineering loop. Follow each step's instructions exactly, write requested files exactly where asked, and keep chat replies short.\n"

var effortConfigKey = map[string]string{
	"claude_code": "claudeConfig:\n  effort: %s\n",
	"codex":       "codexConfig:\n  model_reasoning_effort: %s\n",
}

var generatedBases = map[string]string{
	"claude_code": "km-claude",
	"codex":       "km-codex",
}

func EffortProfileSource(provider, base, effort string, perms Permissions) (string, error) {
	if generatedBases[provider] != base {
		return "", fmt.Errorf("profile %s is not installed in CAO, and kapellmeister only generates effort profiles for %s; install a copy of %s with %s effort as %s",
			EffortProfile(base, effort), generatedBaseList(), base, effort, EffortProfile(base, effort))
	}
	if !slices.Contains(effortLevels[provider], effort) {
		return "", fmt.Errorf("%s does not support effort %q", provider, effort)
	}
	permissions := ""
	if perms.PermissionMode != "" {
		permissions += "permissionMode: " + perms.PermissionMode + "\n"
	}
	if perms.AllowedTools != nil {
		tools, err := json.Marshal(perms.AllowedTools)
		if err != nil {
			return "", err
		}
		permissions += "allowedTools: " + string(tools) + "\n"
	}
	if perms.CodexProfile != "" {
		profile, err := json.Marshal(perms.CodexProfile)
		if err != nil {
			return "", err
		}
		permissions += "codexProfile: " + string(profile) + "\n"
	}
	name := EffortProfile(base, effort)
	return fmt.Sprintf("---\nname: %s\ndescription: kapellmeister chair at %s effort; kapellmeister passes the model per step\nprovider: %s\nrole: developer\n%s%s---\n\n%s",
		name, effort, provider, permissions, fmt.Sprintf(effortConfigKey[provider], effort), chairInstructions), nil
}

func generatedBaseList() string {
	return generatedBases["claude_code"] + " and " + generatedBases["codex"]
}

type EffortVariant struct {
	Provider, Base, Effort string
}

func (v EffortVariant) Name() string { return EffortProfile(v.Base, v.Effort) }

func (p *Policy) EffortVariants(steps map[string][]Chair) []EffortVariant {
	if p == nil {
		return nil
	}
	seen := map[EffortVariant]bool{}
	var out []EffortVariant
	for _, key := range sortedKeys(steps) {
		step, ok := p.Steps[key]
		if !ok {
			continue
		}
		for _, chair := range steps[key] {
			if chair.Model != "" {
				continue
			}
			for i := tierIndex(step.Min); i >= 0 && i <= tierIndex(step.Max); i++ {
				spec := p.Tiers[chair.Provider][TierNames[i]]
				if spec.Effort == "" || spec.Effort == InheritEffort {
					continue
				}
				v := EffortVariant{Provider: chair.Provider, Base: chair.Profile, Effort: spec.Effort}
				if !seen[v] {
					seen[v] = true
					out = append(out, v)
				}
			}
		}
	}
	return out
}
