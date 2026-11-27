package loop

import (
	"os"
	"strings"
)

func FocusBlock(focus string) string {
	if focus == "" {
		return ""
	}
	return "Your focus in this ensemble: " + focus + "\nOther chairs playing the same part may cover other angles; concentrate on yours.\n"
}

func (e *Engine) brief(spec ScoreStep, h handoff, path string, chair Chair) string {
	if chair.Reply == ReplyMessage {
		path = finalMessage
	}
	cfg := e.Config
	lessons := e.lessonsFor(spec.Name, chair)
	switch spec.Brief {
	case "plan":
		askSize := spec.Sizes && e.sizing().Mode != SizingOff && e.sizing().Decider == DeciderPlanner
		return planPrompt(cfg.Task, path, askSize, lessons) + inputNotes(h) + FocusBlock(chair.Focus)
	case "implement":
		return implementPrompt(cfg.Task, h.path(0), path, cfg.CheckCommand, cfg.VerifyCommand, h.feedback, h.decide, h.test, lessons) + FocusBlock(chair.Focus)
	case "review":
		return reviewPrompt(cfg.Task, h.path(0), h.path(1), cfg.VerifyCommand, h.verify, h.verifyPath, path, lessons) + FocusBlock(chair.Focus)
	case "gate":
		var reviews, failed []string
		if len(h.inputs) > 2 {
			reviews = h.inputs[2].files
			failed = h.inputs[2].failed
		}
		return gatePrompt(cfg.Task, h.path(0), h.path(1), reviews, failed, cfg.VerifyCommand, h.verify, h.verifyPath, path, lessons) + FocusBlock(chair.Focus)
	case "test-plan":
		return testPlanPrompt(cfg.Task, h.path(0), path, cfg.ScratchDir, e.chairTimeout(chair), os.Getpid(), lessons, cfg.CAOURL, cfg.StateDir) + FocusBlock(chair.Focus)
	case "test":
		return testPrompt(cfg.Task, h.path(0), h.path(1), h.path(2), cfg.VerifyCommand, h.verify, h.verifyPath, cfg.ScratchDir, path, e.chairTimeout(chair), os.Getpid(), lessons, cfg.CAOURL, cfg.StateDir, e.measurements) + FocusBlock(chair.Focus)
	}
	part, _ := e.playingScore().Part(spec.Part)
	template := part.Prompt[spec.Name]
	if chair.Reply == ReplyMessage && e.playingScore().Source == "built-in" {
		template = strings.NewReplacer("Do not modify any file other than {reply}. When done, reply with one short sentence.", "Do not write or modify any file; your reply goes in {reply}.", "Write your review to {reply}", "Give your review in {reply}", "Write to {reply}", "Give in {reply}").Replace(template)
	}
	instructions := strings.NewReplacer("{reply}", path, "{inputs}", strings.Join(h.files, ", "), "{plan}", h.path(0), "{verify}", h.verifyPath, "{verification}", strings.TrimRight(verifyLine(cfg.VerifyCommand, h.verify, h.verifyPath), "\n"), "{check}", cfg.CheckCommand, "{scratch}", cfg.ScratchDir).Replace(template)
	prompt := preamble(spec.Part) + taskBlock(cfg.Task) + lessons + instructions + "\n" + FocusBlock(chair.Focus)
	prompt += inputNotes(h)
	if spec.Kind == KindTest {
		prompt += "Everything you create stays under " + cfg.ScratchDir + ".\n"
	}
	if chair.Reply != ReplyMessage {
		prompt += "Write your reply to " + path + ".\n"
	}
	if spec.Kind != KindProduce {
		if chair.Reply == ReplyMessage {
			prompt += "Put APPROVE or CHANGES alone on the last line before the closing km-end line.\n"
		} else {
			prompt += "Put APPROVE or CHANGES alone on the last line of the reply file.\n"
		}
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
	if e.classifierChair().Reply == ReplyMessage {
		out = finalMessage
	}
	score := e.playingScore()
	part, ok := score.Part(score.Classifier)
	if !ok || part.Prompt["classify"] == "" {
		part, _ = DefaultScore().Part(DefaultScore().Classifier)
	}
	step, _ := score.Step(subject)
	return classifyPartPrompt(part, step.Kind, subject, path, out, e.lessonsFor("classify-"+subject, e.classifierChair())) + FocusBlock(e.classifierChair().Focus)
}

func inputNotes(h handoff) string {
	var out string
	if len(h.files) > 0 {
		out += "Read " + strings.Join(h.files, ", ") + ".\n"
	}
	var failed []string
	for _, input := range h.inputs {
		failed = append(failed, input.failed...)
	}
	if len(failed) > 0 {
		out += "These chairs failed to finish their reply: " + strings.Join(failed, ", ") + ". Their reply files are missing.\n"
	}
	return out
}
