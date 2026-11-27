package loop

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
)

const ReplyMessage = "message"
const ReplyScreen = "screen"

func messageNonce() (string, error) {
	var nonce [8]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(nonce[:]), nil
}

func messagePrompt(nonce string) string {
	return fmt.Sprintf("\nGive your whole reply in your final message and write no file for it. This replaces any instruction to write a reply file, keep chat replies short, or reply with one short sentence. Start with a line `km-begin %s`, then a blank line, your reply, a blank line, and a line `km-end %s`; each marker line must hold nothing else. Call no tool after that message.\n", nonce, nonce)
}

func bracketedReply(capture, nonce string) (string, bool) {
	if nonce == "" {
		return "", false
	}
	lines := strings.Split(strings.ReplaceAll(capture, "\r\n", "\n"), "\n")
	begin, end := -1, -1
	for i, line := range lines {
		switch strings.TrimSpace(line) {
		case "km-begin " + nonce:
			if begin >= 0 {
				return "", false
			}
			begin = i
		case "km-end " + nonce:
			if end >= 0 {
				return "", false
			}
			end = i
		}
	}
	if begin < 0 || end <= begin {
		return "", false
	}
	body := lines[begin+1 : end]
	for i, line := range body {
		body[i] = strings.Map(cleanReplyRune, line)
	}
	for len(body) > 0 && strings.TrimSpace(body[0]) == "" {
		body = body[1:]
	}
	for len(body) > 0 && strings.TrimSpace(body[len(body)-1]) == "" {
		body = body[:len(body)-1]
	}
	return strings.Join(body, "\n"), true
}

func cleanReplyRune(r rune) rune {
	if r != '\n' && r != '\t' && unprintableRune(r) {
		return ' '
	}
	return r
}
