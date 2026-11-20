package loop

import (
	"os"
	"strings"
)

func (e *Engine) brief(spec ScoreStep, h handoff, path string, chair Chair) string {
	cfg := e.Config
	lessons := e.lessonsFor(spec.Name, chair)
	switch spec.Brief {
	case "plan":
		askSize := spec.Sizes && e.sizing().Mode != SizingOff && e.sizing().Decider == DeciderPlanner
		return planPrompt(cfg.Task, path, askSize, lessons)
	case "implement":
		return implementPrompt(cfg.Task, h.path(0), path, cfg.CheckCommand, cfg.VerifyCommand, h.feedback, h.decide, h.test, lessons)
	case "review":
		return reviewPrompt(cfg.Task, h.path(0), h.path(1), cfg.VerifyCommand, h.verify, h.verifyPath, path, lessons)
	case "gate":
		var reviews, failed []string
		if len(h.inputs) > 2 {
			reviews = h.inputs[2].files
			failed = h.inputs[2].failed
		}
		return gatePrompt(cfg.Task, h.path(0), h.path(1), reviews, failed, cfg.VerifyCommand, h.verify, h.verifyPath, path, lessons)
	case "test-plan":
		return testPlanPrompt(cfg.Task, h.path(0), path, cfg.ScratchDir, e.chairTimeout(chair), os.Getpid(), lessons, cfg.CAOURL, cfg.StateDir)
	case "test":
		return testPrompt(cfg.Task, h.path(0), h.path(1), h.path(2), cfg.VerifyCommand, h.verify, h.verifyPath, cfg.ScratchDir, path, e.chairTimeout(chair), os.Getpid(), lessons, cfg.CAOURL, cfg.StateDir, e.measurements)
	}
	part, _ := e.playingScore().Part(spec.Part)
	instructions := strings.NewReplacer("{reply}", path, "{inputs}", strings.Join(h.files, ", "), "{plan}", h.path(0), "{verify}", h.verifyPath, "{verification}", strings.TrimRight(verifyLine(cfg.VerifyCommand, h.verify, h.verifyPath), "\n"), "{check}", cfg.CheckCommand, "{scratch}", cfg.ScratchDir).Replace(part.Prompt[spec.Name])
	prompt := preamble(spec.Part) + taskBlock(cfg.Task) + lessons + instructions + "\n"
	if len(h.files) > 0 {
		prompt += "Read " + strings.Join(h.files, ", ") + ".\n"
	}
	if spec.Kind == KindTest {
		prompt += "Everything you create stays under " + cfg.ScratchDir + ".\n"
	}
	prompt += "Write your reply to " + path + ".\n"
	if spec.Kind != KindProduce {
		prompt += "Put APPROVE or CHANGES alone on the last line of the reply file.\n"
	}
	return prompt
}

func (e *Engine) classifierChair() Chair {
	chairs := e.Config.Chairs.Of(e.playingScore().Classifier)
	if len(chairs) > 0 {
		return chairs[0]
	}
	return e.Config.Chairs.Classifier
}

func (e *Engine) classifyBrief(subject, path, out string) string {
	score := e.playingScore()
	part, ok := score.Part(score.Classifier)
	if !ok || part.Prompt["classify"] == "" {
		part, _ = DefaultScore().Part(DefaultScore().Classifier)
	}
	step, _ := score.Step(subject)
	return classifyPartPrompt(part, step.Kind, subject, path, out, e.lessonsFor("classify-"+subject, e.classifierChair()))
}
