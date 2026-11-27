package loop

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const ReplyTranscript = "transcript"
const maxReplyRange = 8 << 20

var replyTranscriptWait = 5 * time.Second

type replyCapture struct {
	meter  *LogMeter
	step   Step
	before map[string]usageSnapshot
	err    error
}

func openReply(home string, step Step) replyCapture {
	if home == "" {
		home = claudeHome()
	}
	c := replyCapture{meter: &LogMeter{ClaudeHome: home}, step: step}
	if step.Chair.Provider == "claude_code" {
		c.before, c.err = c.meter.logs(step)
	}
	return c
}

type replyEntry struct {
	Type      string `json:"type"`
	Sidechain bool   `json:"isSidechain"`
	Message   struct {
		ID      string `json:"id"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"message"`
}

func (c replyCapture) read(ctx context.Context, nonce, screen string) (string, bool, string, string) {
	if c.step.Chair.Provider != "claude_code" {
		text, ok := bracketedReply(screen, nonce)
		return text, ok, ReplyScreen, bracketProblem(screen, nonce)
	}
	if c.err != nil {
		return "", false, ReplyTranscript, c.err.Error()
	}
	deadline := time.Now().Add(replyTranscriptWait)
	for {
		after, err := c.meter.logs(c.step)
		if err != nil {
			return "", false, ReplyTranscript, err.Error()
		}
		for path, before := range c.before {
			if before.parent == path {
				if a, ok := after[path]; !ok || a.size < before.size {
					return "", false, ReplyTranscript, "transcript disappeared or shrank"
				}
			}
		}
		grown, matches := 0, 0
		var final string
		var tool bool
		for path, log := range after {
			if path != log.parent {
				continue
			}
			start := c.before[path].size
			if log.size < start {
				return "", false, ReplyTranscript, "transcript shrank"
			}
			if log.size == start {
				continue
			}
			grown++
			if log.size-start > maxReplyRange {
				return "", false, ReplyTranscript, "transcript range exceeds limit"
			}
			data, err := readUsageRange(path, start, log.size)
			if err != nil {
				return "", false, ReplyTranscript, err.Error()
			}
			var id, text string
			hasNonce, finalTool := false, false
			for _, line := range bytes.Split(data, []byte{'\n'}) {
				if len(bytes.TrimSpace(line)) == 0 {
					continue
				}
				var kind struct {
					Type string `json:"type"`
				}
				if json.Unmarshal(line, &kind) != nil {
					return "", false, ReplyTranscript, "invalid transcript JSON"
				}
				if kind.Type != "assistant" {
					continue
				}
				var entry replyEntry
				if json.Unmarshal(line, &entry) != nil {
					return "", false, ReplyTranscript, "invalid transcript JSON"
				}
				if entry.Sidechain || entry.Type != "assistant" {
					continue
				}
				if entry.Message.ID == "" || entry.Message.ID != id {
					id = entry.Message.ID
					text = ""
					finalTool = false
				}
				for _, block := range entry.Message.Content {
					if block.Type == "text" {
						if strings.Contains(block.Text, nonce) {
							hasNonce = true
						}
						if text != "" {
							text += "\n"
						}
						text += block.Text
					}
					if block.Type == "tool_use" {
						finalTool = true
					}
				}
			}
			if hasNonce {
				matches++
				final, tool = text, finalTool
			}
		}
		if grown == 0 {
			text, ok := bracketedReply(screen, nonce)
			return text, ok, ReplyScreen, bracketProblem(screen, nonce)
		}
		if matches > 1 {
			return "", false, ReplyTranscript, "multiple transcripts contain the nonce"
		}
		if matches == 1 && strings.Contains(final, "km-end "+nonce) {
			if tool {
				return "", false, ReplyTranscript, "tool call in the final message"
			}
			text, ok := bracketedReply(final, nonce)
			if ok {
				return text, true, ReplyTranscript, ""
			}
			return text, ok, ReplyTranscript, bracketProblem(final, nonce)
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			if tool {
				return "", false, ReplyTranscript, "tool call in the final message"
			}
			return "", false, ReplyTranscript, bracketProblem(final, nonce)
		}
		interval := min(200*time.Millisecond, replyTranscriptWait/10)
		if interval <= 0 {
			interval = time.Millisecond
		}
		timer := time.NewTimer(min(interval, remaining))
		select {
		case <-ctx.Done():
			timer.Stop()
			return "", false, ReplyTranscript, ctx.Err().Error()
		case <-timer.C:
		}
	}
}
func bracketProblem(text, nonce string) string {
	if _, ok := bracketedReply(text, nonce); ok {
		return ""
	}
	begin, end := "km-begin "+nonce, "km-end "+nonce
	whole := func(marker string) bool {
		for _, line := range strings.Split(text, "\n") {
			if strings.TrimSpace(line) == marker {
				return true
			}
		}
		return false
	}
	b, e := whole(begin), whole(end)
	if (!b && strings.Contains(text, begin)) || (!e && strings.Contains(text, end)) {
		return "marker not on a line of its own"
	}
	if !b && !e {
		return "missing begin and end markers"
	}
	if !b {
		return "missing begin marker"
	}
	if !e {
		return "missing end marker"
	}
	return fmt.Sprint("repeated or out of order markers")
}
