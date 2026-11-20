package loop

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestAnActiveStepRecordsItsRoundAndReusedTerminal(t *testing.T) {
	dir := t.TempDir()
	var active map[string]any
	e := Engine{Config: Config{Worktree: dir}, Stepper: statusRecordStepper{}, Playing: func(_ int, steps []ActiveStep) {
		if len(steps) > 0 {
			b, _ := json.Marshal(steps[0])
			json.Unmarshal(b, &active)
		}
	}}
	_, err := e.step(context.Background(), Step{Name: "draft", Round: 2, ReuseTerminal: "kept", KeepTerminal: true, ReplyPath: "reply", Chair: Chair{Provider: "p"}})
	if err != nil {
		t.Fatal(err)
	}
	if active["round"] != float64(2) || active["terminal"] != "kept" {
		t.Fatal(active)
	}
}

type statusRecordStepper struct{}

func (statusRecordStepper) Run(_ context.Context, s Step) (StepResult, error) {
	err := os.WriteFile(filepath.Join(s.Dir, s.ReplyPath), []byte("reply"), 0600)
	return StepResult{TerminalID: "kept"}, err
}
func (statusRecordStepper) Release(context.Context, string) error { return nil }

func TestAStepRecordsWhetherItsTerminalRemainsSeated(t *testing.T) {
	for _, keep := range []bool{false, true} {
		dir := t.TempDir()
		var records bytes.Buffer
		e := Engine{Config: Config{Worktree: dir}, Stepper: statusRecordStepper{}, Records: &records}
		_, err := e.step(context.Background(), Step{Name: "draft", KeepTerminal: keep, ReplyPath: "reply", Chair: Chair{Provider: "p"}})
		if err != nil {
			t.Fatal(err)
		}
		var record map[string]any
		if err := json.Unmarshal(records.Bytes(), &record); err != nil {
			t.Fatal(err)
		}
		if kept, _ := record["kept"].(bool); kept != keep {
			t.Fatal(record)
		}
	}
}

func TestABlockedRecordNamesThePartOfItsStep(t *testing.T) {
	var records bytes.Buffer
	e := Engine{Records: &records}
	e.record(Record{Step: "blocked", BlockedStep: "classify-review", ChairName: "subject"})
	var record map[string]any
	if err := json.Unmarshal(records.Bytes(), &record); err != nil {
		t.Fatal(err)
	}
	if record["part"] != DefaultScore().StepPart("classify-review") {
		t.Fatal(record)
	}
}
