package loop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	DefaultLessonsTimeout = 5 * time.Minute
	maxLessonsReply       = 32 * 1024
	maxLessons            = 5
	maxLessonRunes        = 400
	maxEvidenceRunes      = 300
	maxLessonsBytes       = 4096
)

type Lesson struct {
	Part     string `json:"part"`
	Lesson   string `json:"lesson"`
	Evidence string `json:"evidence"`
}

type LessonsReport struct {
	Proposed int    `json:"proposed"`
	Reason   string `json:"reason,omitempty"`
}

var (
	errLessonsUnreadable = errors.New("the answer was not readable")
	errLessonsTooLarge   = errors.New("the answer was too large")
	errLessonsFormat     = errors.New("the answer was not a JSON array of lessons")
)

func ReadLessonsReply(path string) (string, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("%w at %s", errNoReply, path)
	}
	if err != nil || !info.Mode().IsRegular() {
		return "", errLessonsUnreadable
	}
	if info.Size() > maxLessonsReply {
		return "", errLessonsTooLarge
	}
	file, err := os.Open(path)
	if err != nil {
		return "", errLessonsUnreadable
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		return "", errLessonsUnreadable
	}
	data, err := io.ReadAll(io.LimitReader(file, maxLessonsReply+1))
	if err != nil {
		return "", errLessonsUnreadable
	}
	if len(data) > maxLessonsReply {
		return "", errLessonsTooLarge
	}
	return string(data), nil
}

func cleanText(text string) string {
	text = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp) {
			return ' '
		}
		return r
	}, text)
	return strings.Join(strings.Fields(text), " ")
}

func parseLessons(text string) ([]Lesson, []string, error) {
	if len(text) > maxLessonsReply {
		return nil, nil, errLessonsTooLarge
	}
	text = strings.TrimSpace(text)
	if strings.HasPrefix(text, "```") && strings.HasSuffix(text, "```") {
		if i := strings.IndexByte(text, '\n'); i >= 0 {
			text = strings.TrimSpace(text[i+1 : len(text)-3])
		}
	}
	if !strings.HasPrefix(text, "[") {
		return nil, nil, errLessonsFormat
	}
	decoder := json.NewDecoder(strings.NewReader(text))
	decoder.DisallowUnknownFields()
	var answer []*Lesson
	if err := decoder.Decode(&answer); err != nil {
		return nil, nil, errLessonsFormat
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, nil, errLessonsFormat
	}
	lessons := make([]Lesson, 0, min(len(answer), maxLessons))
	var dropped []string
	total := 0
	full := false
	for i, l := range answer {
		if l == nil {
			return nil, nil, errLessonsFormat
		}
		switch {
		case i >= maxLessons:
			dropped = append(dropped, "over count")
			continue
		case !slices.Contains(AllRoles(), l.Part):
			dropped = append(dropped, "unknown part")
			continue
		}
		l.Lesson, l.Evidence = cleanText(l.Lesson), cleanText(l.Evidence)
		if l.Lesson == "" || l.Evidence == "" {
			dropped = append(dropped, "empty field")
			continue
		}
		if utf8.RuneCountInString(l.Lesson) > maxLessonRunes || utf8.RuneCountInString(l.Evidence) > maxEvidenceRunes {
			dropped = append(dropped, "over length")
			continue
		}
		size := len(l.Part) + len(l.Lesson) + len(l.Evidence)
		if full || total+size > maxLessonsBytes {
			full = true
			dropped = append(dropped, "over total size")
			continue
		}
		total += size
		lessons = append(lessons, *l)
	}
	return lessons, dropped, nil
}

func lessonsSummary(proposed int, dropped []string) string {
	if len(dropped) == 0 {
		return ""
	}
	var counts []string
	for _, reason := range []string{"over count", "unknown part", "empty field", "over length", "over total size"} {
		n := 0
		for _, d := range dropped {
			if d == reason {
				n++
			}
		}
		if n > 0 {
			counts = append(counts, fmt.Sprintf("%d %s", n, reason))
		}
	}
	return fmt.Sprintf("%d proposed, %d dropped (%s)", proposed, len(dropped), strings.Join(counts, ", "))
}

func (e *Engine) proposeLessons(ctx context.Context, res Result) (lessons []Lesson, report LessonsReport) {
	defer func() {
		rec := Record{RunID: e.Config.RunID, Round: res.Rounds, Step: "lessons-proposed", Detail: fmt.Sprintf("%d proposed", report.Proposed)}
		if report.Reason != "" {
			rec.Detail = report.Reason
			if report.Proposed == 0 {
				rec.Error = report.Reason
			}
		}
		e.record(rec)
	}()
	if ctx.Err() != nil {
		report.Reason = "lessons step skipped: context cancelled"
		return
	}
	if e.gateRound == 0 {
		report.Reason = "the gate gave no verdict"
		return
	}
	limit := e.Config.LessonsTimeout
	if limit <= 0 {
		limit = DefaultLessonsTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	before, err := e.treeState()
	if err != nil {
		report.Reason = "lessons step failed: could not inspect the worktree"
		return
	}
	path := e.rel("lessons-reply.json")
	_, stepErr := e.step(ctx, Step{Name: "lessons", Round: res.Rounds, Chair: e.Config.Chairs.Gate, ReplyPath: path, Timeout: limit,
		Prompt: lessonsPrompt(e.Config.Task, res, e.lessonFiles(), path)})
	after, err := e.treeState()
	if err != nil {
		report.Reason = "lessons step failed: could not inspect the worktree"
		return
	}
	if before != after {
		report.Reason = "lessons step modified the worktree"
		e.record(Record{RunID: e.Config.RunID, Round: res.Rounds, Step: "lessons", TreeChanged: true, Error: report.Reason})
		e.progress("km: lessons step modified the worktree")
		return
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(stepErr, context.DeadlineExceeded) {
		report.Reason = fmt.Sprintf("lessons step failed: timed out after %s", limit)
		return
	}
	if stepErr != nil {
		if errors.Is(stepErr, errLessonsUnreadable) || errors.Is(stepErr, errLessonsTooLarge) {
			report.Reason = stepErr.Error()
		} else {
			report.Reason = "lessons step failed: " + cleanText(stepErr.Error())
			if utf8.RuneCountInString(report.Reason) > maxEvidenceRunes {
				report.Reason = string([]rune(report.Reason)[:maxEvidenceRunes])
			}
		}
		return
	}
	text, err := ReadLessonsReply(filepath.Join(e.Config.Worktree, path))
	if err != nil {
		report.Reason = err.Error()
		return
	}
	var dropped []string
	lessons, dropped, err = parseLessons(text)
	if err != nil {
		report.Reason = err.Error()
		return
	}
	report.Proposed = len(lessons)
	report.Reason = lessonsSummary(len(lessons), dropped)
	return
}

func (e *Engine) lessonFiles() []string {
	entries, _ := os.ReadDir(filepath.Join(e.Config.Worktree, RunDirRel(e.Config.RunID)))
	var files []string
	for _, entry := range entries {
		if !entry.Type().IsRegular() {
			continue
		}
		name := entry.Name()
		include := name == "plan.md" || name == "test-plan.md"
		for _, prefix := range []string{"implement-", "review-", "gate-", "test-", "verify-"} {
			if strings.HasPrefix(name, prefix) && ((prefix == "verify-" && strings.HasSuffix(name, ".txt")) || (prefix != "verify-" && strings.HasSuffix(name, ".md"))) {
				include = true
			}
		}
		if include {
			files = append(files, e.rel(name))
		}
	}
	return files
}
