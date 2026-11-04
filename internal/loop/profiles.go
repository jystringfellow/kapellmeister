package loop

import (
	"fmt"
	"slices"
)

const seatInstructions = "You are a seat in a kapellmeister engineering loop. Follow each step's instructions exactly, write requested files exactly where asked, and keep chat replies short.\n"

var effortConfigKey = map[string]string{
	"claude_code": "claudeConfig:\n  effort: %s\n",
	"codex":       "codexConfig:\n  model_reasoning_effort: %s\n",
}

var generatedBases = map[string]string{
	"claude_code": "km-claude",
	"codex":       "km-codex",
}

func EffortProfileSource(provider, base, effort string) (string, error) {
	if generatedBases[provider] != base {
		return "", fmt.Errorf("profile %s is not installed in CAO, and kapellmeister only generates effort profiles for %s; install a copy of %s with %s effort as %s",
			EffortProfile(base, effort), generatedBaseList(), base, effort, EffortProfile(base, effort))
	}
	if !slices.Contains(effortLevels[provider], effort) {
		return "", fmt.Errorf("%s does not support effort %q", provider, effort)
	}
	name := EffortProfile(base, effort)
	return fmt.Sprintf("---\nname: %s\ndescription: kapellmeister seat at %s effort; kapellmeister passes the model per step\nprovider: %s\nrole: developer\n"+effortConfigKey[provider]+"---\n\n%s",
		name, effort, provider, effort, seatInstructions), nil
}

func generatedBaseList() string {
	return generatedBases["claude_code"] + " and " + generatedBases["codex"]
}

type EffortVariant struct {
	Provider, Base, Effort string
}

func (v EffortVariant) Name() string { return EffortProfile(v.Base, v.Effort) }

func (p *Policy) EffortVariants(steps map[string][]Seat) []EffortVariant {
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
		for _, seat := range steps[key] {
			if seat.Model != "" {
				continue
			}
			for i := tierIndex(step.Min); i >= 0 && i <= tierIndex(step.Max); i++ {
				spec := p.Tiers[seat.Provider][TierNames[i]]
				if spec.Effort == "" || spec.Effort == InheritEffort {
					continue
				}
				v := EffortVariant{Provider: seat.Provider, Base: seat.Profile, Effort: spec.Effort}
				if !seen[v] {
					seen[v] = true
					out = append(out, v)
				}
			}
		}
	}
	return out
}
