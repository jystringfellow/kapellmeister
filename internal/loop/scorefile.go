package loop

import (
	"fmt"
	"slices"
	"strings"

	"github.com/BurntSushi/toml"
)

type scorePartFile struct {
	ResumeTokens *int64  `toml:"resume_tokens"`
	ResumeSteps  *int    `toml:"resume_steps"`
	Memory       *Memory `toml:"memory"`
	Optional     bool    `toml:"optional"`
}
type scoreStepFile struct {
	Name        string            `toml:"name"`
	Kind        StepKind          `toml:"kind"`
	Part        string            `toml:"part"`
	Reads       []string          `toml:"reads"`
	Writes      *string           `toml:"writes"`
	Background  bool              `toml:"background"`
	ApproveWhen *string           `toml:"approve_when"`
	Escalate    bool              `toml:"escalate"`
	Next        map[string]string `toml:"next"`
}
type scoreFile struct {
	Rounds *int                     `toml:"rounds"`
	Parts  map[string]scorePartFile `toml:"parts"`
	Steps  []scoreStepFile          `toml:"steps"`
}

func scoreError(path, subject, format string, args ...any) error {
	if subject != "" {
		subject += ": "
	}
	return fmt.Errorf("%s: %s%s", path, subject, fmt.Sprintf(format, args...))
}
func scoreKeys(path, subject string, raw map[string]any, allowed ...string) error {
	for _, key := range sortedKeys(raw) {
		if !slices.Contains(allowed, key) {
			return scoreError(path, subject, "unknown key %q", key)
		}
	}
	return nil
}
func decodeScore(text, path, name string) (Score, error) {
	return decodeScoreWithPartText(text, path, name, false)
}

