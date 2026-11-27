package loop

import (
	"crypto/sha256"
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

const defaultLearnedBudget = 4000
const lessonsDir = ".kapellmeister/learned/"

type LessonFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Bytes  int    `json:"bytes"`
}

type SkippedLesson struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

type LessonsRecord struct {
	Part    string          `json:"part"`
	Address string          `json:"address,omitempty"`
	Base    string          `json:"base"`
	Files   []LessonFile    `json:"files,omitempty"`
	Skipped []SkippedLesson `json:"skipped,omitempty"`
	Bytes   int             `json:"bytes"`
	Cut     bool            `json:"cut"`
}

type lessonKey struct{ part, address string }

type chairLessons struct {
	LessonsRecord
	text                              string
	totalBytes, totalLines, keptLines int
}

type textFault int

const (
	faultNone textFault = iota
	faultEncoding
	faultInvisible
	faultControl
)

func (f textFault) String() string {
	switch f {
	case faultEncoding:
		return "invalid UTF-8"
	case faultInvisible:
		return "invisible or direction-changing characters"
	case faultControl:
		return "control characters"
	}
	return ""
}

type lessonBlob struct {
	text, reason, raw string
	fault             textFault
}

func diagnoseText(raw string) string {
	if strings.HasPrefix(raw, "\x00\x00\xfe\xff") || strings.HasPrefix(raw, "\xff\xfe\x00\x00") {
		return "is UTF-32 encoded; convert the file to UTF-8"
	}
	if strings.HasPrefix(raw, "\xfe\xff") || strings.HasPrefix(raw, "\xff\xfe") {
		return "is UTF-16 encoded; convert the file to UTF-8"
	}
	line := 1
	var other, crlf string
	for i := 0; i < len(raw); {
		r, size := utf8.DecodeRuneInString(raw[i:])
		problem := ""
		switch {
		case r == utf8.RuneError && size == 1:
			problem = "has invalid UTF-8; convert the file to UTF-8"
		case r == '\ufeff' && i == 0:
			problem = "has a byte order mark; remove the BOM"
		case unicode.Is(unicode.Cf, r) || unicode.Is(unicode.Zl, r) || unicode.Is(unicode.Zp, r):
			problem = "has invisible or direction-changing characters; remove them"
		case r == '\r' && i+1 < len(raw) && raw[i+1] == '\n':
			if crlf == "" {
				crlf = fmt.Sprintf("line %d: has CRLF line endings; convert the file to LF", line)
			}
		case unicode.IsControl(r) && r != '\n' && r != '\t':
			problem = "has control characters; remove them"
		}
		if problem != "" && other == "" {
			other = fmt.Sprintf("line %d: %s", line, problem)
		}
		if r == '\n' {
			line++
		}
		i += size
	}
	if other != "" && crlf != "" {
		return other + " (also " + crlf + ")"
	}
	if other != "" {
		return other
	}
	return crlf
}

