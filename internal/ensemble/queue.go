package ensemble

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

type Closure struct {
	Cue      string    `json:"cue"`
	Terminal string    `json:"terminal"`
	Note     string    `json:"note,omitempty"`
	At       time.Time `json:"at"`
}

func Close(queue, cue, terminal, note string) error {
	if err := os.MkdirAll(filepath.Dir(queue), 0o755); err != nil {
		return err
	}
	line, err := json.Marshal(Closure{Cue: cue, Terminal: terminal, Note: note, At: time.Now().UTC()})
	if err != nil {
		return err
	}
	f, err := os.OpenFile(queue, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func Closures(queue string) (map[string]Closure, error) {
	closed := map[string]Closure{}
	f, err := os.Open(queue)
	if os.IsNotExist(err) {
		return closed, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var c Closure
		if json.Unmarshal(scanner.Bytes(), &c) != nil || c.Cue == "" {
			continue
		}
		if _, seen := closed[c.Cue]; !seen {
			closed[c.Cue] = c
		}
	}
	return closed, scanner.Err()
}
