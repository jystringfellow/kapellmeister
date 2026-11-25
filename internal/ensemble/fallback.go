package ensemble

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

type TriedAgent struct {
	Agent  string `json:"agent"`
	Reason string `json:"reason"`
}
type SeatChange struct {
	Address         string       `json:"address"`
	BlockedAgent    string       `json:"blocked_agent"`
	BlockedProvider string       `json:"blocked_provider"`
	Reason          string       `json:"reason"`
	SeatedAgent     string       `json:"seated_agent"`
	SeatedProvider  string       `json:"seated_provider"`
	Tried           []TriedAgent `json:"tried"`
	Overlaps        []string     `json:"overlaps"`
}
type ProvidersLost struct {
	Section string   `json:"section"`
	Before  int      `json:"before"`
	After   int      `json:"after"`
	Blocked []string `json:"blocked"`
}

func FindOverlaps(name string, before, after []Member, changes []SeatChange) {
	index := map[string]int{}
	for i, c := range changes {
		index[c.Address] = i
	}
	for i, m := range after {
		if m.Part == "reviewer" {
			for j, imp := range after {
				if imp.Part == "implementer" && m.Chair.Provider == imp.Chair.Provider && before[i].Chair.Provider != before[j].Chair.Provider {
					k, ok := index[m.Address(name)]
					if !ok {
						k, ok = index[imp.Address(name)]
					}
					if ok {
						changes[k].Overlaps = append(changes[k].Overlaps, fmt.Sprintf("%s now plays on the same provider as the implementer (%s)", m.Address(name), m.Chair.Provider))
					}
				}
			}
		}
		for j := i + 1; j < len(after); j++ {
			other := after[j]
			if m.Section != other.Section || m.Chair.Provider != other.Chair.Provider || before[i].Chair.Provider == before[j].Chair.Provider {
				continue
			}
			x, y := m, other
			k, ok := index[x.Address(name)]
			if !ok {
				x, y = y, x
				k, ok = index[x.Address(name)]
			}
			if ok {
				changes[k].Overlaps = append(changes[k].Overlaps, fmt.Sprintf("%s now plays on the same provider as %s (%s)", x.Address(name), y.Address(name), x.Chair.Provider))
			}
		}
	}
}
func LostProviders(before, after []Member) []ProvidersLost {
	var sections []string
	for _, m := range before {
		if !slices.Contains(sections, m.Section) {
			sections = append(sections, m.Section)
		}
	}
	sections = append(sections, "")
	var out []ProvidersLost
	for _, section := range sections {
		old, current := map[string]bool{}, map[string]bool{}
		for _, m := range before {
			if section == "" || m.Section == section {
				old[m.Chair.Provider] = true
			}
		}
		for _, m := range after {
			if section == "" || m.Section == section {
				current[m.Chair.Provider] = true
			}
		}
		if len(current) >= len(old) {
			continue
		}
		l := ProvidersLost{Section: section, Before: len(old), After: len(current)}
		for p := range old {
			if !current[p] {
				l.Blocked = append(l.Blocked, p)
			}
		}
		slices.Sort(l.Blocked)
		out = append(out, l)
	}
	return out
}
func SeatLines(changes []SeatChange, lost []ProvidersLost) []string {
	var lines []string
	for _, c := range changes {
		lines = append(lines, fmt.Sprintf("%s: %s was BLOCKED (untrusted text from the agent's pane: %s); %s takes the seat", c.Address, c.BlockedAgent, c.Reason, c.SeatedAgent))
		lines = append(lines, c.Overlaps...)
	}
	for _, l := range lost {
		scope := "the ensemble"
		if l.Section != "" {
			scope = "section " + l.Section
		}
		a := "providers"
		if l.After == 1 {
			a = "provider"
		}
		verb := "were"
		if len(l.Blocked) == 1 {
			verb = "was"
		}
		lines = append(lines, fmt.Sprintf("%s played with %d %s instead of %d: %s %s blocked", scope, l.After, a, l.Before, strings.Join(l.Blocked, ", "), verb))
	}
	return lines
}

func (c SeatChange) MarshalJSON() ([]byte, error) {
	type seatChange SeatChange
	if c.Tried == nil {
		c.Tried = []TriedAgent{}
	}
	if c.Overlaps == nil {
		c.Overlaps = []string{}
	}
	return json.Marshal(seatChange(c))
}
