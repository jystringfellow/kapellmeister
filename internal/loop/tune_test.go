package loop

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestTuneOrdersWritingCheckingProofAndReviewAndCarriesFeedback(t *testing.T) {
	eng, fake, records := newEngine(t, func(context.Context, string) (VerifyResult, error) {
		t.Fatal("baseline ran")
		return VerifyResult{}, nil
	})
	eng.Meter = NewLogMeter()
	patchDir := t.TempDir()
	for _, name := range []string{"source.patch", "test.patch"} {
		if err := os.WriteFile(filepath.Join(patchDir, name), []byte("patch"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	reply, _ := json.Marshal(TuneReply{Command: "tests", Patches: []TunePatch{{File: "source.patch", Kind: "source"}, {File: "test.patch", Kind: "test"}}})
	var order []string
	fake.actions["implement"] = func(s Step) {
		order = append(order, "write")
		if s.Round == 2 && !strings.Contains(s.Prompt, "proof-1.txt") {
			t.Error(s.Prompt)
		}
		if s.Round > 1 && s.ReuseTerminal == "" {
			t.Error("writer was not reused")
		}
		fake.write(s.ReplyPath, string(reply))
	}
	fake.actions["review"] = func(s Step) { order = append(order, "review"); fake.write(s.ReplyPath, "APPROVE") }
	playingProof := false
	eng.Playing = func(_ int, steps []ActiveStep) {
		for _, s := range steps {
			if s.Step == "prove" {
				playingProof = true
			}
		}
	}
	hooks := TuneHooks{
		PatchDir: patchDir,
		Changed:  func() ([]string, error) { order = append(order, "changed"); return nil, nil },
		Snapshot: func() (string, error) { return "same", nil },
		Prove: func(_ context.Context, _ TuneReply, round int) (bool, string, error) {
			order = append(order, "prove")
			return round == 2, "proof-1.txt", nil
		},
		Publish: func() error { order = append(order, "publish"); return nil },
	}
	res, err := eng.Tune(t.Context(), hooks)
	if err != nil || res.Outcome != Approved || res.Rounds != 2 {
		t.Fatal(res, err)
	}
	want := []string{"write", "changed", "prove", "write", "changed", "prove", "review", "publish"}
	if !reflect.DeepEqual(order, want) {
		t.Fatal(order)
	}
	if !playingProof || !strings.Contains(records.String(), `"step":"prove"`) || res.Usage == nil || res.Usage.UnknownSteps != 3 {
		t.Fatal(records.String(), res.Usage)
	}
}

func TestTuneRejectsPatchesOutsideTheRecordDirectoryAndNeedsBothKinds(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.patch")
	if err := os.WriteFile(outside, []byte("patch"), 0644); err != nil {
		t.Fatal(err)
	}
	inside := filepath.Join(root, "inside.patch")
	if err := os.WriteFile(inside, []byte("patch"), 0644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link.patch")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	for _, patches := range [][]TunePatch{
		{{outside, "source"}, {inside, "test"}},
		{{link, "source"}, {inside, "test"}},
		{{inside, "source"}, {inside, "test"}},
		{{inside, "source"}},
		{{inside, "unknown"}, {outside, "test"}},
	} {
		data, _ := json.Marshal(TuneReply{Command: "tests", Patches: patches})
		if _, err := readTuneReply(string(data), root); err == nil {
			t.Fatal(string(data))
		}
	}
}
