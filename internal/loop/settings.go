package loop

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

const (
	HomeEnv        = "KAPELLMEISTER_HOME"
	StateEnv       = "KAPELLMEISTER_STATE_DIR"
	CAOURLEnv      = "CAO_API_BASE_URL"
	UserConfigFile = "config.toml"
	homeDirName    = ".kapellmeister"
)

var SeatRoles = []string{"planner", "implementer", "reviewer", "gate", "classifier"}

type TestConfig struct {
	Verify string `toml:"verify"`
	Check  string `toml:"check"`
}

type RunConfig struct {
	Rounds           *int   `toml:"rounds"`
	VerifyRetries    *int   `toml:"verify_retries"`
	Timeout          string `toml:"timeout"`
	ReplyGrace       string `toml:"reply_grace"`
	PreflightTimeout string `toml:"preflight_timeout"`
}

type FileConfig struct {
	Test TestConfig `toml:"test"`
	CAO  struct {
		URL string `toml:"url"`
	} `toml:"cao"`
	Run   RunConfig         `toml:"run"`
	Seats map[string]string `toml:"seats"`
	Rig   struct {
		Agents []string `toml:"agents"`
	} `toml:"rig"`
	Tiers  map[string]map[string]TierSpec `toml:"tiers"`
	Steps  map[string]StepPolicy          `toml:"steps"`
	Sizing SizingConfig                   `toml:"sizing"`
}

type Settings struct {
	Files            []string          `json:"files"`
	CAOURL           string            `json:"cao_url"`
	Rounds           int               `json:"rounds"`
	VerifyRetries    int               `json:"verify_retries"`
	Timeout          time.Duration     `json:"timeout"`
	ReplyGrace       time.Duration     `json:"reply_grace"`
	PreflightTimeout time.Duration     `json:"preflight_timeout"`
	Seats            map[string]string `json:"seats"`
	RigAgents        []string          `json:"rig_agents"`
	Policy           Policy            `json:"policy"`
}

func Home() string {
	if dir := os.Getenv(HomeEnv); dir != "" {
		return dir
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, homeDirName)
}

func StateDir() string {
	if dir := os.Getenv(StateEnv); dir != "" {
		return dir
	}
	return filepath.Join(Home(), "state")
}

func UserConfigPath(home string) string {
	return filepath.Join(home, UserConfigFile)
}

func DefaultSettings() Settings {
	return Settings{
		CAOURL:           "http://127.0.0.1:9889",
		Rounds:           3,
		VerifyRetries:    1,
		Timeout:          40 * time.Minute,
		ReplyGrace:       15 * time.Minute,
		PreflightTimeout: 4 * time.Minute,
		Seats: map[string]string{
			"planner": "claude", "implementer": "codex", "reviewer": "copilot", "gate": "claude", "classifier": "claude",
		},
		RigAgents: []string{"claude", "codex", "copilot", "agy"},
		Policy:    DefaultPolicy(),
	}
}

var (
	userOnlySections = []string{"cao", "seats", "rig"}
	repoOnlySections = []string{"test"}
)

func LoadSettings(home, worktree string) (Settings, error) {
	s := DefaultSettings()
	userPath := UserConfigPath(home)
	user, userFound, err := loadConfigFile(userPath, userPath)
	if err != nil {
		return s, err
	}
	if err := refuseSections(user, userPath, repoOnlySections, "the repository's "+ConfigFile); err != nil {
		return s, err
	}
	repo, repoFound := loadedConfig{}, false
	if worktree != "" {
		if repo, repoFound, err = loadConfigFile(filepath.Join(worktree, ConfigFile), ConfigFile); err != nil {
			return s, err
		}
		if err := refuseSections(repo, ConfigFile, userOnlySections, userPath); err != nil {
			return s, err
		}
	}
	if userFound {
		s.Files = append(s.Files, userPath)
	}
	if repoFound {
		s.Files = append(s.Files, filepath.Join(worktree, ConfigFile))
	}

	s.CAOURL = firstSet(os.Getenv(CAOURLEnv), user.CAO.URL, s.CAOURL)
	for _, role := range sortedKeys(user.Seats) {
		if !slices.Contains(SeatRoles, role) {
			return s, fmt.Errorf("%s: seats.%s: unknown role (want %s)", userPath, role, strings.Join(SeatRoles, ", "))
		}
		s.Seats[role] = user.Seats[role]
	}
	if len(user.Rig.Agents) > 0 {
		s.RigAgents = user.Rig.Agents
	}
	for _, layer := range []loadedConfig{user, repo} {
		if err := s.applyRun(layer); err != nil {
			return s, err
		}
	}

	p := DefaultPolicy()
	p = p.withTiers(repo.Tiers).withTiers(user.Tiers)
	p = p.withSteps(user.Steps).withSteps(repo.Steps)
	p = p.withSizing(user.Sizing).withSizing(repo.Sizing)
	s.Policy = p
	if err := p.Validate(); err != nil {
		return s, fmt.Errorf("%s: %w", strings.Join(append([]string{"config"}, s.Files...), ", "), err)
	}
	return s, nil
}

type loadedConfig struct {
	FileConfig
	md   toml.MetaData
	name string
}

func loadConfigFile(path, name string) (loadedConfig, bool, error) {
	cfg := loadedConfig{name: name}
	md, err := toml.DecodeFile(path, &cfg.FileConfig)
	if errors.Is(err, os.ErrNotExist) {
		return cfg, false, nil
	}
	if err != nil {
		return cfg, false, fmt.Errorf("%s: %w", name, err)
	}
	if extra := md.Undecoded(); len(extra) > 0 {
		return cfg, false, fmt.Errorf("%s: unknown keys %v", name, extra)
	}
	cfg.md = md
	return cfg, true, nil
}

func refuseSections(cfg loadedConfig, name string, sections []string, home string) error {
	for _, section := range sections {
		if cfg.md.IsDefined(section) {
			return fmt.Errorf("%s: [%s] belongs in %s", name, section, home)
		}
	}
	return nil
}

func (s *Settings) applyRun(layer loadedConfig) error {
	r := layer.Run
	if r.Rounds != nil {
		if *r.Rounds < 1 {
			return fmt.Errorf("%s: run.rounds: must be at least 1", layer.name)
		}
		s.Rounds = *r.Rounds
	}
	if r.VerifyRetries != nil {
		if *r.VerifyRetries < 0 {
			return fmt.Errorf("%s: run.verify_retries: must be 0 or more", layer.name)
		}
		s.VerifyRetries = *r.VerifyRetries
	}
	for _, d := range []struct {
		key   string
		value string
		into  *time.Duration
	}{
		{"timeout", r.Timeout, &s.Timeout},
		{"reply_grace", r.ReplyGrace, &s.ReplyGrace},
		{"preflight_timeout", r.PreflightTimeout, &s.PreflightTimeout},
	} {
		if d.value == "" {
			continue
		}
		parsed, err := time.ParseDuration(d.value)
		if err != nil || parsed < 0 {
			return fmt.Errorf("%s: run.%s: %q is not a duration such as \"40m\"", layer.name, d.key, d.value)
		}
		*d.into = parsed
	}
	return nil
}
