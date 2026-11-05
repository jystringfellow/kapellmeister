package ensemble

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

type StandingChair struct {
	Address    string `json:"address"`
	Section    string `json:"section"`
	Part       string `json:"part"`
	Chair      string `json:"chair"`
	TerminalID string `json:"terminal_id"`
}

type Standing struct {
	Ensemble string          `json:"ensemble"`
	Session  string          `json:"session"`
	Worktree string          `json:"worktree"`
	Queue    string          `json:"queue"`
	TaskFile string          `json:"task_file,omitempty"`
	Task     string          `json:"task,omitempty"`
	Chairs   []StandingChair `json:"chairs"`
}

func (s Standing) Save(dir string) error {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, s.Session+".json"), append(data, '\n'), 0644)
}

func ListStanding(dir string) ([]Standing, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var records []Standing
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		var s Standing
		if err := json.Unmarshal(data, &s); err != nil {
			return nil, fmt.Errorf("reading %s: %w", path, err)
		}
		records = append(records, s)
	}
	slices.SortFunc(records, func(a, b Standing) int { return cmp.Compare(a.Session, b.Session) })
	return records, nil
}

func RemoveStanding(dir, session string) error {
	err := os.Remove(filepath.Join(dir, session+".json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
