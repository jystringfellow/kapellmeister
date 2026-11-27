package loop

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
)

var TierNames = []string{"small", "medium", "large", "xlarge"}

var SizeNames = []string{"small", "medium", "large"}

var StepKeys = []string{"plan", "implement", "review", "gate", "classify", "test"}

const (
	SizingOff    = "off"
	SizingShadow = "shadow"
	SizingOn     = "on"

	DeciderPlanner = "planner"
	DeciderCommand = "command"

	InheritEffort = "inherit"
)

type TierSpec struct {
	Model  string `toml:"model" json:"model,omitempty"`
	Effort string `toml:"effort" json:"effort,omitempty"`
}

type StepPolicy struct {
	Tier string `toml:"tier" json:"tier,omitempty"`
	Min  string `toml:"min" json:"min,omitempty"`
	Max  string `toml:"max" json:"max,omitempty"`
}

type SizingConfig struct {
	Mode          string `toml:"mode" json:"mode,omitempty"`
	Decider       string `toml:"decider" json:"decider,omitempty"`
	Command       string `toml:"command" json:"command,omitempty"`
	EscalateAfter *int   `toml:"escalate_after" json:"escalate_after,omitempty"`
}

type Policy struct {
	custom map[string]StepPolicy
	score  Score
	Tiers  map[string]map[string]TierSpec `json:"tiers"`
	Steps  map[string]StepPolicy          `json:"steps"`
	Sizing SizingConfig                   `json:"sizing"`
}

type Assignment struct {
	Tier   string `json:"tier,omitempty"`
	Effort string `json:"effort,omitempty"`
	Source string `json:"source,omitempty"`
}

const (
	SourceExplicit  = "explicit"
	SourceDefault   = "default"
	SourcePinned    = "pinned"
	SourceSized     = "sized"
	SourceEscalated = "escalated"
)

var modelIDRE = regexp.MustCompile(`^[A-Za-z0-9._:/-]+$`)

var effortLevels = map[string][]string{
	"claude_code": {"low", "medium", "high", "xhigh", "max"},
	"codex":       {"low", "medium", "high", "xhigh", "max", "ultra"},
}

func DefaultPolicy() Policy {
	escalateAfter := 2
	return Policy{
		Tiers: map[string]map[string]TierSpec{
			"claude_code": {
				"small":  {Model: "claude-haiku-5-5", Effort: "medium"},
				"medium": {Model: "claude-sonnet-5-5", Effort: "high"},
				"large":  {Model: "claude-opus-5-5", Effort: "high"},
				"xlarge": {Model: "claude-fable-5-2", Effort: "high"},
			},
			"codex": {
				"small":  {Model: "gpt-6-luna", Effort: "medium"},
				"medium": {Model: "gpt-6.1-terra", Effort: "high"},
				"large":  {Model: "gpt-6.1-sol", Effort: "high"},
				"xlarge": {Model: "gpt-6-astra", Effort: "high"},
			},
			"copilot_cli": {
				"small":  {Model: "claude-haiku-5.5"},
				"medium": {Model: "claude-sonnet-5.5"},
				"large":  {Model: "claude-opus-5.5"},
				"xlarge": {Model: "claude-fable-5.2"},
			},
			"antigravity_cli": {
				"small":  {Model: "gemini-3.8-flash-high"},
				"medium": {Model: "gemini-4-hydrogen-high"},
				"large":  {Model: "gemini-4-neon-high"},
				"xlarge": {Model: "gemini-4-argon-high"},
			},
		},
		Steps: map[string]StepPolicy{
			"plan":      {Tier: "medium", Min: "medium", Max: "large"},
			"implement": {Tier: "medium", Min: "small", Max: "xlarge"},
			"review":    {Tier: "medium", Min: "small", Max: "large"},
			"gate":      {Tier: "medium", Min: "medium", Max: "large"},
			"test":      {Tier: "medium", Min: "medium", Max: "large"},
			"classify":  {Tier: "small", Min: "small", Max: "small"},
		},
		Sizing: SizingConfig{Mode: SizingShadow, Decider: DeciderPlanner, EscalateAfter: &escalateAfter},
	}
}

func (p Policy) clone() Policy {
	out := Policy{Tiers: map[string]map[string]TierSpec{}, Steps: map[string]StepPolicy{}, Sizing: p.Sizing, score: p.score}
	for provider, tiers := range p.Tiers {
		out.Tiers[provider] = map[string]TierSpec{}
		for name, spec := range tiers {
			out.Tiers[provider][name] = spec
		}
	}
	for name, step := range p.Steps {
		out.Steps[name] = step
	}
	if p.custom != nil {
		out.custom = map[string]StepPolicy{}
		for name, step := range p.custom {
			out.custom[name] = step
		}
	}
	return out
}

