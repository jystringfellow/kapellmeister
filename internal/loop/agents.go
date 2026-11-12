package loop

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

type Agent struct {
	Name  string
	Chair Chair
}

type Agents []Agent

func BuiltInAgents() Agents {
	return Agents{
		{"claude", Chair{Provider: "claude_code", Profile: "km-claude"}},
		{"codex", Chair{Provider: "codex", Profile: "km-codex"}},
		{"copilot", Chair{Provider: "copilot_cli", Profile: "km-copilot"}},
		{"agy", Chair{Provider: "antigravity_cli", Profile: "km-agy"}},
	}
}

func (defined Agents) Select(names string) ([]Agent, error) {
	byName := map[string]Agent{}
	for _, a := range defined {
		byName[a.Name] = a
	}
	var out []Agent
	for _, n := range strings.Split(names, ",") {
		a, ok := byName[strings.TrimSpace(n)]
		if !ok {
			return nil, fmt.Errorf("unknown agent %q (want %s)", n, strings.Join(defined.Names(), ", "))
		}
		out = append(out, a)
	}
	return out, nil
}

func (defined Agents) Names() []string {
	var names []string
	for _, a := range defined {
		names = append(names, a.Name)
	}
	return names
}

func (defined Agents) ResolveChair(spec string) (Chair, error) {
	base, tier, err := SplitTier(spec)
	if err != nil {
		return Chair{}, err
	}
	if !strings.Contains(base, ":") {
		agents, err := defined.Select(base)
		if err != nil || len(agents) != 1 {
			return Chair{}, fmt.Errorf("chair %q: want an agent (%s) or provider:profile[:model], with optional @small|@medium|@large", spec, strings.Join(defined.Names(), ", "))
		}
		chair := agents[0].Chair
		chair.Tier = tier
		return chair, nil
	}
	return ParseChair(spec)
}

var agentName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

func (defined Agents) With(defs map[string]string) (Agents, error) {
	out := append(Agents(nil), defined...)
	for _, name := range sortedKeys(defs) {
		value := defs[name]
		fail := func() (Agents, error) {
			return nil, fmt.Errorf("agents.define.%s: want a lowercase agent name of letters, digits, dashes or underscores and provider:profile without a model or tier", name)
		}
		if !agentName.MatchString(name) || strings.Count(value, ":") != 1 || strings.ContainsAny(value, "@, \t\r\n") {
			return fail()
		}
		chair, err := ParseChair(value)
		if err != nil {
			return fail()
		}
		replaced := false
		for i := range out {
			if out[i].Name == name {
				out[i].Chair = chair
				replaced = true
				break
			}
		}
		if !replaced {
			out = append(out, Agent{Name: name, Chair: chair})
		}
	}
	return out, nil
}

func DefaultChairs(available []string) map[string]string {
	var names []string
	for _, name := range available {
		if !slices.Contains(names, name) {
			names = append(names, name)
		}
	}
	chairs := map[string]string{}
	if len(names) == 0 {
		return chairs
	}
	planner, implementer, reviewer := names[0], names[1%len(names)], names[2%len(names)]
	if slices.Contains(names, "claude") && slices.Contains(names, "codex") && slices.Contains(names, "copilot") {
		planner, implementer, reviewer = "claude", "codex", "copilot"
	}
	for _, role := range []string{"planner", "gate", "classifier"} {
		chairs[role] = planner
	}
	chairs["implementer"], chairs["reviewer"] = implementer, reviewer
	return chairs
}
