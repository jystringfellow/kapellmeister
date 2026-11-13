package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jystringfellow/kapellmeister/internal/cao"
	"github.com/jystringfellow/kapellmeister/internal/loop"
)

func caoHome() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	path := strings.TrimSpace(os.Getenv("CAO_HOME_DIR"))
	if path == "" {
		path = filepath.Join(home, ".aws", "cli-agent-orchestrator")
	}
	if path == "~" {
		path = home
	} else if strings.HasPrefix(path, "~/") {
		path = filepath.Join(home, path[2:])
	}
	return filepath.Abs(path)
}

func proveCAOHome(ctx context.Context, client *cao.Client, home, profile string) (err error) {
	var token [16]byte
	if _, err = rand.Read(token[:]); err != nil {
		return err
	}
	name := fmt.Sprintf("km-probe-%x", token)
	source := fmt.Sprintf("---\nname: %s\ndescription: kapellmeister CAO home probe\nrole: developer\n---\n", name)
	if err := client.CreateProfile(ctx, name, source); err != nil {
		return fmt.Errorf("cannot establish that the CAO at %s uses the CAO home %s: %w", client.BaseURL, home, err)
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		if cleanupErr := client.DeleteProfile(cleanup, name); cleanupErr != nil {
			err = errors.Join(err, fmt.Errorf("cannot establish the CAO home: probe profile %s may remain at %s because cleanup failed: %w", name, client.BaseURL, cleanupErr))
		}
	}()
	path := filepath.Join(home, "agent-store", name+".md")
	data, readErr := os.ReadFile(path)
	if readErr != nil || string(data) != source {
		return fmt.Errorf("refusing to install CAO profile %s: the CAO at %s did not write the probe into the CAO home %s that cao install writes to (set CAO_HOME_DIR to that CAO server's home, or point -cao at the CAO that uses it): %v", profile, client.BaseURL, home, readErr)
	}
	return nil
}

func guardProfile(home, name, source string) error {
	data, err := os.ReadFile(filepath.Join(home, "agent-store", name+".md"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if bytes.Equal(data, []byte(source)) {
		return nil
	}
	found, _, err := findRuns([]string{defaultRunsDir()}, defaultRecordDirs, "", 64, 1000000, true, false)
	if err != nil {
		return nil
	}
	for _, recorded := range unfinishedRuns(found) {
		s := recorded.state
		if s == nil || s.PID == os.Getpid() || runLiveness(*s) != "alive" {
			continue
		}
		return fmt.Errorf("refusing to replace CAO profile %s: run %s (pid %d, %s, started %s) is still alive in %s and its chairs may depend on it; wait for it to finish, or use a separate KAPELLMEISTER_STATE_DIR and CAO home", name, recorded.RunID, s.PID, s.Command, s.Started.Format(time.RFC3339), loop.StateDir())
	}
	return nil
}
