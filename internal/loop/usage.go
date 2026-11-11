package loop

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

type Tokens struct {
	InputTokens      int64 `json:"input_tokens"`
	OutputTokens     int64 `json:"output_tokens"`
	CacheWriteTokens int64 `json:"cache_write_tokens"`
	CacheReadTokens  int64 `json:"cache_read_tokens"`
}

func (t Tokens) add(other Tokens) Tokens {
	return Tokens{t.InputTokens + other.InputTokens, t.OutputTokens + other.OutputTokens, t.CacheWriteTokens + other.CacheWriteTokens, t.CacheReadTokens + other.CacheReadTokens}
}

func (t Tokens) subtract(other Tokens) Tokens {
	return Tokens{t.InputTokens - other.InputTokens, t.OutputTokens - other.OutputTokens, t.CacheWriteTokens - other.CacheWriteTokens, t.CacheReadTokens - other.CacheReadTokens}
}

func (t Tokens) valid() bool {
	return t.InputTokens >= 0 && t.OutputTokens >= 0 && t.CacheWriteTokens >= 0 && t.CacheReadTokens >= 0
}

type Usage struct {
	Known bool `json:"known"`
	*Tokens
	Models       map[string]Tokens `json:"models,omitempty"`
	Reason       string            `json:"reason,omitempty"`
	UnknownSteps int               `json:"unknown_steps,omitempty"`
}

func unknownUsage(reason string) Usage { return Usage{Reason: reason} }

func (u *Usage) addModel(model string, tokens Tokens) {
	if model == "" {
		model = "unknown"
	}
	if u.Tokens == nil {
		u.Tokens = &Tokens{}
		u.Models = make(map[string]Tokens)
	}
	u.Known = true
	*u.Tokens = u.Tokens.add(tokens)
	u.Models[model] = u.Models[model].add(tokens)
}

func (u *Usage) add(step Usage) {
	if !step.Known {
		u.UnknownSteps++
		return
	}
	for model, tokens := range step.Models {
		u.addModel(model, tokens)
	}
}

type UsageMeter interface{ Open(step Step) func() Usage }

type LogMeter struct {
	ClaudeHome string
	CodexHome  string
	mu         sync.Mutex
	cwd        map[string]string
	windows    map[string]chan struct{}
}

func NewLogMeter() *LogMeter {
	home, _ := os.UserHomeDir()
	claude := os.Getenv("CLAUDE_CONFIG_DIR")
	if claude == "" {
		claude = filepath.Join(home, ".claude")
	}
	codex := os.Getenv("CODEX_HOME")
	if codex == "" {
		codex = filepath.Join(home, ".codex")
	}
	return &LogMeter{ClaudeHome: claude, CodexHome: codex}
}

const maxUsageRange = 64 << 20
const maxUsageLine = 4 << 20

var claudeProjectName = regexp.MustCompile(`[^a-zA-Z0-9]`)

func (m *LogMeter) Open(step Step) (close func() Usage) {
	close = func() Usage { return unknownUsage("unreadable-log") }
	var release func()
	handedOff := false
	defer func() {
		if recover() != nil {
			close = func() Usage { return unknownUsage("unreadable-log") }
		}
		if !handedOff && release != nil {
			release()
		}
	}()
	if step.Chair.Provider != "claude_code" && step.Chair.Provider != "codex" {
		return func() Usage { return unknownUsage("unsupported-provider") }
	}
	release = m.chairWindow(step)
	before, err := m.logs(step)
	if err != nil {
		return close
	}
	handedOff = true
	return func() (usage Usage) {
		if release != nil {
			defer release()
		}
		usage = unknownUsage("unreadable-log")
		defer func() {
			if recover() != nil {
				usage = unknownUsage("unreadable-log")
			}
		}()
		after, err := m.logs(step)
		if err != nil {
			return usage
		}
		for path := range before {
			if _, ok := after[path]; !ok {
				return usage
			}
		}
		type growth struct {
			path  string
			start int64
			data  []byte
		}
		var grown []growth
		for path, size := range after {
			start := before[path]
			if size < start {
				return usage
			}
			if size == start {
				continue
			}
			data, err := readUsageRange(path, start, size)
			if err != nil {
				return usage
			}
			grown = append(grown, growth{path, start, data})
		}
		if len(grown) == 0 {
			return unknownUsage("no-log")
		}
		chosen := grown[0]
		if len(grown) > 1 {
			matches := 0
			for _, g := range grown {
				if step.ReplyPath != "" && bytes.Contains(g.data, []byte(step.ReplyPath)) {
					chosen = g
					matches++
				}
			}
			if matches != 1 {
				return unknownUsage("ambiguous-log")
			}
		}
		if step.Chair.Provider == "claude_code" {
			return claudeUsage(chosen.data)
		}
		head, err := readUsageHead(chosen.path, chosen.start)
		if err != nil {
			return usage
		}
		return codexUsage(chosen.data, head)
	}
}

