package tune

import (
	"crypto/sha256"
	"fmt"
	"regexp"
	"strings"

	"github.com/jystringfellow/kapellmeister/internal/initrepo"
)

type Comparison struct {
	ProvedDigest      string `json:"proved_digest"`
	CandidateDigest   string `json:"candidate_digest"`
	ProvedCommands    string `json:"proved_commands"`
	CandidateCommands string `json:"candidate_commands"`
}

var stepLabel = regexp.MustCompile(`^run (?:'[^'\n]*'|"[^"$` + "`" + `\\\n]*"|[A-Za-z0-9_.-]+) `)

func SameCommands(proved, candidate string) (Comparison, bool) {
	a, okA := commands(proved)
	b, okB := commands(candidate)
	c := Comparison{ProvedDigest: fmt.Sprintf("%x", sha256.Sum256([]byte(a))), CandidateDigest: fmt.Sprintf("%x", sha256.Sum256([]byte(b))), ProvedCommands: a, CandidateCommands: b}
	return c, okA && okB && a == b
}

func commands(script string) (string, bool) {
	if strings.ContainsAny(script, "\r\x00") {
		return script, false
	}
	template, _ := initrepo.RenderVerify("true")
	prelude, _, _ := strings.Cut(template, "run 'verify' 'true'")
	var lines []string
	var line strings.Builder
	quote := byte(0)
	beganQuoted := false
	wordStart := true
	for i := 0; i < len(script); i++ {
		c := script[i]
		if c == '\n' {
			if quote != 0 {
				line.WriteByte(c)
				continue
			}
			value := line.String()
			if !beganQuoted {
				value = strings.Trim(value, " \t")
			}
			if value != "" {
				lines = append(lines, value)
			}
			line.Reset()
			beganQuoted = false
			wordStart = true
			continue
		}
		if quote == '\'' {
			line.WriteByte(c)
			if c == '\'' {
				quote = 0
			}
			continue
		}
		if c == '\\' {
			if i+1 >= len(script) || script[i+1] == '\n' {
				return script, false
			}
			line.WriteByte(c)
			i++
			line.WriteByte(script[i])
			wordStart = false
			continue
		}
		if c == '`' || c == '$' && i+1 < len(script) && (script[i+1] == '(' || script[i+1] == '{') {
			// The wrapper's fixed initialization is compared verbatim; expansions elsewhere require proof.
			end := strings.IndexByte(script[i:], '\n')
			if end < 0 {
				end = len(script) - i
			}
			full := line.String() + script[i:i+end]
			if !strings.Contains(prelude, full+"\n") {
				return script, false
			}
			quote = 0
			line.WriteString(script[i : i+end])
			i += end - 1
			continue
		}
		if quote == '"' {
			line.WriteByte(c)
			if c == '"' {
				quote = 0
			}
			continue
		}
		if c == '<' && i+1 < len(script) && script[i+1] == '<' {
			return script, false
		}
		if c == '#' && wordStart && !(i == 0 && strings.HasPrefix(script, "#!")) {
			for i < len(script) && script[i] != '\n' {
				i++
			}
			i--
			continue
		}
		if c == '\'' || c == '"' {
			quote = c
			if line.Len() == 0 {
				beganQuoted = true
			}
		}
		line.WriteByte(c)
		wordStart = strings.ContainsRune(" \t;&|()", rune(c))
	}
	if quote != 0 {
		return script, false
	}
	value := line.String()
	if !beganQuoted {
		value = strings.Trim(value, " \t")
	}
	if value != "" {
		lines = append(lines, value)
	}

	var fixedLines []string
	for _, l := range strings.Split(prelude, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			fixedLines = append(fixedLines, l)
		}
	}
	if len(lines) < len(fixedLines)+3 {
		return script, false
	}
	for i, l := range fixedLines {
		if lines[i] != l {
			return script, false
		}
	}
	last := len(lines) - 2
	if lines[last] != "rm -f \"$log\"" || lines[last+1] != "printf '%s\\n' 'verify ok: verify'" {
		return script, false
	}
	for i := len(fixedLines); i < last; i++ {
		l := lines[i]
		loc := stepLabel.FindStringIndex(l)
		if loc == nil {
			return script, false
		}
		command := strings.ReplaceAll(l[loc[1]:], "'\"'\"'", "")
		if len(command) < 2 || command[0] != '\'' || command[len(command)-1] != '\'' || strings.Contains(command[1:len(command)-1], "'") {
			return script, false
		}
		lines[i] = stepLabel.ReplaceAllString(l, "run LABEL ")
	}
	return strings.Join(lines, "\n"), true
}
