package rig

import (
	"testing"

	"github.com/jystringfellow/kapellmeister/internal/loop"
)

func TestRotationSeatsEveryAgentInEveryRoleOnceAndNeverTwiceInAPod(t *testing.T) {
	pods, err := Rotation(DefaultAgents, loop.Seat{Provider: "claude_code", Profile: "km-claude"})
	if err != nil {
		t.Fatal(err)
	}
	if len(pods) != 4 {
		t.Fatalf("got %d pods", len(pods))
	}
	seen := map[string]map[string]bool{}
	for _, p := range pods {
		inPod := map[string]bool{}
		for _, role := range Roles {
			agent := p.Cast[role]
			if inPod[agent] {
				t.Fatalf("%s seats %s twice: %s", p.Name, agent, p.CastLine())
			}
			inPod[agent] = true
			if seen[agent] == nil {
				seen[agent] = map[string]bool{}
			}
			if seen[agent][role] {
				t.Fatalf("%s sits %s twice across the rig", agent, role)
			}
			seen[agent][role] = true
		}
	}
	for _, a := range DefaultAgents {
		if len(seen[a.Name]) != len(Roles) {
			t.Fatalf("%s sat %v, want all of %v", a.Name, seen[a.Name], Roles)
		}
	}
}

func TestRotationSeatsMatchTheCast(t *testing.T) {
	pods, _ := Rotation(DefaultAgents, loop.Seat{})
	p := pods[0]
	if p.Cast["implementer"] != "codex" || p.Seats.Implementer.Provider != "codex" {
		t.Fatalf("pod1 implementer = %s / %s", p.Cast["implementer"], p.Seats.Implementer)
	}
	if p.Cast["gate"] != "agy" || p.Seats.Gate.Provider != "antigravity_cli" {
		t.Fatalf("pod1 gate = %s / %s", p.Cast["gate"], p.Seats.Gate)
	}
}

func TestRotationNeedsOneAgentPerRole(t *testing.T) {
	if _, err := Rotation(DefaultAgents[:3], loop.Seat{}); err == nil {
		t.Fatal("want error for three agents")
	}
}

func TestEveryRigPodHasOneReviewer(t *testing.T) {
	pods, err := Rotation(DefaultAgents, loop.Seat{})
	if err != nil {
		t.Fatal(err)
	}
	for _, pod := range pods {
		agents, err := SelectAgents(pod.Cast["reviewer"])
		if err != nil || len(pod.Seats.Reviewers) != 1 || pod.Seats.Reviewers[0] != agents[0].Seat {
			t.Fatalf("%+v %v", pod, err)
		}
	}
}
