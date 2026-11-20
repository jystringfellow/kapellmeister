package loop

import (
	"embed"
	"slices"
)

//go:embed builtin
var builtinScores embed.FS

func BuiltinScoreNames() []string { return []string{"review"} }

func BuiltinScoreText(name string) (string, bool) {
	data, err := builtinScores.ReadFile("builtin/" + name + ".toml")
	return string(data), err == nil
}

func BuiltinPartText(score, part string) string {
	data, err := builtinScores.ReadFile("builtin/" + score + "/" + part + ".md")
	if err != nil {
		panic(err)
	}
	return string(data)
}

func BuiltinScore(name string) (Score, bool) {
	text, ok := BuiltinScoreText(name)
	if !ok {
		return Score{}, false
	}
	score, err := decodeScoreWithPartText(text, "builtin/"+name+".toml", name, true)
	if err != nil {
		panic(err)
	}
	score.Source = "built-in"
	var parts []Part
	for _, step := range score.Steps {
		if slices.ContainsFunc(parts, func(p Part) bool { return p.Name == step.Part }) {
			continue
		}
		part, _ := score.Part(step.Part)
		part.Prompt, err = decodePart(BuiltinPartText(name, part.Name), "builtin/"+name+"/"+part.Name+".md", score.StepsOf(part.Name))
		if err != nil {
			panic(err)
		}
		parts = append(parts, part)
	}
	classifier, _ := score.Part(score.Classifier)
	score.Parts = append(parts, classifier)
	return score, true
}
