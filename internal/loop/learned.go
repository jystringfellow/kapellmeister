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

type lessonBlob struct{ text, reason string }

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
	seats := []seat{
		{"planner", "", e.Config.Chairs.Planner}, {"implementer", "", e.Config.Chairs.Implementer},
		{"gate", "", e.Config.Chairs.Gate}, {"classifier", "", e.Config.Chairs.Classifier},
	}
	for i, chair := range e.Config.Chairs.Reviewers {
		seats = append(seats, seat{"reviewer", ReviewerName(i, len(e.Config.Chairs.Reviewers)), chair})
	}
	if chair := e.Config.Chairs.Tester; chair != nil {
		seats = append(seats, seat{"tester", "", *chair})
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
	return lessonsSection(e.lessons[lessonKey{partOf(step), chair.Address}])
}

func (e *Engine) readLessonBlob(mode, oid string) lessonBlob {
	switch mode {
	case "120000":
		return lessonBlob{reason: "symbolic link"}
	case "100644", "100755":
		text, err := e.gitOutput("cat-file", "blob", oid)
		if err != nil {
			return lessonBlob{reason: "cannot read committed file: " + err.Error()}
		}
		if !utf8.ValidString(text) {
			return lessonBlob{reason: "invalid UTF-8"}
		}
		if strings.ContainsFunc(text, func(r rune) bool {
			return unicode.Is(unicode.Cf, r) || unicode.Is(unicode.Zl, r) || unicode.Is(unicode.Zp, r)
		}) {
			return lessonBlob{reason: "invisible or direction-changing characters"}
		}
		if strings.ContainsFunc(text, func(r rune) bool { return unicode.IsControl(r) && r != '\n' && r != '\t' }) {
			return lessonBlob{reason: "control characters"}
		}
		lower := strings.ToLower(text)
		if strings.Contains(lower, strings.TrimSuffix(lessonsOpen, ">")) || strings.Contains(lower, strings.TrimSuffix(lessonsClose, ">")) {
			return lessonBlob{reason: "section marker"}
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
