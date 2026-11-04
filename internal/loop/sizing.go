package loop

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

type SizeDecision struct {
	Decider string `json:"decider"`
	Mode    string `json:"mode"`
	Size    string `json:"size,omitempty"`
	Reason  string `json:"reason,omitempty"`
	Applied bool   `json:"applied"`
	Error   string `json:"error,omitempty"`
}

type SizeRequest struct {
	Task  string   `json:"task"`
	Plan  string   `json:"plan"`
	Tiers []string `json:"tiers"`
}

const sizeCommandTimeout = 2 * time.Minute

var plannerSizeRE = regexp.MustCompile(`(?im)^[\s*_>-]*size\s*[*_]*\s*:\s*[*_]*\s*(small|medium|large)\b[*_]*[\s:—–-]*(.*)$`)

func PlannerSize(plan string) (size, reason string, err error) {
	matches := plannerSizeRE.FindAllStringSubmatch(plan, -1)
	if len(matches) == 0 {
		return "", "", errors.New("the plan has no Size line")
	}
	last := matches[len(matches)-1]
	return strings.ToLower(last[1]), strings.TrimSpace(last[2]), nil
}

func CommandSize(ctx context.Context, command, dir string, req SizeRequest) (size, reason string, err error) {
	ctx, cancel := context.WithTimeout(ctx, sizeCommandTimeout)
	defer cancel()
	input, err := json.Marshal(req)
	if err != nil {
		return "", "", err
	}
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Dir = dir
	cmd.Stdin = bytes.NewReader(input)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return "", "", fmt.Errorf("sizing command: %w: %s", err, tailChars(strings.TrimSpace(stderr.String()), 300))
	}
	var answer struct {
		Size   string `json:"size"`
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &answer); err != nil {
		return "", "", fmt.Errorf("sizing command: want JSON {\"size\", \"reason\"} on stdout: %w", err)
	}
	size = strings.ToLower(strings.TrimSpace(answer.Size))
	if tierIndex(size) < 0 {
		return "", "", fmt.Errorf("sizing command answered %q, not one of %s", answer.Size, strings.Join(TierNames, ", "))
	}
	return size, strings.TrimSpace(answer.Reason), nil
}
