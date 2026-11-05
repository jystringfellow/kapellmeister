package rig

import (
	"fmt"
	"strings"

	"github.com/jystringfellow/kapellmeister/internal/loop"
)

var Roles = []string{"planner", "implementer", "reviewer", "gate"}

type Agent struct {
	Name string
	Seat loop.Seat
}

var DefaultAgents = []Agent{
	{"claude", loop.Seat{Provider: "claude_code", Profile: "km-claude"}},
	{"codex", loop.Seat{Provider: "codex", Profile: "km-codex"}},
	{"copilot", loop.Seat{Provider: "copilot_cli", Profile: "km-copilot"}},
	{"agy", loop.Seat{Provider: "antigravity_cli", Profile: "km-agy"}},
}

type Pod struct {
	Name  string            `json:"name"`
	Cast  map[string]string `json:"cast"`
	Seats loop.Seats        `json:"seats"`
}

func Rotation(agents []Agent, classifier loop.Seat) ([]Pod, error) {
	if len(agents) != len(Roles) {
		return nil, fmt.Errorf("a full rotation needs exactly %d agents, got %d", len(Roles), len(agents))
	}
	pods := make([]Pod, len(agents))
	for i := range agents {
		cast := map[string]string{}
		pick := func(role int) loop.Seat {
			a := agents[(i+role)%len(agents)]
			cast[Roles[role]] = a.Name
			return a.Seat
		}
		pods[i] = Pod{
			Name: fmt.Sprintf("pod%d", i+1),
			Seats: loop.Seats{
				Planner: pick(0), Implementer: pick(1), Reviewers: []loop.Seat{pick(2)}, Gate: pick(3),
				Classifier: classifier,
			},
			Cast: cast,
		}
	}
	return pods, nil
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

func (p Pod) CastLine() string {
	parts := make([]string, len(Roles))
	for i, r := range Roles {
		parts[i] = r + "=" + p.Cast[r]
	}
	return strings.Join(parts, " ")
}
