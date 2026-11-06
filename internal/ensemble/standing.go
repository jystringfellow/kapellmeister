package ensemble

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/jystringfellow/kapellmeister/internal/cao"
	"github.com/jystringfellow/kapellmeister/internal/loop"
)

type StandingChair struct {
	Name       string     `json:"name"`
	Binding    loop.Chair `json:"binding"`
	Address    string     `json:"address"`
	Section    string     `json:"section"`
	Part       string     `json:"part"`
	Chair      string     `json:"chair"`
	TerminalID string     `json:"terminal_id"`
	Briefed    bool       `json:"briefed"`
}

type Standing struct {
	GateWhen string          `json:"gate_when,omitempty"`
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

func (s Standing) RebuildChairs() (loop.Chairs, error) {
	for _, c := range s.Chairs {
		if c.Binding.Provider == "" {
			return loop.Chairs{}, fmt.Errorf("session %s was rehearsed by an older km (no recorded binding); take it down and rehearse again", s.Session)
		}
	}
	chairs, err := ChairsOf(s.Members())
	if err != nil {
		return loop.Chairs{}, fmt.Errorf("session %s: %w", s.Session, err)
	}
	return chairs, nil
}

func (s Standing) Members() []Member {
	members := make([]Member, 0, len(s.Chairs))
	for _, c := range s.Chairs {
		binding := c.Binding
		binding.Address = c.Address
		members = append(members, Member{Section: c.Section, Part: c.Part, Name: c.Name, Chair: binding})
	}
	return members
}

func MissingOccupants(ctx context.Context, client *cao.Client, s Standing) ([]string, error) {
	var missing []string
	for _, c := range s.Chairs {
		if _, err := client.Terminal(ctx, c.TerminalID); err != nil {
			var httpErr *cao.HTTPError
			if !errors.As(err, &httpErr) || httpErr.Status != http.StatusNotFound {
				return nil, err
			}
			missing = append(missing, c.Address)
		}
	}
	return missing, nil
}

func (s Standing) Unbriefed() []string {
	var addresses []string
	for _, c := range s.Chairs {
		if !c.Briefed {
			addresses = append(addresses, c.Address)
		}
	}
	return addresses
}