func (p Policy) withTiers(over map[string]map[string]TierSpec) Policy {
	out := p.clone()
	for provider, tiers := range over {
		if out.Tiers[provider] == nil {
			out.Tiers[provider] = map[string]TierSpec{}
		}
		for name, spec := range tiers {
			cur := out.Tiers[provider][name]
			cur.Model = firstSet(spec.Model, cur.Model)
			cur.Effort = firstSet(spec.Effort, cur.Effort)
			out.Tiers[provider][name] = cur
		}
	}
	return out
}

func (p Policy) withSteps(over map[string]StepPolicy) Policy {
	out := p.clone()
	for name, step := range over {
		if slices.Contains(StepKeys, name) {
			out.Steps[name] = mergeStep(out.Steps[name], step)
		} else {
			if out.custom == nil {
				out.custom = map[string]StepPolicy{}
			}
			out.custom[name] = mergeStep(out.custom[name], step)
		}
	}
	return out
}

func (p Policy) withSizing(over SizingConfig) Policy {
	out := p.clone()
	out.Sizing.Mode = firstSet(over.Mode, out.Sizing.Mode)
	out.Sizing.Decider = firstSet(over.Decider, out.Sizing.Decider)
	out.Sizing.Command = firstSet(over.Command, out.Sizing.Command)
	if over.EscalateAfter != nil {
		out.Sizing.EscalateAfter = over.EscalateAfter
	}
	return out
}

func (p Policy) Validate() error {
	var problems []string
	for _, provider := range sortedKeys(p.Tiers) {
		for _, name := range sortedKeys(p.Tiers[provider]) {
			spec := p.Tiers[provider][name]
			where := fmt.Sprintf("tiers.%s.%s", provider, name)
			if tierIndex(name) < 0 {
				problems = append(problems, fmt.Sprintf("%s: unknown tier (want %s)", where, strings.Join(TierNames, ", ")))
			}
			if spec.Model == "" {
				problems = append(problems, where+": model is required")
			} else if !modelIDRE.MatchString(spec.Model) {
				problems = append(problems, fmt.Sprintf("%s: model %q must be a model ID matching %s, as CAO's run-step requires", where, spec.Model, modelIDRE))
			}
			if spec.Effort == "" || spec.Effort == InheritEffort {
				continue
			}
			levels, ok := effortLevels[provider]
			if !ok {
				problems = append(problems, fmt.Sprintf("%s: CAO cannot set effort for %s; leave effort out and pick a model", where, provider))
			} else if !slices.Contains(levels, spec.Effort) {
				problems = append(problems, fmt.Sprintf("%s: effort %q is not one of %s", where, spec.Effort, strings.Join(levels, ", ")))
			}
		}
	}
	for _, name := range sortedKeys(p.Steps) {
		if err := validateStep(name, p.Steps[name]); err != nil {
			problems = append(problems, err.Error())
		}
	}
	for _, name := range sortedKeys(p.custom) {
		step := p.custom[name]
		for _, value := range []string{step.Tier, step.Min, step.Max} {
			if value != "" && tierIndex(value) < 0 {
				problems = append(problems, fmt.Sprintf("steps.%s: tier, min and max must each be one of %s", name, strings.Join(TierNames, ", ")))
				break
			}
		}
	}
	switch p.Sizing.Mode {
	case SizingOff, SizingShadow, SizingOn:
	default:
		problems = append(problems, fmt.Sprintf("sizing.mode: %q is not off, shadow or on", p.Sizing.Mode))
	}
	switch p.Sizing.Decider {
	case DeciderPlanner:
	case DeciderCommand:
		if p.Sizing.Command == "" {
			problems = append(problems, "sizing.command: required when decider is command")
		}
	default:
		problems = append(problems, fmt.Sprintf("sizing.decider: %q is not planner or command", p.Sizing.Decider))
	}
	if p.Sizing.EscalateAfter != nil && *p.Sizing.EscalateAfter < 0 {
		problems = append(problems, "sizing.escalate_after: must be 0 (off) or more")
	}
	if len(problems) > 0 {
		return errors.New(strings.Join(problems, "; "))
	}
	return nil
}

func (p Policy) EscalateAfter() int {
	if p.Sizing.EscalateAfter == nil {
		return 0
	}
	return *p.Sizing.EscalateAfter
}