func decodeScoreWithPartText(text, path, name string, ownPartText bool) (Score, error) {
	var raw map[string]any
	if _, err := toml.Decode(text, &raw); err != nil {
		return Score{}, scoreError(path, "", "%v", err)
	}
	if err := scoreKeys(path, "", raw, "rounds", "parts", "steps"); err != nil {
		return Score{}, err
	}
	if parts, ok := raw["parts"].(map[string]any); ok {
		for _, name := range sortedKeys(parts) {
			if message, ok := ReservedNameMessage(name); ok {
				return Score{}, scoreError(path, "part "+name, "%s", message)
			}
			if p, ok := parts[name].(map[string]any); ok {
				if err := scoreKeys(path, "part "+name, p, "memory", "optional", "resume_tokens", "resume_steps"); err != nil {
					return Score{}, err
				}
			}
		}
	}
	if steps, ok := raw["steps"].([]map[string]any); ok {
		for i, step := range steps {
			subject := fmt.Sprintf("step #%d", i+1)
			if name, ok := step["name"].(string); ok {
				subject = "step " + name
			}
			if err := scoreKeys(path, subject, step, "name", "kind", "part", "reads", "writes", "background", "approve_when", "escalate", "next"); err != nil {
				return Score{}, err
			}
		}
	}
	var f scoreFile
	if _, err := toml.Decode(text, &f); err != nil {
		return Score{}, scoreError(path, "", "%v", err)
	}
	score := Score{Name: name, Classifier: "classifier"}
	if f.Rounds != nil {
		if *f.Rounds < 1 {
			return Score{}, scoreError(path, "", "rounds must be at least 1")
		}
		score.Rounds = *f.Rounds
	}
	if len(f.Steps) == 0 {
		return Score{}, scoreError(path, "", "score has no steps")
	}
	used := map[string]bool{}
	for i, step := range f.Steps {
		subject := "step " + step.Name
		if !scoreName.MatchString(step.Name) {
			return Score{}, scoreError(path, fmt.Sprintf("step #%d", i+1), "invalid name %q", step.Name)
		}
		if slices.Contains([]string{"approved", "changes_requested", "verify", "strict", "lessons"}, step.Name) || strings.HasPrefix(step.Name, "classify-") {
			return Score{}, scoreError(path, subject, "step name is reserved")
		}
		if message, ok := ReservedNameMessage(step.Name); ok {
			return Score{}, scoreError(path, subject, "%s", message)
		}
		if score.Index(step.Name) >= 0 {
			return Score{}, scoreError(path, subject, "duplicate step name")
		}
		if !scoreName.MatchString(step.Part) || step.Part == "classifier" {
			return Score{}, scoreError(path, subject, "invalid step part %q", step.Part)
		}
		if message, ok := ReservedNameMessage(step.Part); ok {
			return Score{}, scoreError(path, subject, "part %q: %s", step.Part, message)
		}
		writes, approval := "", ""
		if step.Writes != nil {
			writes = *step.Writes
			if writes == "" {
				return Score{}, scoreError(path, subject, "unknown writes %q (want reply)", writes)
			}
		}
		if step.ApproveWhen != nil {
			approval = *step.ApproveWhen
			if approval == "" {
				return Score{}, scoreError(path, subject, "unknown approve_when %q (want any, all or always)", approval)
			}
		}
		spec := ScoreStep{Name: step.Name, Kind: step.Kind, Part: step.Part, Reads: step.Reads, Writes: writes, Background: step.Background, ApproveWhen: approval, Escalate: step.Escalate}
		for _, exit := range sortedKeys(step.Next) {
			spec.Exits = append(spec.Exits, Exit{exit, step.Next[exit]})
		}
		score.Steps = append(score.Steps, spec)
		used[step.Part] = true
	}
	verdict := false
	backgrounds := 0
	for i := range score.Steps {
		step := &score.Steps[i]
		subject := "step " + step.Name
		exits := []string{}
		switch step.Kind {
		case KindProduce:
			exits = []string{"done"}
		case KindReview, KindDecide:
			exits = []string{"approve", "changes"}
			verdict = true
		case KindTest:
			exits = []string{"approve", "changes", "absent"}
			verdict = true
		default:
			return Score{}, scoreError(path, subject, "unknown kind %q (want produce, review, decide, test)", step.Kind)
		}
		if step.Writes != "" && step.Writes != "reply" {
			return Score{}, scoreError(path, subject, "unknown writes %q (want reply)", step.Writes)
		}
		if step.Writes != "" && step.Kind != KindProduce {
			return Score{}, scoreError(path, subject, "writes is only for produce")
		}
		if step.ApproveWhen != "" {
			if step.Kind != KindReview {
				return Score{}, scoreError(path, subject, "approve_when is only for review")
			}
			if !slices.Contains([]string{"any", "all", "always"}, step.ApproveWhen) {
				return Score{}, scoreError(path, subject, "unknown approve_when %q (want any, all or always)", step.ApproveWhen)
			}
		}
		if step.Escalate && step.Kind != KindProduce {
			return Score{}, scoreError(path, subject, "escalate is only for produce")
		}
		if step.Background {
			backgrounds++
			if backgrounds > 1 {
				return Score{}, scoreError(path, subject, "more than one background step")
			}
			if step.Kind != KindProduce || step.Writes != "reply" {
				return Score{}, scoreError(path, subject, "background requires produce writing only reply")
			}
			if i == 0 {
				return Score{}, scoreError(path, subject, "background cannot be the first step")
			}
			if len(step.Exits) > 0 {
				return Score{}, scoreError(path, subject, "background has no next exits")
			}
		} else {
			for _, exit := range step.Exits {
				if !slices.Contains(exits, exit.Name) {
					return Score{}, scoreError(path, subject, "unknown exit %q for %s", exit.Name, step.Kind)
				}
			}
			for _, name := range exits {
				target := ""
				for _, exit := range step.Exits {
					if exit.Name == name {
						target = exit.To
					}
				}
				if target == "" {
					return Score{}, scoreError(path, subject, "exit %s has no target", name)
				}
				index := score.Index(target)
				if index < 0 && target != Approved && target != ChangesRequested {
					return Score{}, scoreError(path, subject, "exit %s has unknown target %q", name, target)
				}
				if index >= 0 && score.Steps[index].Background {
					return Score{}, scoreError(path, subject, "exit %s targets background step %q", name, target)
				}
				if index >= 0 && index <= i && f.Rounds == nil {
					return Score{}, scoreError(path, subject, "exit %s targets earlier step %q without rounds", name, target)
				}
			}
		}
		for _, read := range step.Reads {
			if score.Index(read) < 0 {
				return Score{}, scoreError(path, subject, "reads unknown step %q", read)
			}
		}
		if built, ok := DefaultScore().Part(step.Part); ok && !ownPartText {
			matched := false
			for _, original := range DefaultScore().StepsOf(built.Name) {
				if original.Kind != step.Kind {
					continue
				}
				matched = true
				step.Brief = original.Brief
				needed := len(original.Reads)
				if original.Brief == "implement" {
					needed = 1
				}
				if len(step.Reads) < needed {
					return Score{}, scoreError(path, subject, "built-in part %s needs at least %d reads", step.Part, needed)
				}
				if original.Writes == "reply" && step.Writes != "reply" {
					return Score{}, scoreError(path, subject, "built-in part %s requires writes = reply", step.Part)
				}
			}
			if !matched {
				return Score{}, scoreError(path, subject, "built-in part %s cannot play %s", step.Part, step.Kind)
			}
		}
	}
	for _, part := range sortedKeys(f.Parts) {
		if !used[part] {
			return Score{}, scoreError(path, "part "+part, "no step plays this part")
		}
	}
	for _, name := range sortedKeys(used) {
		p := f.Parts[name]
		memory := MemoryFresh
		if p.Memory != nil {
			memory = *p.Memory
		}
		if memory != MemoryFresh && memory != MemoryResume {
			return Score{}, scoreError(path, "part "+name, "unknown memory %q (want fresh or resume)", memory)
		}
		for _, step := range score.StepsOf(name) {
			if step.Kind == KindReview && memory == MemoryResume {
				return Score{}, scoreError(path, "part "+name, "review steps cannot resume")
			}
			if p.Optional && step.Kind != KindTest && !step.Background {
				return Score{}, scoreError(path, "part "+name, "optional requires test or background steps")
			}
		}
		part := Part{Name: name, Memory: memory, Optional: p.Optional}
		for _, limit := range []struct {
			name  string
			value int64
			set   bool
		}{
			{"resume_tokens", valueOrZero(p.ResumeTokens), p.ResumeTokens != nil},
			{"resume_steps", int64(valueOrZero(p.ResumeSteps)), p.ResumeSteps != nil},
		} {
			if !limit.set {
				continue
			}
			if limit.value < 1 {
				return Score{}, scoreError(path, "part "+name, "%s must be at least 1", limit.name)
			}
			if memory != MemoryResume {
				return Score{}, scoreError(path, "part "+name, "%s requires memory = resume", limit.name)
			}
		}
		part.ResumeTokens, part.ResumeSteps = valueOrZero(p.ResumeTokens), valueOrZero(p.ResumeSteps)
		score.Parts = append(score.Parts, part)
	}
	classifier, _ := DefaultScore().Part("classifier")
	classifier.Optional = !verdict
	score.Parts = append(score.Parts, classifier)
	reached := map[string]bool{}
	var visit func(string)
	visit = func(name string) {
		if reached[name] {
			return
		}
		reached[name] = true
		if index := score.Index(name); index >= 0 {
			if index+1 < len(score.Steps) && score.Steps[index+1].Background {
				reached[score.Steps[index+1].Name] = true
			}
			for _, exit := range score.Steps[index].Exits {
				visit(exit.To)
			}
		}
	}
	visit(score.Steps[0].Name)
	for _, step := range score.Steps {
		if !reached[step.Name] {
			return Score{}, scoreError(path, "step "+step.Name, "no path reaches this step")
		}
	}
	ending := map[string]bool{Approved: true, ChangesRequested: true}
	for changed := true; changed; {
		changed = false
		for _, step := range score.Steps {
			for _, exit := range step.Exits {
				if ending[exit.To] && !ending[step.Name] {
					ending[step.Name] = true
					changed = true
				}
			}
		}
	}
	for _, step := range score.Steps {
		if !step.Background && !ending[step.Name] {
			return Score{}, scoreError(path, "step "+step.Name, "no path reaches an ending")
		}
	}
	return score, nil
}

func valueOrZero[T ~int | ~int64](value *T) T {
	if value != nil {
		return *value
	}
	return 0
}
