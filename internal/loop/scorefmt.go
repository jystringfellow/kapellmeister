package loop

import (
	"fmt"
	"strconv"
	"strings"
)

func (s Score) File() string {
	var out strings.Builder
	if s.Name == "default" {
		out.WriteString("# File scores omit sizing, lesson proposals, and the test-plan tier policy alias.\n")
	}
	if s.Description != "" {
		fmt.Fprintf(&out, "description = %q\n", s.Description)
	}
	if s.Rounds > 0 {
		fmt.Fprintf(&out, "rounds = %d\n", s.Rounds)
	}
	for _, part := range s.Parts {
		if part.Name == s.Classifier {
			continue
		}
		memory := part.Memory
		if memory == "" {
			memory = MemoryFresh
		}
		fmt.Fprintf(&out, "\n[parts.%s]\nmemory = %q\n", part.Name, memory)
		if part.From != "" {
			fmt.Fprintf(&out, "from = %q\n", part.From)
		}
		if part.Optional {
			out.WriteString("optional = true\n")
		}
		if part.ResumeTokens > 0 {
			fmt.Fprintf(&out, "resume_tokens = %d\n", part.ResumeTokens)
		}
		if part.ResumeSteps > 0 {
			fmt.Fprintf(&out, "resume_steps = %d\n", part.ResumeSteps)
		}
	}
	for _, step := range s.Steps {
		fmt.Fprintf(&out, "\n[[steps]]\nname = %q\nkind = %q\npart = %q\n", step.Name, step.Kind, step.Part)
		if len(step.Reads) > 0 {
			reads := make([]string, len(step.Reads))
			for i, read := range step.Reads {
				reads[i] = strconv.Quote(read)
			}
			fmt.Fprintf(&out, "reads = [%s]\n", strings.Join(reads, ", "))
		}
		if step.Writes != "" {
			fmt.Fprintf(&out, "writes = %q\n", step.Writes)
		}
		if step.Chairs == ChairsAll {
			out.WriteString("chairs = \"all\"\n")
		}
		if step.Background {
			out.WriteString("background = true\n")
		}
		if step.ApproveWhen != "" {
			fmt.Fprintf(&out, "approve_when = %q\n", step.ApproveWhen)
		}
		if step.Escalate {
			out.WriteString("escalate = true\n")
		}
		if len(step.Exits) > 0 {
			exits := make([]string, len(step.Exits))
			for i, exit := range step.Exits {
				exits[i] = fmt.Sprintf("%s = %q", exit.Name, exit.To)
			}
			fmt.Fprintf(&out, "next = { %s }\n", strings.Join(exits, ", "))
		}
	}
	return out.String()
}