var lessonChairName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*\.[a-z0-9][a-z0-9-]*$`)

func chairFileName(address string) string {
	name, ensemble, ok := strings.Cut(address, "@")
	if !ok || ensemble == "" || !lessonChairName.MatchString(name) {
		return ""
	}
	return name + ".md"
}

func (e *Engine) loadLessons() {
	e.lessons = make(map[lessonKey]chairLessons)
	base, err := e.gitOutput("rev-parse", "HEAD")
	if err != nil {
		e.progress("  warning: lessons: cannot read base commit: %s", err)
		return
	}
	base = strings.TrimSpace(base)
	entries, err := e.gitOutput("ls-tree", "-l", "-z", base, "--", lessonsDir)
	if err != nil {
		e.progress("  warning: lessons: cannot list committed files: %s", err)
		return
	}
	tree := make(map[string][]string)
	for _, entry := range strings.Split(entries, "\x00") {
		meta, path, ok := strings.Cut(entry, "\t")
		if fields := strings.Fields(meta); ok && len(fields) == 4 {
			tree[path] = fields
		}
	}
	blobs := make(map[string]lessonBlob)
	read := func(path string) (lessonBlob, bool) {
		fields, ok := tree[path]
		if !ok {
			return lessonBlob{}, false
		}
		if blob, ok := blobs[path]; ok {
			return blob, true
		}
		blob := e.readLessonBlob(fields[0], fields[2])
		if blob.reason != "" {
			e.progress("  warning: lessons: skipping %s: %s", path, blob.reason)
		}
		blobs[path] = blob
		return blob, true
	}
	type seat struct {
		part, name string
		chair      Chair
	}
	var seats []seat
	score := e.playingScore()
	for group := 0; group < 3; group++ {
		for _, part := range score.Parts {
			priority, review := 0, false
			for _, step := range score.StepsOf(part.Name) {
				review = review || step.Kind == KindReview || step.Chairs == ChairsAll
			}
			if review {
				priority = 1
			}
			if part.Optional {
				priority = 2
			}
			if priority != group {
				continue
			}
			chairs := e.Config.Chairs.Of(part.Name)
			for i, chair := range chairs {
				name := ""
				if review {
					name = reviewChairName(part.Name, i, len(chairs))
				}
				seats = append(seats, seat{part.Name, name, chair})
			}
		}
	}
	budget := e.Config.LearnedBudget
	if budget <= 0 {
		budget = defaultLearnedBudget
	}
	for _, seat := range seats {
		l := chairLessons{LessonsRecord: LessonsRecord{Part: seat.part, Address: seat.chair.Address, Base: base}}
		paths := []string{lessonsDir + seat.part + ".md"}
		if file := chairFileName(seat.chair.Address); file != "" {
			paths = append(paths, lessonsDir+file)
		}
		var kept strings.Builder
		for _, path := range paths {
			blob, ok := read(path)
			if !ok {
				continue
			}
			if blob.reason != "" {
				l.Skipped = append(l.Skipped, SkippedLesson{Path: path, Reason: blob.reason})
				continue
			}
			l.Files = append(l.Files, LessonFile{Path: path, SHA256: fmt.Sprintf("%x", sha256.Sum256([]byte(blob.text))), Bytes: len(blob.text)})
			l.keepLines(&kept, blob.text, budget)
		}
		l.text, l.Bytes = kept.String(), kept.Len()
		e.lessons[lessonKey{seat.part, seat.chair.Address}] = l
		if len(l.Files) == 0 && len(l.Skipped) == 0 {
			continue
		}
		name := firstSet(seat.chair.Address, seat.name, seat.part)
		if l.Cut {
			e.progress("  lessons: %s cut to %d of %d bytes (kept %d of %d lines)", name, l.Bytes, l.totalBytes, l.keptLines, l.totalLines)
		}
		e.record(Record{RunID: e.Config.RunID, Step: "lessons", ChairName: seat.name, Lessons: &l.LessonsRecord})
	}
}

func (e *Engine) lessonsFor(step string, chair Chair) string {
	return lessonsSection(e.lessons[lessonKey{firstSet(e.playingScore().StepPart(step), step), chair.Address}])
}

func (e *Engine) readLessonBlob(mode, oid string) lessonBlob {
	blob := e.readTextBlob(mode, oid)
	if blob.reason != "" {
		return blob
	}
	text := blob.text
	lower := strings.ToLower(text)
	if strings.Contains(lower, strings.TrimSuffix(lessonsOpen, ">")) || strings.Contains(lower, strings.TrimSuffix(lessonsClose, ">")) {
		return lessonBlob{reason: "section marker"}
	}

	return blob
}

func (e *Engine) readTextBlob(mode, oid string) lessonBlob {
	return e.readTextBlobWithGitOptions(mode, oid, nil)
}

func (e *Engine) readTextBlobWithGitOptions(mode, oid string, options []string) lessonBlob {
	switch mode {
	case "120000":
		return lessonBlob{reason: "symbolic link"}
	case "100644", "100755":
		text, err := e.gitOutput(append(options, "cat-file", "blob", oid)...)
		if err != nil {
			return lessonBlob{reason: "cannot read committed file: " + err.Error()}
		}
		if !utf8.ValidString(text) {
			return lessonBlob{reason: faultEncoding.String(), fault: faultEncoding, raw: text}
		}
		if strings.ContainsFunc(text, func(r rune) bool {
			return unicode.Is(unicode.Cf, r) || unicode.Is(unicode.Zl, r) || unicode.Is(unicode.Zp, r)
		}) {
			return lessonBlob{reason: faultInvisible.String(), fault: faultInvisible, raw: text}
		}
		if strings.ContainsFunc(text, func(r rune) bool { return unicode.IsControl(r) && r != '\n' && r != '\t' }) {
			return lessonBlob{reason: faultControl.String(), fault: faultControl, raw: text}
		}
		return lessonBlob{text: text}
	default:
		return lessonBlob{reason: "not a regular file"}
	}
}

func (l *chairLessons) keepLines(kept *strings.Builder, text string, budget int) {
	for text != "" {
		line, rest, _ := strings.Cut(text, "\n")
		line += "\n"
		l.totalBytes += len(line)
		l.totalLines++
		if !l.Cut && len(line) <= budget-kept.Len() {
			kept.WriteString(line)
			l.keptLines++
		} else {
			l.Cut = true
		}
		text = rest
	}
}
