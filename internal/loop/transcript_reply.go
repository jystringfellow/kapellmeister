package loop

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"regexp"
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
	poll   func()
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

type replyBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}
type replyContent []replyBlock

func (c *replyContent) UnmarshalJSON(data []byte) error {
	var text string
	if len(data) > 0 && data[0] == '"' {
		if err := json.Unmarshal(data, &text); err != nil {
			return err
		}
		*c = replyContent{{Type: "text", Text: text}}
		return nil
	}
	type content replyContent
	return json.Unmarshal(data, (*content)(c))
}

type replyEntry struct {
	Type      string `json:"type"`
	Sidechain bool   `json:"isSidechain"`
	Message   struct {
		ID      string       `json:"id"`
		Content replyContent `json:"content"`
	} `json:"message"`
}

const replyChunk = 64 << 10
const replySizeCause = "the final message exceeds 8 MiB"

var reverseAssistant = regexp.MustCompile(`"tnatsissa"\s*:\s*"epyt"`)

type transcriptTail struct {
	text                  string
	tool, assistant, half bool
	cause                 string
}

func transcriptHolds(f *os.File, start, end int64, nonce string) (bool, error) {
	buf := make([]byte, replyChunk)
	carry := []byte(nil)
	for pos := start; pos < end; {
		n := min(int64(len(buf)), end-pos)
		count, err := f.ReadAt(buf[:n], pos)
		if err != nil && err != io.EOF {
			return false, err
		}
		if count == 0 {
			return false, io.ErrUnexpectedEOF
		}
		data := append(carry, buf[:count]...)
		if bytes.Contains(data, []byte(nonce)) {
			return true, nil
		}
		keep := min(len(data), max(0, len(nonce)-1))
		carry = append(carry[:0], data[len(data)-keep:]...)
		pos += int64(count)
	}
	return false, nil
}

