package main

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/jystringfellow/kapellmeister/internal/ensemble"
	"github.com/jystringfellow/kapellmeister/internal/loop"
)

type runState struct {
	SeatsChanged   []ensemble.SeatChange    `json:"seats_changed,omitempty"`
	ProvidersLost  []ensemble.ProvidersLost `json:"providers_lost,omitempty"`
	EnsembleSource string                   `json:"ensemble_source,omitempty"`
	Why            string                   `json:"why,omitempty"`
	CAO            string                   `json:"cao,omitempty"`
	Seating        *statusSeating           `json:"seating,omitempty"`
	Score          *loop.ScoreOutline       `json:"score,omitempty"`
	RunID          string                   `json:"run_id"`
	Command        string                   `json:"command"`
	Repository     string                   `json:"repository"`
	Ensemble       string                   `json:"ensemble"`
	Started        time.Time                `json:"started"`
	Updated        time.Time                `json:"updated"`
	Round          int                      `json:"round"`
	MaxRounds      int                      `json:"max_rounds"`
	Playing        []loop.ActiveStep        `json:"playing"`
	PID            int                      `json:"pid"`
	Host           string                   `json:"host"`
	ProcessStart   string                   `json:"process_start"`
}

func statusServerAddress(address string) string {
	parsed, err := url.Parse(address)
	if err != nil {
		return ""
	}
	parsed.User = nil
	return parsed.String()
}

func writeRunState(dir string, state runState) error {
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(dir, ".current-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err := file.Write(append(data, '\n')); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), filepath.Join(dir, "current.json"))
}

func initialRunState(id, command, worktree, ensemble string, rounds int, start time.Time, scores ...loop.Score) runState {
	if len(scores) > 0 && !scores[0].Loops() {
		rounds = 0
	}
	host, _ := os.Hostname()
	token, _ := processStart(os.Getpid())
	origin, _ := gitOutput(worktree, "config", "--get", "remote.origin.url")
	return runState{RunID: id, Command: command, Repository: repositoryOf(origin), Ensemble: ensemble, Started: start.UTC(), Updated: start.UTC(), MaxRounds: rounds, PID: os.Getpid(), Host: host, ProcessStart: token, Playing: []loop.ActiveStep{}}
}

func writeAtomic(path string, data []byte, mode os.FileMode) error {
	file, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if err := file.Chmod(mode); err != nil {
		file.Close()
		return err
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}
