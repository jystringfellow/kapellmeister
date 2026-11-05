package loop

import (
	"fmt"
	"strings"
)

type Agent struct {
	Name string
	Seat Seat
}

var DefaultAgents = []Agent{
	{"claude", Seat{Provider: "claude_code", Profile: "km-claude"}},
	{"codex", Seat{Provider: "codex", Profile: "km-codex"}},
	{"copilot", Seat{Provider: "copilot_cli", Profile: "km-copilot"}},
	{"agy", Seat{Provider: "antigravity_cli", Profile: "km-agy"}},
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
