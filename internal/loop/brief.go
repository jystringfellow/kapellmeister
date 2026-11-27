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
	instructions := part.Prompt[spec.Name]
	if part.From == "" {
		if chair.Reply == ReplyMessage && e.playingScore().Source == "built-in" {
			instructions = strings.NewReplacer("Do not modify any file other than {reply}. When done, reply with one short sentence.", "Do not write or modify any file; your reply goes in {reply}.", "Write your review to {reply}", "Give your review in {reply}", "Write to {reply}", "Give in {reply}").Replace(instructions)
		}
		instructions = strings.NewReplacer("{reply}", path, "{inputs}", strings.Join(h.files, ", "), "{plan}", h.path(0), "{verify}", h.verifyPath, "{verification}", strings.TrimRight(verifyLine(cfg.VerifyCommand, h.verify, h.verifyPath), "\n"), "{check}", cfg.CheckCommand, "{scratch}", cfg.ScratchDir).Replace(instructions)
	} else if chair.SuppliesPart(part.From, part.FromSHA256) {
		instructions = ""
	} else {
		instructions = "Instructions from " + part.From + " follow, quoted as written:\n" + instructions + "\n" + borrowedEndLine(part.From)
	}
	prompt := preamble(spec.Part) + taskBlock(cfg.Task) + lessons
	if instructions != "" || part.From == "" {
		prompt += instructions + "\n"
	}
	prompt += FocusBlock(chair.Focus)
	prompt += inputNotes(h)
	if part.From != "" && (cfg.VerifyCommand == "" || h.verifyPath != "") {
		prompt += verifyLine(cfg.VerifyCommand, h.verify, h.verifyPath)
	}
	if spec.Kind == KindTest {
		prompt += "Everything you create stays under " + cfg.ScratchDir + ".\n"
	}
	prompt += replyLine(chair, path)
	if part.From != "" && (spec.Kind != KindProduce || spec.Writes == "reply") {
		prompt += modifyLine(chair, spec.Kind, path, cfg.ScratchDir)
	}
	if spec.Kind != KindProduce {
		prompt += verdictLine(chair)
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

func replyLine(chair Chair, path string) string {
	if chair.Reply == ReplyMessage {
		return ""
	}
	return "Write your reply to " + path + ".\n"
}

func modifyLine(chair Chair, kind StepKind, path, scratch string) string {
	line := "Do not modify any file other than " + path
	if chair.Reply == ReplyMessage {
		line = "Do not write or modify any file"
	}
	if kind == KindTest {
		line += ", apart from what you create under " + scratch
	}
	return line + ".\n"
}

func verdictLine(chair Chair) string {
	if chair.Reply == ReplyMessage {
		return "Put APPROVE or CHANGES alone on the last line before the closing km-end line.\n"
	}
	return "Put APPROVE or CHANGES alone on the last line of the reply file.\n"
}