func (p *Policy) Chair(stepKey string, chair Chair, want string, bump int) (Chair, Assignment) {
	if p == nil || chair.NativeAgent != "" {
		return chair, Assignment{}
	}
	if chair.Model != "" {
		return chair, Assignment{Source: SourceExplicit}
	}
	_, tiered := p.Tiers[chair.Provider]
	step, known := p.Steps[stepKey]
	if !tiered || !known {
		return chair, Assignment{}
	}
	source := SourceDefault
	tier := step.Tier
	if chair.Tier != "" {
		tier, source = chair.Tier, SourcePinned
	} else if want != "" {
		tier, source = want, SourceSized
	}
	if bump > 0 {
		shifted := shiftTier(tier, bump)
		if clampTier(shifted, step.Min, step.Max) != clampTier(tier, step.Min, step.Max) {
			source = SourceEscalated
		}
		tier = shifted
	}
	tier = clampTier(tier, step.Min, step.Max)
	chair.Tier = ""
	resolved, effort := p.At(chair, tier)
	return resolved, Assignment{Tier: tier, Effort: effort, Source: source}
}

func (p *Policy) At(chair Chair, tier string) (Chair, string) {
	if chair.NativeAgent != "" {
		return chair, ""
	}
	spec, ok := p.Tiers[chair.Provider][tier]
	if !ok {
		return chair, ""
	}
	chair.Model = spec.Model
	if spec.Effort != "" && spec.Effort != InheritEffort {
		chair.Profile = EffortProfile(chair.Profile, spec.Effort)
		return chair, spec.Effort
	}
	return chair, ""
}

func EffortProfile(base, effort string) string {
	return base + "-" + effort
}

func (p *Policy) InheritAdvice(provider, effort string) string {
	lines := []string{fmt.Sprintf("[tiers.%s]", provider)}
	for _, tier := range TierNames {
		if p.Tiers[provider][tier].Effort == effort {
			lines = append(lines, fmt.Sprintf("%s = { effort = %q }", tier, InheritEffort))
		}
	}
	return strings.Join(lines, "\n")
}

func tierIndex(name string) int {
	return slices.Index(TierNames, name)
}

func shiftTier(tier string, by int) string {
	i := tierIndex(tier) + by
	return TierNames[max(0, min(i, len(TierNames)-1))]
}

func clampTier(tier, lo, hi string) string {
	i := tierIndex(tier)
	if l := tierIndex(lo); l >= 0 && i < l {
		i = l
	}
	if h := tierIndex(hi); h >= 0 && i > h {
		i = h
	}
	if i < 0 {
		return tier
	}
	return TierNames[i]
}

func firstSet(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func mergeStep(base, over StepPolicy) StepPolicy {
	return StepPolicy{Tier: firstSet(over.Tier, base.Tier), Min: firstSet(over.Min, base.Min), Max: firstSet(over.Max, base.Max)}
}

func (p Policy) ForScore(score Score) (Policy, error) {
	out := p.clone()
	out.score = score
	names := slices.Clone(StepKeys)
	for _, step := range score.Steps {
		if !slices.Contains(names, step.Name) {
			names = append(names, step.Name)
		}
	}
	for _, name := range sortedKeys(p.custom) {
		step, ok := score.Step(name)
		if !ok {
			return p, fmt.Errorf("steps.%s: unknown step (want %s)", name, strings.Join(names, ", "))
		}
		if key := score.PolicyKey(step.Name); key != name {
			return p, fmt.Errorf("steps.%s: use [steps.%s] for this step", name, key)
		}
	}
	for _, step := range score.Steps {
		key := score.PolicyKey(step.Name)
		if _, ok := out.Steps[key]; !ok {
			policy := StepPolicy{Tier: "medium", Min: "medium", Max: "large"}
			if step.Kind == KindProduce || step.Kind == KindReview {
				policy.Min = "small"
			}
			if step.Kind == KindProduce && step.Escalate {
				policy.Max = "xlarge"
			}
			out.Steps[key] = policy
		}
		out.Steps[key] = mergeStep(out.Steps[key], p.custom[step.Name])
	}
	for _, name := range sortedKeys(out.Steps) {
		if err := validateStep(name, out.Steps[name]); err != nil {
			return p, err
		}
	}
	return out, nil
}

func (p Policy) PartPolicyKey(part string) string {
	score := p.score
	if len(score.Steps) == 0 {
		score = DefaultScore()
	}
	return score.PartPolicyKey(part)
}

func validateStep(name string, step StepPolicy) error {
	lo, mid, hi := tierIndex(step.Min), tierIndex(step.Tier), tierIndex(step.Max)
	if lo < 0 || mid < 0 || hi < 0 {
		return fmt.Errorf("steps.%s: tier, min and max must each be one of %s", name, strings.Join(TierNames, ", "))
	}
	if lo > mid || mid > hi {
		return fmt.Errorf("steps.%s: want min <= tier <= max", name)
	}
	return nil
}
