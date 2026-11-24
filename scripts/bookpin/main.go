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
	kitLines := strings.SplitAfter(data, "\n")
	result := map[string]int{}
	counts := map[string]int{}
	active := ""
	section := false
	sections := 0
	for i, line := range kitLines {
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
			for _, name := range names {
				if bare == "  "+name+":" {
					active = name
					counts[name]++
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
	for _, name := range names {
		if counts[name] != 1 || counts[name+" default"] != 1 {
			return nil, fmt.Errorf("expected one %s argument and default", name)
		}
	}
	return result, nil
}

func readKit(path string, names []string) (string, map[string]int, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return "", nil, err
	}
	indexes, err := defaults(string(content), names)
	return string(content), indexes, err
}

func value(line string) string { return defaultPattern.FindStringSubmatch(line)[1] }

func pin(path string) (string, error) {
	data, indexes, err := readKit(path, []string{"km"})
	if err != nil {
		return "", err
	}
	tag := value(strings.SplitAfter(data, "\n")[indexes["km"]])
	if !tagPattern.MatchString(tag) {
		return "", fmt.Errorf("invalid pinned tag %q", tag)
	}
	return tag, nil
}

func numberCmp(left, right string) int {
	left = strings.TrimLeft(left, "0")
	right = strings.TrimLeft(right, "0")
	if len(left) < len(right) {
		return -1
	}
	if len(left) > len(right) {
		return 1
	}
	return strings.Compare(left, right)
}

func compare(left, right string) int {
	leftParts, rightParts := tagPattern.FindStringSubmatch(left), tagPattern.FindStringSubmatch(right)
	for i := 1; i <= 3; i++ {
		if order := numberCmp(leftParts[i], rightParts[i]); order != 0 {
			return order
		}
	}
	if leftParts[4] == "" && rightParts[4] != "" {
		return 1
	}
	if leftParts[4] != "" && rightParts[4] == "" {
		return -1
	}
	if leftParts[4] == rightParts[4] {
		return 0
	}
	leftIdentifiers, rightIdentifiers := strings.Split(leftParts[4], "."), strings.Split(rightParts[4], ".")
	for i := 0; i < len(leftIdentifiers) && i < len(rightIdentifiers); i++ {
		leftNumeric, rightNumeric := digitsPattern.MatchString(leftIdentifiers[i]), digitsPattern.MatchString(rightIdentifiers[i])
		if leftNumeric && !rightNumeric {
			return -1
		}
		if !leftNumeric && rightNumeric {
			return 1
		}
		order := strings.Compare(leftIdentifiers[i], rightIdentifiers[i])
		if leftNumeric && rightNumeric {
			order = numberCmp(leftIdentifiers[i], rightIdentifiers[i])
		}
		if order != 0 {
			return order
		}
	}
	if len(leftIdentifiers) < len(rightIdentifiers) {
		return -1
	}
	if len(leftIdentifiers) > len(rightIdentifiers) {
		return 1
	}
	return 0
}

func lines(input io.Reader, visit func(string)) error {
	scanner := bufio.NewScanner(input)
	for scanner.Scan() {
		visit(strings.TrimSpace(scanner.Text()))
	}
	return scanner.Err()
}

func set(path, tag, sumsPath string) (string, error) {
	if !tagPattern.MatchString(tag) {
		return "", fmt.Errorf("invalid tag %q", tag)
	}
	sums, err := os.ReadFile(sumsPath)
	if err != nil {
		return "", err
	}
	names := []string{"km", "kmSha256Arm64", "kmSha256Amd64"}
	hashes := map[string]string{}
	targets := map[string]string{"km_" + tag + "_linux_arm64.tar.gz": names[1], "km_" + tag + "_linux_amd64.tar.gz": names[2]}
	for _, line := range strings.Split(string(sums), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		name := strings.TrimPrefix(fields[1], "*")
		key, ok := targets[name]
		if !ok {
			continue
		}
		if !hashPattern.MatchString(fields[0]) {
			return "", fmt.Errorf("invalid checksum for %s", name)
		}
		if old, ok := hashes[key]; ok && old != fields[0] {
			return "", fmt.Errorf("conflicting checksums for %s", name)
		}
		hashes[key] = fields[0]
	}
	for _, name := range names[1:] {
		if hashes[name] == "" {
			return "", fmt.Errorf("missing checksum for %s", name)
		}
	}
	hashes["km"] = tag
	data, indexes, err := readKit(path, names)
	if err != nil {
		return "", err
	}
	kitLines := strings.SplitAfter(data, "\n")
	current := value(kitLines[indexes["km"]])
	if !tagPattern.MatchString(current) {
		return "", fmt.Errorf("invalid pinned tag %q", current)
	}
	order := compare(tag, current)
	if order < 0 {
		return fmt.Sprintf("older: %s is below the pinned %s", tag, current), nil
	}
	for _, name := range names {
		lineIndex := indexes[name]
		valueSpan := defaultPattern.FindStringSubmatchIndex(kitLines[lineIndex])
		kitLines[lineIndex] = kitLines[lineIndex][:valueSpan[2]] + hashes[name] + kitLines[lineIndex][valueSpan[3]:]
	}
	updated := strings.Join(kitLines, "")
	if updated == data {
		return "unchanged", nil
	}
	if order == 0 {
		return "", fmt.Errorf("release assets for %s differ from the pinned checksums", tag)
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if err = os.WriteFile(path, []byte(updated), info.Mode().Perm()); err != nil {
		return "", err
	}
	return "set " + tag, nil
}

func run(args []string, in io.Reader, out, errout io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(errout, "bookpin: missing command")
		return 2
	}
	want := map[string]int{"set": 4, "pinned": 2, "check": 2, "next-book-tag": 1}
	wantArgs, ok := want[args[0]]
	if !ok || len(args) != wantArgs {
		fmt.Fprintln(errout, "bookpin: invalid usage")
		return 2
	}
	var result string
	var err error
	switch args[0] {
	case "set":
		result, err = set(args[1], args[2], args[3])
	case "pinned":
		result, err = pin(args[1])
	case "check":
		var pinned string
		pinned, err = pin(args[1])
		if err == nil {
			latest := ""
			err = lines(in, func(line string) {
				if tagPattern.MatchString(line) && (latest == "" || compare(line, latest) > 0) {
					latest = line
				}
			})
			if err == nil && latest != "" && compare(pinned, latest) < 0 {
				err = fmt.Errorf("km pin %s is behind the latest release %s", pinned, latest)
			}
			result = "ok"
		}
	case "next-book-tag":
		latest := ""
		err = lines(in, func(line string) {
			match := bookPattern.FindStringSubmatch(line)
			if match != nil && (latest == "" || compare(match[1], latest) > 0) {
				latest = match[1]
			}
		})
		if err == nil {
			if latest == "" {
				err = fmt.Errorf("no book tags found")
			} else {
				match := tagPattern.FindStringSubmatch(latest)
				patch, _ := new(big.Int).SetString(match[3], 10)
				patch.Add(patch, big.NewInt(1))
				result = "book/v" + match[1] + "." + match[2] + "." + patch.String()
			}
		}
	}
	if err != nil {
		fmt.Fprintln(errout, "bookpin:", err)
		return 1
	}
	fmt.Fprintln(out, result)
	return 0
}
