package main

import (
	"path/filepath"
	"strings"

	"github.com/jystringfellow/kapellmeister/internal/ensemble"
	"github.com/jystringfellow/kapellmeister/internal/loop"
)

func recordSeating(p ensembleRun, score loop.Score) *statusSeating {
	source := p.EnsembleSource
	if source == "" {
		if path, found := ensemble.Path(p.Ensemble, loop.Home(), p.Config.Worktree); found {
			source = "repository"
			if filepath.Dir(path) == filepath.Join(loop.Home(), ensemble.UserDir) {
				source = "user"
			}
		}
	}
	seating := &statusSeating{Name: p.Ensemble, Source: source, Sections: []statusSection{}}
	members := p.Members
	if len(members) == 0 {
		members = ensemble.MembersOf(p.Config.Chairs)
	}
	for _, member := range members {
		section := member.Section
		name := member.Name
		address := member.Chair.Address
		if address != "" {
			seat, _, _ := strings.Cut(address, "@")
			if s, n, ok := strings.Cut(seat, "."); ok {
				section, name = s, n
			}
		}
		if source == "" || source == "config" {
			section = "chairs"
		}
		if address == "" {
			address = section + "." + name
			if p.Ensemble != "" {
				address += "@" + p.Ensemble
			}
		}
		index := -1
		for i, s := range seating.Sections {
			if s.Name == section {
				index = i
				break
			}
		}
		if index < 0 {
			seating.Sections = append(seating.Sections, statusSection{Name: section, Chairs: []statusChair{}})
			index = len(seating.Sections) - 1
		}
		agent := p.Agents.NameOf(member.Chair)
		chair, assignment := initialStatusChair(p.Config.Policy, score, member)
		memory := string(loop.MemoryFresh)
		if part, ok := score.Part(member.Part); ok {
			memory = string(part.Memory)
		}
		for _, step := range score.StepsOf(member.Part) {
			if step.Kind == loop.KindReview {
				memory = string(loop.MemoryFresh)
			}
		}
		seating.Sections[index].Chairs = append(seating.Sections[index].Chairs, statusChair{
			Definition: member.Chair.Definition, NativeAgent: member.Chair.NativeAgent, ModelSource: nativeModelSource(member.Chair), Focus: member.Chair.Focus, Address: address, Name: name, Part: member.Part, Agent: agent, Provider: member.Chair.Provider, Model: chair.Model, Tier: firstNonEmpty(assignment.Tier, chair.Tier), Memory: memory, Terminal: p.Terminals[address],
		})
	}
	return seating
}

func initialStatusChair(policy *loop.Policy, score loop.Score, member ensemble.Member) (loop.Chair, loop.Assignment) {
	for _, step := range score.StepsOf(member.Part) {
		chair, assignment := policy.Chair(step.Name, member.Chair, "", 0)
		if assignment != (loop.Assignment{}) {
			return chair, assignment
		}
	}
	if member.Part == score.Classifier {
		return policy.Chair("classify", member.Chair, "", 0)
	}
	return member.Chair, loop.Assignment{}
}

func nativeModelSource(chair loop.Chair) string {
	if chair.NativeAgent != "" {
		return "agent"
	}
	return ""
}