func (m *LogMeter) chairWindow(step Step) func() {
	key := "chair:" + step.Chair.Address
	if step.Chair.Address == "" {
		if step.ReuseTerminal == "" {
			return nil
		}
		key = "terminal:" + step.ReuseTerminal
	}
	key = step.Dir + "\x00" + key
	m.mu.Lock()
	if m.windows == nil {
		m.windows = make(map[string]chan struct{})
	}
	slot := m.windows[key]
	if slot == nil {
		slot = make(chan struct{}, 1)
		m.windows[key] = slot
	}
	m.mu.Unlock()
	slot <- struct{}{}
	var once sync.Once
	return func() { once.Do(func() { <-slot }) }
}

func readUsageRange(path string, start, end int64) ([]byte, error) {
	if end-start > maxUsageRange {
		return nil, errors.New("usage range exceeds limit")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data := make([]byte, end-start)
	if _, err = io.ReadFull(io.NewSectionReader(f, start, end-start), data); err != nil {
		return nil, err
	}
	if start > 0 {
		var previous [1]byte
		if _, err = f.ReadAt(previous[:], start-1); err != nil {
			return nil, err
		}
		if previous[0] != '\n' {
			i := bytes.IndexByte(data, '\n')
			if i < 0 {
				return nil, nil
			}
			data = data[i+1:]
		}
	}
	if i := bytes.LastIndexByte(data, '\n'); i >= 0 {
		return data[:i+1], nil
	}
	return nil, nil
}

func readUsageHead(path string, end int64) ([]byte, error) {
	if end > 64<<10 {
		end = 64 << 10
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data := make([]byte, end)
	_, err = io.ReadFull(f, data)
	if err != nil {
		return nil, err
	}
	if i := bytes.LastIndexByte(data, '\n'); i >= 0 {
		return data[:i+1], nil
	}
	return nil, nil
}

func (m *LogMeter) logs(step Step) (map[string]int64, error) {
	paths := make(map[string]int64)
	var dirs []string
	pattern := "*.jsonl"
	if step.Chair.Provider == "claude_code" {
		dirs = []string{filepath.Join(m.ClaudeHome, "projects", claudeProjectName.ReplaceAllString(step.Dir, "-"))}
	} else {
		now := time.Now()
		dirs = []string{filepath.Join(m.CodexHome, "sessions", now.Format("2006/01/02")), filepath.Join(m.CodexHome, "sessions", now.AddDate(0, 0, -1).Format("2006/01/02"))}
		pattern = "rollout-*.jsonl"
	}
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			match, _ := filepath.Match(pattern, entry.Name())
			if !match {
				continue
			}
			path := filepath.Join(dir, entry.Name())
			if entry.Type()&os.ModeSymlink != 0 {
				return nil, errors.New("usage log is a symlink")
			}
			info, err := os.Stat(path)
			if err != nil {
				return nil, err
			}
			if !info.Mode().IsRegular() {
				return nil, errors.New("usage log is not a regular file")
			}
			if step.Chair.Provider == "codex" {
				cwd, err := m.codexCwd(path)
				if err != nil {
					return nil, err
				}
				if filepath.Clean(cwd) != filepath.Clean(step.Dir) {
					continue
				}
			}
			paths[path] = info.Size()
		}
	}
	return paths, nil
}

func (m *LogMeter) codexCwd(path string) (string, error) {
	m.mu.Lock()
	cwd, ok := m.cwd[path]
	m.mu.Unlock()
	if ok {
		return cwd, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(nil, maxUsageLine)
	if !scanner.Scan() {
		return "", scanner.Err()
	}
	var entry struct {
		Type    string `json:"type"`
		Payload struct {
			Cwd string `json:"cwd"`
		} `json:"payload"`
	}
	if json.Unmarshal(scanner.Bytes(), &entry) != nil || entry.Type != "session_meta" {
		return "", nil
	}
	cwd = entry.Payload.Cwd
	if cwd != "" {
		m.mu.Lock()
		if m.cwd == nil {
			m.cwd = make(map[string]string)
		}
		m.cwd[path] = cwd
		m.mu.Unlock()
	}
	return cwd, nil
}

func usageLines(data []byte, read func([]byte) bool) bool {
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(nil, maxUsageLine)
	for scanner.Scan() {
		if len(strings.TrimSpace(string(scanner.Bytes()))) == 0 {
			continue
		}
		if !read(scanner.Bytes()) {
			return false
		}
	}
	return scanner.Err() == nil
}
