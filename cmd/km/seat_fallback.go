package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/jystringfellow/kapellmeister/internal/cao"
	"github.com/jystringfellow/kapellmeister/internal/ensemble"
	"github.com/jystringfellow/kapellmeister/internal/loop"
)

func memberSteps(m ensemble.Member, score loop.Score, policy *loop.Policy) map[string][]loop.Chair {
	steps := map[string][]loop.Chair{}
	if score.Source == "" {
		steps[policy.PartPolicyKey(m.Part)] = []loop.Chair{m.Chair}
	} else {
		for _, step := range score.StepsOf(m.Part) {
			steps[score.PolicyKey(step.Name)] = []loop.Chair{m.Chair}
		}
		if m.Part == score.Classifier {
			steps["classify"] = []loop.Chair{m.Chair}
		}
	}
	return steps
}

func memberChecks(m ensemble.Member, score loop.Score, policy *loop.Policy) []loop.Chair {
	steps := memberSteps(m, score, policy)
	var checks []loop.Chair
	for _, key := range sortedKeys(steps) {
		c, _ := policy.Chair(key, m.Chair, "", 0)
		checks = append(checks, c)
	}
	return loop.UniqueChairs(checks...)
}

func preflightSeats(ctx context.Context, client *cao.Client, wt string, s loop.Settings, spec ensemble.Spec, members []ensemble.Member, chairs loop.Chairs, policy *loop.Policy, output io.Writer) ([]ensemble.Member, []ensemble.SeatChange, []ensemble.ProvidersLost, error) {
	results, err := checkPreflight(ctx, client, wt, s.PreflightTimeout, s.PreflightRetryWait, defaultChairs(policy, chairs, s.Score.Score)...)
	if err != nil {
		return members, nil, nil, err
	}
	all := slices.Clone(results)
	cleanup := func() {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		for i := range all {
			r := &all[i]
			if r.OK || r.Session == "" {
				continue
			}
			if err := (loop.CAOStepper{Client: client}).Unseat(cleanupCtx, r.Session); err != nil {
				fmt.Fprintf(os.Stderr, "preflight teardown failed for %s: %v\n", r.Session, err)
			} else {
				r.Session, r.TerminalID = "", ""
			}
		}
	}
	for _, result := range results {
		if result.DefinitionError != "" {
			cleanup()
			return members, nil, nil, preflightFailure(all, s.PreflightTimeout)
		}
	}
	if preflightFailure(results, s.PreflightTimeout) == nil {
		return members, nil, nil, nil
	}
	defer func() {
		if ctx.Err() != nil {
			cleanup()
		}
	}()
	cache := map[[5]string]loop.PreflightResult{}
	blocked := map[string]bool{}
	remember := func(rs []loop.PreflightResult) {
		for _, r := range rs {
			cache[loop.ChairConfigKey(r.Chair)] = r
			if !r.OK && !r.ProfileMissing {
				blocked[r.Chair.Provider] = true
			}
		}
	}
	remember(results)
	failure := func(m ensemble.Member) *loop.PreflightResult {
		for _, c := range memberChecks(m, s.Score.Score, policy) {
			if r, ok := cache[loop.ChairConfigKey(c)]; ok && !r.OK {
				return &r
			}
		}
		return nil
	}
	type pendingSeat struct {
		paneTrials map[int]bool
		index      int
		later      []string
		change     ensemble.SeatChange
		done       bool
	}
	var pending []pendingSeat
	for i, m := range members {
		if r := failure(m); r != nil {
			first, later := spec.Listed(m.Section, m.Name, s.Agents, s.Defined)
			pending = append(pending, pendingSeat{index: i, later: slices.Clone(later), change: ensemble.SeatChange{Address: m.Address(spec.Name), BlockedAgent: first, BlockedProvider: m.Chair.Provider, Reason: r.Reason(), NativeAgent: m.Chair.NativeAgent, Reply: m.Chair.Reply}})
		}
	}
	if !s.SeatFallback {
		message := preflightFailure(all, s.PreflightTimeout).Error()
		for _, p := range pending {
			if len(p.later) > 0 {
				message += fmt.Sprintf("\n%s also lists %s; seat_fallback is off", p.change.Address, strings.Join(p.later, ", "))
			}
		}
		return members, nil, nil, fmt.Errorf("%s", message)
	}
	after := slices.Clone(members)
	for {
		type candidate struct {
			pending int
			agent   string
			member  ensemble.Member
		}
		var candidates []candidate
		var checks []loop.Chair
		for i := range pending {
			p := &pending[i]
			if p.done {
				continue
			}
			for len(p.later) > 0 {
				agent := p.later[0]
				p.later = p.later[1:]
				reject := func(reason string) {
					p.change.Tried = append(p.change.Tried, ensemble.TriedAgent{Agent: agent, Reason: reason})
				}
				if !ensemble.Eligible(agent, s.Agents) {
					reject("not in [agents] available")
					continue
				}
				original := members[p.index]
				if original.Chair.NativeAgent != "" {
					resolved, resolveErr := s.Defined.ResolveChair(agent)
					if resolveErr == nil && resolved.Provider != "claude_code" {
						base, _, _ := loop.SplitTier(agent)
						reject(fmt.Sprintf("%s cannot play native agent %s (only claude_code can)", base, original.Chair.NativeAgent))
						continue
					}
				}
				m, e := spec.Seat(s.Defined, original.Section, original.Name, agent)
				if e == nil {
					m.Chair = m.Chair.WithDefinitionOf(original.Chair)
					e = checkPin(*policy, m.Part, m.Address(spec.Name), agent, m.Chair)
				}
				if e != nil {
					reject(e.Error())
					continue
				}
				if blocked[m.Chair.Provider] {
					reject(fmt.Sprintf("skipped: provider %s was BLOCKED in this preflight", m.Chair.Provider))
					continue
				}
				if e = ensureProfilesTo(ctx, client, policy, memberSteps(m, s.Score.Score, policy), output); e != nil {
					if ctx.Err() != nil {
						return members, nil, nil, ctx.Err()
					}
					reject(e.Error())
					continue
				}
				candidates = append(candidates, candidate{pending: i, agent: agent, member: m})
				for _, c := range memberChecks(m, s.Score.Score, policy) {
					if _, ok := cache[loop.ChairConfigKey(c)]; !ok {
						checks = append(checks, c)
					}
				}
				break
			}
		}
		if len(candidates) == 0 {
			break
		}
		if len(checks) > 0 {
			rs, e := checkPreflight(ctx, client, wt, s.PreflightTimeout, s.PreflightRetryWait, checks...)
			all = append(all, rs...)
			if e != nil {
				return members, nil, nil, e
			}
			for _, result := range rs {
				if result.DefinitionError != "" {
					cleanup()
					return members, nil, nil, preflightFailure(all, s.PreflightTimeout)
				}
			}
			remember(rs)
		}
		for _, c := range candidates {
			p := &pending[c.pending]
			if r := failure(c.member); r != nil {
				if p.paneTrials == nil {
					p.paneTrials = map[int]bool{}
				}
				p.paneTrials[len(p.change.Tried)] = true
				p.change.Tried = append(p.change.Tried, ensemble.TriedAgent{Agent: c.agent, Reason: r.Reason()})
			} else {
				p.change.SeatedAgent, p.change.SeatedProvider = c.agent, c.member.Chair.Provider
				after[p.index] = c.member
				p.done = true
			}
		}
	}
	var failed []string
	var changes []ensemble.SeatChange
	for _, p := range pending {
		if p.done {
			changes = append(changes, p.change)
			continue
		}
		lines := []string{fmt.Sprintf("%s: no listed agent passed preflight:", p.change.Address), fmt.Sprintf("  %s: BLOCKED (untrusted text from the agent's pane: %s)", p.change.BlockedAgent, p.change.Reason)}
		for i, tried := range p.change.Tried {
			reason := plain(tried.Reason)
			if p.paneTrials[i] {
				reason = "BLOCKED (untrusted text from the agent's pane: " + reason + ")"
			}
			lines = append(lines, fmt.Sprintf("  %s: %s", tried.Agent, reason))
		}
		failed = append(failed, strings.Join(lines, "\n"))
	}
	if len(failed) > 0 {
		return members, nil, nil, fmt.Errorf("%w\n%s", preflightFailure(all, s.PreflightTimeout), strings.Join(failed, "\n"))
	}
	ensemble.FindOverlaps(spec.Name, members, after, changes)
	lost := ensemble.LostProviders(members, after)
	cleanup()
	return after, changes, lost, nil
}
