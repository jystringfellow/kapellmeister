package loop

import (
	"fmt"
	"strings"
)

type Agent struct {
	Name  string
	Chair Chair
}

var DefaultAgents = []Agent{
	{"claude", Chair{Provider: "claude_code", Profile: "km-claude"}},
	{"codex", Chair{Provider: "codex", Profile: "km-codex"}},
	{"copilot", Chair{Provider: "copilot_cli", Profile: "km-copilot"}},
	{"agy", Chair{Provider: "antigravity_cli", Profile: "km-agy"}},
}

func SelectAgents(names string) ([]Agent, error) {
	byName := map[string]Agent{}
	for _, a := range DefaultAgents {
		byName[a.Name] = a
	}
	var out []Agent
	for _, n := range strings.Split(names, ",") {
		a, ok := byName[strings.TrimSpace(n)]
		if !ok {
			return nil, fmt.Errorf("unknown agent %q", n)
		}
		out = append(out, a)
	}
	return out, nil
}

func AgentNames() []string {
	var names []string
	for _, a := range DefaultAgents {
		names = append(names, a.Name)
	}
	return names
}

func ResolveChair(spec string) (Chair, error) {
	base, tier, err := SplitTier(spec)
	if err != nil {
		return Chair{}, err
	}
	if !strings.Contains(base, ":") {
		agents, err := SelectAgents(base)
		if err != nil {
			return Chair{}, fmt.Errorf("chair %q: want an agent (%s) or provider:profile[:model], with optional @small|@medium|@large", spec, strings.Join(AgentNames(), ", "))
		}
		chair := agents[0].Chair
		chair.Tier = tier
		return chair, nil
	}
	return ParseChair(spec)
}
