package loop

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
)

var nativeNonString = regexp.MustCompile(`(?i)^(?:~|null|true|false|yes|no|on|off|[-+]?(?:\.inf|\.nan|0x[0-9a-f_]+|0o[0-7_]+|0b[01_]+|[0-9][0-9_]*|(?:[0-9][0-9_]*\.[0-9_]*|\.[0-9_]+)(?:e[-+]?[0-9]+)?|[0-9][0-9_]*e[-+]?[0-9]+)|[0-9]{4}-[0-9]{1,2}-[0-9]{1,2}(?:[t ].*)?)$`)
var nativeKey = regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_-]*):(?: +(.*))?$`)
var nativeSuspiciousName = regexp.MustCompile(`^\s*(-\s+)?["']?name["']?\s*:`)

type nativeFrame struct {
	column   int
	sequence bool
	keys     map[string]bool
}
type nativeBlock struct{ column, indent, blank, blankLine int }

func nativeName(path string, data []byte) (string, error) {
	name := ""
	lines := strings.Split(strings.TrimPrefix(string(data), "\ufeff"), "\n")
	fail := func(n int, reason string) (string, error) {
		return "", fmt.Errorf("%s:%d: km could not read the front matter: %s", nativePath(path), n, reason)
	}
	if strings.HasPrefix(lines[0], "---") && strings.ContainsAny(strings.TrimSuffix(lines[0], "\r"), "\r\u0085\u2028\u2029") {
		return fail(1, "invalid line break")
	}
	if !strings.HasPrefix(lines[0], "---") || strings.TrimFunc(lines[0][3:], func(r rune) bool { return unicode.IsSpace(r) || r == '\ufeff' }) != "" {
		return "", nil
	}
	control := func(line string, n int) error {
		for _, r := range line {
			if r == '\r' {
				_, err := fail(n, "CR line endings")
				return err
			}
			if r != '\t' && unicode.IsControl(r) {
				_, err := fail(n, "control character")
				return err
			}
		}
		return nil
	}
	if err := control(lines[0], 1); err != nil {
		return "", err
	}
	frames := []nativeFrame{{column: 0, keys: map[string]bool{}}}
	pending := -1
	var block *nativeBlock
	lenient := false
	nameLine, nameComment := 0, false
	offenders := []int{}
	closed := false
	blockLine, commentedLine, trailingName := 0, 0, false
	lenientDigitLine := 0
	for i := 1; i < len(lines); i++ {
		line := lines[i]
		if err := control(line, i+1); err != nil {
			return "", err
		}
		if strings.TrimRight(line, " \t") == "---" {
			closed = true
			break
		}
		if strings.Contains(line, "---") {
			return fail(i+1, "contains ---")
		}
		if strings.ContainsAny(line, "\r\u0085\u2028\u2029\ufeff") {
			return fail(i+1, "invalid line break")
		}
		if nativeSuspiciousName.MatchString(line) {
			offenders = append(offenders, i+1)
		}
		indent := len(line) - len(strings.TrimLeft(line, " "))
		blank := indent == len(line)
		if block != nil {
			if block.indent == 0 {
				if blank {
					if indent > block.blank {
						block.blank = indent
						block.blankLine = i + 1
					}
					continue
				}
				if indent > block.column {
					block.indent = indent
					if block.blank > indent {
						return fail(block.blankLine, "block indentation")
					}
					continue
				}
			} else {
				if blank || indent >= block.indent {
					continue
				}
				if indent > block.column {
					return fail(i+1, "block indentation")
				}
			}
			block = nil
		}
		if blank || strings.HasPrefix(line[indent:], "#") {
			continue
		}
		rest := line[indent:]
		if strings.HasPrefix(rest, "\t") {
			return fail(i+1, "tab in indentation")
		}
		item := strings.HasPrefix(rest, "- ")
		if pending >= 0 {
			if indent > pending || (indent == pending && item) {
				frames = append(frames, nativeFrame{column: indent, sequence: item, keys: map[string]bool{}})
			}
			pending = -1
		}
		for len(frames) > 1 && (frames[len(frames)-1].column > indent || (frames[len(frames)-1].column == indent && frames[len(frames)-1].sequence && !item)) {
			frames = frames[:len(frames)-1]
		}
		f := &frames[len(frames)-1]
		if f.column != indent || f.sequence != item {
			return fail(i+1, "invalid indentation")
		}
		column := indent
		if item {
			rest = rest[2:]
			column = indent + 2
			if nativeKey.MatchString(rest) {
				frames = append(frames, nativeFrame{column: column, keys: map[string]bool{}})
				f = &frames[len(frames)-1]
			} else {
				kind, _, _, colon, e := nativeValue(rest, false)
				if e != "" || kind == "empty" || colon {
					return fail(i+1, "invalid sequence value")
				}
				if kind == "block" {
					blockLine = i + 1
					block = &nativeBlock{column: indent}
				}
				continue
			}
		}
		m := nativeKey.FindStringSubmatch(rest)
		if m == nil {
			return fail(i+1, "invalid mapping line")
		}
		key := m[1]
		if f.keys[key] {
			return fail(i+1, "repeated key")
		}
		f.keys[key] = true
		isName := column == 0 && key == "name"
		kind, value, comment, colon, e := nativeValue(m[2], isName)
		if e != "" {
			return fail(i+1, e)
		}
		if colon {
			if column != 0 || isName {
				return fail(i+1, "colon in plain scalar")
			}
			if strings.ContainsAny(key, "0123456789") {
				lenientDigitLine = i + 1
			}
			lenient = true
		}
		if isName {
			nameLine = i + 1
			trailingName = strings.TrimRightFunc(line, unicode.IsSpace) != line
			nameComment = comment
			if kind != "plain" && kind != "quoted" || value == "" {
				return fail(i+1, "name must be a string")
			}
			name = value
		}
		if kind == "empty" {
			if comment {
				commentedLine = i + 1
			}
			pending = column
		}
		if kind == "block" {
			blockLine = i + 1
			block = &nativeBlock{column: column}
		}
	}
	if !closed {
		return fail(len(lines), "no closing --- line")
	}
	if lenient {
		if nameComment {
			return fail(nameLine, "name comment with lenient value")
		}
		for _, n := range offenders {
			if n != nameLine {
				return fail(n, "name line with lenient value")
			}
		}
		if blockLine != 0 {
			return fail(blockLine, "block scalar with lenient value")
		}
		if commentedLine != 0 {
			return fail(commentedLine, "commented empty key with lenient value")
		}
		if trailingName {
			return fail(nameLine, "trailing whitespace on name with lenient value")
		}
		if lenientDigitLine != 0 {
			return fail(lenientDigitLine, "lenient value under a key with a digit")
		}
	}
	return name, nil
}

func nativeValue(s string, name bool) (kind, value string, comment, colon bool, err string) {
	s = strings.TrimLeft(s, " ")
	if s == "" || strings.HasPrefix(s, "#") {
		return "empty", "", strings.HasPrefix(s, "#"), false, ""
	}
	if s[0] == '\'' || s[0] == '"' {
		v, n, e := nativeQuote(s)
		if e != "" {
			return "", "", false, false, e
		}
		tail := s[n:]
		c, ok := nativeTail(tail)
		if !ok || (name && s[0] == '"' && strings.Contains(s[:n], `\`)) {
			return "", "", false, false, "invalid quoted scalar"
		}
		return "quoted", v, c, false, ""
	}
	if s[0] == '[' {
		pos := 1
		count := 0
		for {
			for pos < len(s) && s[pos] == ' ' {
				pos++
			}
			if pos >= len(s) || s[pos] == ']' {
				return "", "", false, false, "invalid flow sequence"
			}
			if s[pos] == '\'' || s[pos] == '"' {
				_, n, e := nativeQuote(s[pos:])
				if e != "" {
					return "", "", false, false, e
				}
				pos += n
			} else {
				start := pos
				for pos < len(s) && s[pos] != ',' && s[pos] != ']' {
					pos++
				}
				v := strings.Trim(s[start:pos], " ")
				if v == "" || strings.ContainsAny(v, ":[]{}&*!#\t") || strings.ContainsRune("-?:,[]{}#&*!|>'\"%@`", rune(v[0])) {
					return "", "", false, false, "invalid flow item"
				}
			}
			count++
			for pos < len(s) && s[pos] == ' ' {
				pos++
			}
			if pos >= len(s) {
				return "", "", false, false, "unclosed flow sequence"
			}
			if s[pos] == ']' {
				pos++
				break
			}
			if s[pos] != ',' {
				return "", "", false, false, "invalid flow sequence"
			}
			pos++
		}
		c, ok := nativeTail(s[pos:])
		if !ok || count == 0 {
			return "", "", false, false, "invalid flow sequence"
		}
		return "flow", "", c, false, ""
	}
	if s[0] == '|' || s[0] == '>' {
		n := 1
		if n < len(s) && (s[n] == '+' || s[n] == '-') {
			n++
		}
		c, ok := nativeTail(s[n:])
		if !ok {
			return "", "", false, false, "invalid block header"
		}
		return "block", "", c, false, ""
	}
	for j := 1; j < len(s); j++ {
		if s[j] == '#' && (s[j-1] == ' ' || s[j-1] == '\t') {
			s = s[:j]
			comment = true
			break
		}
	}
	s = strings.TrimRight(s, " \t")
	if name && nativeNonString.MatchString(s) {
		return "", "", false, false, "name must be quoted"
	}
	if s == "" || strings.ContainsRune("-?:,[]{}#&*!|>'\"%@`", rune(s[0])) || strings.ContainsAny(s, "{}[]\t") || strings.HasSuffix(s, ":") {
		return "", "", false, false, "invalid plain scalar"
	}
	return "plain", s, comment, strings.Contains(s, ": "), ""
}

func nativeTail(s string) (bool, bool) {
	if s == "" {
		return false, true
	}
	if s[0] != ' ' && s[0] != '\t' {
		return false, false
	}
	s = strings.TrimLeft(s, " \t")
	if s == "" {
		return false, true
	}
	return strings.HasPrefix(s, "#"), strings.HasPrefix(s, "#")
}
func nativeQuote(s string) (string, int, string) {
	q := s[0]
	var b strings.Builder
	for i := 1; i < len(s); i++ {
		c := s[i]
		if c == q {
			if q == '\'' && i+1 < len(s) && s[i+1] == q {
				b.WriteByte(q)
				i++
				continue
			}
			return b.String(), i + 1, ""
		}
		if c == '\\' && q == '"' {
			i++
			if i >= len(s) || !strings.ContainsRune(`\"nt/`, rune(s[i])) {
				return "", 0, "invalid escape"
			}
			switch s[i] {
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			default:
				b.WriteByte(s[i])
			}
			continue
		}
		b.WriteByte(c)
	}
	return "", 0, "unclosed quoted scalar"
}
