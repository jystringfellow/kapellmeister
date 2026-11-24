package main

import (
	"bufio"
	"fmt"
	"io"
	"math/big"
	"os"
	"regexp"
	"strings"
)

var tagPattern = regexp.MustCompile(`^v([0-9]+)\.([0-9]+)\.([0-9]+)(?:-([0-9A-Za-z.-]+))?$`)
var bookPattern = regexp.MustCompile(`^book/(v[0-9]+\.[0-9]+\.[0-9]+)$`)
var defaultPattern = regexp.MustCompile(`^    default: "([^"]*)"[ \t]*\r?\n?$`)
var hashPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
var digitsPattern = regexp.MustCompile(`^[0-9]+$`)

func main() { os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr)) }

func defaults(data string, names []string) (map[string]int, error) {
	lines := strings.SplitAfter(data, "\n")
	result := map[string]int{}
	counts := map[string]int{}
	active := ""
	section := false
	sections := 0
	for i, line := range lines {
		bare := strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
		if bare == "args:" {
			section = true
			sections++
			active = ""
			continue
		}
		if !section {
			continue
		}
		if strings.TrimSpace(bare) == "" {
			continue
		}
		if !strings.HasPrefix(bare, " ") {
			section = false
			active = ""
			continue
		}
		if !strings.HasPrefix(bare, "   ") {
			active = ""
			for _, n := range names {
				if bare == "  "+n+":" {
					active = n
					counts[n]++
				}
			}
			continue
		}
		if active != "" && defaultPattern.MatchString(line) {
			counts[active+" default"]++
			result[active] = i
		}
	}
	if sections != 1 {
		return nil, fmt.Errorf("expected one args section")
	}
	for _, n := range names {
		if counts[n] != 1 || counts[n+" default"] != 1 {
			return nil, fmt.Errorf("expected one %s argument and default", n)
		}
	}
	return result, nil
}
func readKit(path string, names []string) (string, map[string]int, error) {
	b, e := os.ReadFile(path)
	if e != nil {
		return "", nil, e
	}
	idx, e := defaults(string(b), names)
	return string(b), idx, e
}
func value(line string) string { return defaultPattern.FindStringSubmatch(line)[1] }
func pin(path string) (string, error) {
	s, idx, e := readKit(path, []string{"km"})
	if e != nil {
		return "", e
	}
	v := value(strings.SplitAfter(s, "\n")[idx["km"]])
	if !tagPattern.MatchString(v) {
		return "", fmt.Errorf("invalid pinned tag %q", v)
	}
	return v, nil
}
func numberCmp(a, b string) int {
	a = strings.TrimLeft(a, "0")
	b = strings.TrimLeft(b, "0")
	if len(a) < len(b) {
		return -1
	}
	if len(a) > len(b) {
		return 1
	}
	return strings.Compare(a, b)
}
func compare(a, b string) int {
	x, y := tagPattern.FindStringSubmatch(a), tagPattern.FindStringSubmatch(b)
	for i := 1; i <= 3; i++ {
		if c := numberCmp(x[i], y[i]); c != 0 {
			return c
		}
	}
	if x[4] == "" && y[4] != "" {
		return 1
	}
	if x[4] != "" && y[4] == "" {
		return -1
	}
	if x[4] == y[4] {
		return 0
	}
	p, q := strings.Split(x[4], "."), strings.Split(y[4], ".")
	for i := 0; i < len(p) && i < len(q); i++ {
		pn, qn := digitsPattern.MatchString(p[i]), digitsPattern.MatchString(q[i])
		if pn && !qn {
			return -1
		}
		if !pn && qn {
			return 1
		}
		c := strings.Compare(p[i], q[i])
		if pn && qn {
			c = numberCmp(p[i], q[i])
		}
		if c != 0 {
			return c
		}
	}
	if len(p) < len(q) {
		return -1
	}
	if len(p) > len(q) {
		return 1
	}
	return 0
}
func lines(in io.Reader, fn func(string)) error {
	s := bufio.NewScanner(in)
	for s.Scan() {
		fn(strings.TrimSpace(s.Text()))
	}
	return s.Err()
}
func set(path, tag, sumsPath string) (string, error) {
	if !tagPattern.MatchString(tag) {
		return "", fmt.Errorf("invalid tag %q", tag)
	}
	b, e := os.ReadFile(sumsPath)
	if e != nil {
		return "", e
	}
	names := []string{"km", "kmSha256Arm64", "kmSha256Amd64"}
	hashes := map[string]string{}
	targets := map[string]string{"km_" + tag + "_linux_arm64.tar.gz": names[1], "km_" + tag + "_linux_amd64.tar.gz": names[2]}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) != 2 {
			continue
		}
		name := strings.TrimPrefix(f[1], "*")
		key, ok := targets[name]
		if !ok {
			continue
		}
		if !hashPattern.MatchString(f[0]) {
			return "", fmt.Errorf("invalid checksum for %s", name)
		}
		if old, ok := hashes[key]; ok && old != f[0] {
			return "", fmt.Errorf("conflicting checksums for %s", name)
		}
		hashes[key] = f[0]
	}
	for _, n := range names[1:] {
		if hashes[n] == "" {
			return "", fmt.Errorf("missing checksum for %s", n)
		}
	}
	hashes["km"] = tag
	data, idx, e := readKit(path, names)
	if e != nil {
		return "", e
	}
	parts := strings.SplitAfter(data, "\n")
	for _, n := range names {
		i := idx[n]
		loc := defaultPattern.FindStringSubmatchIndex(parts[i])
		parts[i] = parts[i][:loc[2]] + hashes[n] + parts[i][loc[3]:]
	}
	updated := strings.Join(parts, "")
	if updated == data {
		return "unchanged", nil
	}
	info, e := os.Stat(path)
	if e != nil {
		return "", e
	}
	if e = os.WriteFile(path, []byte(updated), info.Mode().Perm()); e != nil {
		return "", e
	}
	return "set " + tag, nil
}
func run(args []string, in io.Reader, out, errout io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(errout, "bookpin: missing command")
		return 2
	}
	want := map[string]int{"set": 4, "pinned": 2, "check": 2, "next-book-tag": 1}
	n, ok := want[args[0]]
	if !ok || len(args) != n {
		fmt.Fprintln(errout, "bookpin: invalid usage")
		return 2
	}
	var result string
	var e error
	switch args[0] {
	case "set":
		result, e = set(args[1], args[2], args[3])
	case "pinned":
		result, e = pin(args[1])
	case "check":
		var v string
		v, e = pin(args[1])
		if e == nil {
			latest := ""
			e = lines(in, func(s string) {
				if tagPattern.MatchString(s) && (latest == "" || compare(s, latest) > 0) {
					latest = s
				}
			})
			if e == nil && latest != "" && compare(v, latest) < 0 {
				e = fmt.Errorf("km pin %s is behind the latest release %s", v, latest)
			}
			result = "ok"
		}
	case "next-book-tag":
		latest := ""
		e = lines(in, func(s string) {
			m := bookPattern.FindStringSubmatch(s)
			if m != nil && (latest == "" || compare(m[1], latest) > 0) {
				latest = m[1]
			}
		})
		if e == nil {
			if latest == "" {
				e = fmt.Errorf("no book tags found")
			} else {
				m := tagPattern.FindStringSubmatch(latest)
				patch, _ := new(big.Int).SetString(m[3], 10)
				patch.Add(patch, big.NewInt(1))
				result = "book/v" + m[1] + "." + m[2] + "." + patch.String()
			}
		}
	}
	if e != nil {
		fmt.Fprintln(errout, "bookpin:", e)
		return 1
	}
	fmt.Fprintln(out, result)
	return 0
}
