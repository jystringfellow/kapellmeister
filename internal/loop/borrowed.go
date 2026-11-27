package loop

import (
	"crypto/sha256"
	"fmt"
	"strconv"
	"strings"
)

func (r scoreReader) borrowed(score, name, path string) (string, ScoreFile, error) {
	file := ScoreFile{Kind: "part", Name: name, Source: "repository", Path: path, From: path}
	fail := func(format string, args ...any) (string, ScoreFile, error) {
		return "", file, scoreError(score, "part "+name, "from %q: %s", path, fmt.Sprintf(format, args...))
	}
	if !r.src.AllowRepository {
		return fail("[run] repository_scores is false")
	}
	if r.base == "" {
		return fail("no base commit")
	}
	entries, err := r.engine.gitOutput("--literal-pathspecs", "ls-tree", "-z", r.base, "--", path)
	if err != nil {
		return fail("%v", err)
	}
	var fields []string
	for _, entry := range strings.Split(entries, "\x00") {
		meta, p, ok := strings.Cut(entry, "\t")
		if ok && p == path {
			fields = strings.Fields(meta)
			break
		}
	}
	if len(fields) != 3 {
		return fail("does not exist at the base commit")
	}
	if fields[0] == "120000" {
		return fail("symbolic link")
	}
	if fields[0] != "100644" && fields[0] != "100755" {
		return fail("not a regular file")
	}
	sizeText, err := r.engine.gitOutput("cat-file", "-s", fields[2])
	if err != nil {
		return fail("%v", err)
	}
	size, err := strconv.ParseInt(strings.TrimSpace(sizeText), 10, 64)
	if err != nil {
		return fail("%v", err)
	}
	if size > partSizeLimit {
		return fail("file is %d bytes, over the %d-byte limit", size, partSizeLimit)
	}
	blob := r.engine.readTextBlob(fields[0], fields[2])
	if blob.reason != "" {
		return fail("%s", blob.reason)
	}
	file.SHA256 = fmt.Sprintf("%x", sha256.Sum256([]byte(blob.text)))
	body, note, err := stripBorrowedFrontMatter(blob.text)
	if err != nil {
		return fail("%v", err)
	}
	file.FrontMatter = note
	return body, file, nil
}

func stripBorrowedFrontMatter(text string) (string, string, error) {
	lines := strings.Split(text, "\n")
	note := ""
	first := 0
	for first < len(lines)-1 && strings.TrimSpace(lines[first]) == "" {
		first++
	}
	if strings.TrimSpace(lines[first]) == "---" && (first > 0 || lines[0] != "---") {
		return "", "", inexactDelimiter(first)
	}
	if lines[0] == "---" {
		end := -1
		for i := 1; i < len(lines); i++ {
			if lines[i] == "---" {
				end = i
				break
			}
			if strings.TrimSpace(lines[i]) == "---" {
				return "", "", inexactDelimiter(i)
			}
		}
		if end < 0 {
			return "", "", fmt.Errorf("unclosed front matter")
		}
		var keys []string
		for _, key := range []string{"tools", "disallowedTools", "model"} {
			for _, line := range lines[1:end] {
				if strings.HasPrefix(line, key+":") {
					keys = append(keys, key)
					break
				}
			}
		}
		note = "front matter ignored"
		if len(keys) == 1 {
			note += ", including " + keys[0]
		}
		if len(keys) == 2 {
			note += ", including " + keys[0] + " and " + keys[1]
		}
		if len(keys) == 3 {
			note += ", including " + keys[0] + ", " + keys[1] + " and " + keys[2]
		}
		lines = lines[end+1:]
	}
	body := strings.Trim(strings.Join(lines, "\n"), "\n")
	if strings.TrimSpace(body) == "" {
		return "", "", fmt.Errorf("part text is empty")
	}
	return body, note, nil
}

func inexactDelimiter(i int) error {
	return fmt.Errorf("line %d: a front matter delimiter must be exactly --- on the first line", i+1)
}