func readTranscriptTail(path string, start, end int64, nonce string) (transcriptTail, bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return transcriptTail{}, false, err
	}
	defer f.Close()
	var holds, checked bool
	var scanErr error
	checkHolds := func() bool {
		if !checked {
			holds, scanErr = transcriptHolds(f, start, end, nonce)
			checked = true
		}
		return holds
	}
	var result transcriptTail
	var last [1]byte
	if _, err = f.ReadAt(last[:], end-1); err != nil {
		return result, holds, err
	}
	result.half = last[0] != '\n'
	partialStart := false
	if start > 0 {
		if _, err = f.ReadAt(last[:], start-1); err != nil {
			return result, holds, err
		}
		partialStart = last[0] != '\n'
	}
	var pending []byte
	oversized := false
	var markerCarry []byte
	assistantHint := false
	var id string
	var groups [][]string
	total := 0
	skippingTail := result.half
	process := func(line []byte, tooBig, hint bool) bool {
		if skippingTail {
			skippingTail = false
			return false
		}
		if tooBig {
			if hint && checkHolds() {
				result.cause = replySizeCause
				return true
			}
			return false
		}
		if len(bytes.TrimSpace(line)) == 0 {
			return false
		}
		var kind struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(line, &kind) != nil {
			if checkHolds() {
				result.cause = "invalid transcript JSON"
				return true
			}
			return false
		}
		if kind.Type != "assistant" {
			return false
		}
		var e replyEntry
		if json.Unmarshal(line, &e) != nil {
			if checkHolds() {
				result.cause = "invalid transcript JSON"
				return true
			}
			return false
		}
		if e.Sidechain {
			return false
		}
		if result.assistant && (id == "" || e.Message.ID == "" || e.Message.ID != id) {
			return true
		}
		if !result.assistant {
			id = e.Message.ID
			result.assistant = true
		}
		total += len(line) + 1
		if total > maxReplyRange {
			if checkHolds() {
				result.cause = replySizeCause
			}
			return true
		}
		var texts []string
		for _, block := range e.Message.Content {
			if block.Type == "text" {
				texts = append(texts, block.Text)
			}
			if block.Type == "tool_use" {
				result.tool = true
			}
		}
		groups = append(groups, texts)
		return false
	}
	done := false
	for pos := end; pos > start && !done; {
		n := min(int64(replyChunk), pos-start)
		pos -= n
		buf := make([]byte, n)
		if _, err = f.ReadAt(buf, pos); err != nil {
			return result, holds, err
		}
		for i := len(buf) - 1; i >= 0; i-- {
			if buf[i] == '\n' {
				if oversized && reverseAssistant.Match(markerCarry) {
					assistantHint = true
				}
				if pos+int64(i) == end-1 {
					continue
				}
				if !oversized {
					for l, r := 0, len(pending)-1; l < r; l, r = l+1, r-1 {
						pending[l], pending[r] = pending[r], pending[l]
					}
				}
				if process(pending, oversized, assistantHint) {
					done = true
					break
				}
				pending = nil
				oversized = false
				assistantHint = false
				markerCarry = nil
			} else if !oversized {
				pending = append(pending, buf[i])
				if len(pending) > maxReplyRange {
					oversized = true
					markerCarry = append(markerCarry, pending...)
					pending = nil
				}
			} else {
				markerCarry = append(markerCarry, buf[i])
			}
			if oversized && len(markerCarry) >= replyChunk {
				if reverseAssistant.Match(markerCarry) {
					assistantHint = true
				}
				keep := min(len(markerCarry), 64)
				markerCarry = append([]byte(nil), markerCarry[len(markerCarry)-keep:]...)
			}
		}
	}
	if !done && !partialStart {
		if oversized && reverseAssistant.Match(markerCarry) {
			assistantHint = true
		}
		if !oversized {
			for l, r := 0, len(pending)-1; l < r; l, r = l+1, r-1 {
				pending[l], pending[r] = pending[r], pending[l]
			}
		}
		process(pending, oversized, assistantHint)
	}
	var texts []string
	for i := len(groups) - 1; i >= 0; i-- {
		texts = append(texts, groups[i]...)
	}
	result.text = strings.Join(texts, "\n")
	if !checked && strings.Contains(result.text, nonce) {
		holds = true
		checked = true
	}
	if !checked {
		checkHolds()
	}
	return result, holds, scanErr
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
	interval := min(200*time.Millisecond, replyTranscriptWait/10)
	if interval <= 0 {
		interval = time.Millisecond
	}
	previous := map[string]int64{}
	seen := map[string]time.Time{}
	extended := false
	for {
		if c.poll != nil {
			c.poll()
		}
		after, err := c.meter.logs(c.step)
		if err != nil {
			return "", false, ReplyTranscript, err.Error()
		}
		observed := time.Now()
		for path, before := range c.before {
			if before.parent == path {
				if a, ok := after[path]; !ok || a.size < before.size {
					return "", false, ReplyTranscript, "transcript disappeared or shrank"
				}
			}
		}
		grown, matches := 0, 0
		var selected transcriptTail
		var selectedPath string
		holders, half, changing := false, false, false
		var holder transcriptTail
		sizes := map[string]int64{}
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
			sizes[path] = log.size
			if previous[path] != log.size {
				seen[path] = observed
			}
			tail, holds, e := readTranscriptTail(path, start, log.size, nonce)
			if e != nil {
				return "", false, ReplyTranscript, e.Error()
			}
			if holds {
				holders = true
				holder = tail
				if tail.cause != "" {
					return "", false, ReplyTranscript, tail.cause
				}
				if tail.half {
					half = true
				}
				if prev, ok := previous[path]; !ok || prev != log.size {
					changing = true
				}
			}
			if strings.Contains(tail.text, nonce) {
				matches++
				selected = tail
				selectedPath = path
			}
		}
		if grown == 0 {
			text, ok := bracketedReply(screen, nonce)
			return text, ok, ReplyScreen, bracketProblem(screen, nonce)
		}
		if matches > 1 {
			return "", false, ReplyTranscript, "multiple transcripts contain the nonce"
		}
		settled := matches == 1 && !selected.half && previous[selectedPath] == sizes[selectedPath] && sizes[selectedPath] > 0 && observed.Sub(seen[selectedPath]) >= interval
		if settled && strings.Contains(selected.text, "km-end "+nonce) {
			if selected.tool {
				return "", false, ReplyTranscript, "tool call in the final message"
			}
			text, ok := transcriptReply(selected.text, nonce)
			if ok {
				return text, true, ReplyTranscript, ""
			}
			return "", false, ReplyTranscript, bracketProblem(selected.text, nonce)
		}
		if matches == 1 && !selected.half && !settled && !extended && strings.Contains(selected.text, "km-end "+nonce) {
			stableAt := seen[selectedPath].Add(interval)
			if stableAt.After(deadline) {
				deadline = stableAt
				extended = true
			}
		}
		if !time.Now().Before(deadline) {
			if half {
				return "", false, ReplyTranscript, "the transcript ended in a half-written line"
			}
			if changing {
				return "", false, ReplyTranscript, "the transcript was still being written"
			}
			if matches == 1 {
				if selected.tool {
					return "", false, ReplyTranscript, "tool call in the final message"
				}
				return "", false, ReplyTranscript, bracketProblem(selected.text, nonce)
			}
			if !holders {
				return "", false, ReplyTranscript, "no transcript holds the nonce"
			}
			if !holder.assistant {
				return "", false, ReplyTranscript, "no final message in the transcript"
			}
			if holder.tool {
				return "", false, ReplyTranscript, "tool call in the final message"
			}
			return "", false, ReplyTranscript, bracketProblem(holder.text, nonce)
		}
		previous = sizes
		timer := time.NewTimer(min(interval, time.Until(deadline)))
		select {
		case <-ctx.Done():
			timer.Stop()
			return "", false, ReplyTranscript, ctx.Err().Error()
		case <-timer.C:
		}
	}
}

func bracketProblem(text, nonce string) string {
	if _, ok := transcriptReply(text, nonce); ok {
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
	return "repeated or out of order markers"
}
