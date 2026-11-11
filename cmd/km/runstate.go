package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/jystringfellow/kapellmeister/internal/loop"
)

type runState struct {
	RunID        string            `json:"run_id"`
	Command      string            `json:"command"`
	Repository   string            `json:"repository"`
	Ensemble     string            `json:"ensemble"`
	Started      time.Time         `json:"started"`
	Updated      time.Time         `json:"updated"`
	Round        int               `json:"round"`
	MaxRounds    int               `json:"max_rounds"`
	Playing      []loop.ActiveStep `json:"playing"`
	PID          int               `json:"pid"`
	Host         string            `json:"host"`
	ProcessStart string            `json:"process_start"`
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
