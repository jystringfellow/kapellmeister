package loop

import (
	"fmt"
	"regexp"
	"strings"
)

var partPlaceholder = regexp.MustCompile(`\{[a-z][a-z0-9_]*\}`)
var partHeading = regexp.MustCompile(`^## step: (.+)$`)

func decodePart(text, path string, steps []ScoreStep) (map[string]string, error) {
	firstLine, _, _ := strings.Cut(strings.TrimSpace(text), "\n")
	if firstLine == "---" {
		return nil, fmt.Errorf("%s: front matter is not allowed", path)
	}
	sections := map[string]string{}
	var common strings.Builder
	name := ""
	for _, line := range strings.Split(text, "\n") {
		if h := partHeading.FindStringSubmatch(line); h != nil {
			name = h[1]
			found := false
			for _, step := range steps {
				if step.Name == name {
					found = true
				}
			}
			if !found {
				return nil, fmt.Errorf("%s: step %s: part does not play this step", path, name)
			}
			if _, ok := sections[name]; ok {
				return nil, fmt.Errorf("%s: step %s: duplicate heading", path, name)
			}
			sections[name] = ""
			continue
		}
		if name == "" {
			common.WriteString(line + "\n")
		} else {
			sections[name] += line + "\n"
		}
	}
	out := map[string]string{}
	for _, step := range steps {
		value := strings.TrimSpace(common.String())
		if len(sections) > 0 {
			value = strings.TrimSpace(value + "\n\n" + strings.TrimSpace(sections[step.Name]))
		}
		if value == "" {
			return nil, fmt.Errorf("%s: step %s: empty instructions", path, step.Name)
		}
		for _, placeholder := range partPlaceholder.FindAllString(value, -1) {
			switch placeholder {
			case "{plan}", "{reply}", "{inputs}", "{verify}", "{verification}", "{check}", "{scratch}":
			default:
				return nil, fmt.Errorf("%s: step %s: unknown placeholder %s", path, step.Name, placeholder)
			}
		}
		if len(step.Reads) == 0 {
			for _, placeholder := range []string{"{plan}", "{inputs}"} {
				if strings.Contains(value, placeholder) {
					return nil, fmt.Errorf("%s: step %s: placeholder %s has nothing to fill it: the step reads nothing", path, step.Name, placeholder)
				}
			}
		}
		out[step.Name] = value
	}
	return out, nil
}
