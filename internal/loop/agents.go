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
