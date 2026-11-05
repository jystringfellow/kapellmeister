package ensemble

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/jystringfellow/kapellmeister/internal/loop"
)

const (
	RepoDir = ".kapellmeister/ensembles"
	UserDir = "ensembles"
)

type Spec struct {
	Name     string
	Path     string
	GateWhen string
	Timeout  time.Duration
	Sections []Section
	portable bool
}

type Section struct {
	Name    string
	Timeout time.Duration
	Chairs  []ChairSpec
}

type ChairSpec struct {
	Name    string
	Part    string
	Agents  []string
	Timeout time.Duration
}

type file struct {
	GateWhen string `toml:"gate_when"`
	Timeout  string `toml:"timeout"`
	Sections map[string]struct {
		Timeout string `toml:"timeout"`
		Chairs  map[string]struct {
			Part    string   `toml:"part"`
			Agents  []string `toml:"agents"`
			Timeout string   `toml:"timeout"`
		} `toml:"chairs"`
	} `toml:"sections"`
}

var validName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

func Find(name, home, worktree string) (Spec, bool, error) {
	path, portable, found := locate(name, home, worktree)
	if !found {
		return Spec{}, false, nil
	}
	spec, err := load(name, path, portable)
	return spec, err == nil, err
}

func Path(name, home, worktree string) (string, bool) {
	path, _, found := locate(name, home, worktree)
	return path, found
}

func locate(name, home, worktree string) (string, bool, bool) {
	if !validName.MatchString(name) {
		return "", false, false
	}
	for _, dir := range dirs(home, worktree) {
		path := filepath.Join(dir.path, name+".toml")
		if _, err := os.Stat(path); err == nil {
			return path, dir.portable, true
		}
	}
	return "", false, false
}

func Names(home, worktree string) []string {
	var names []string
	for _, dir := range dirs(home, worktree) {
		paths, _ := filepath.Glob(filepath.Join(dir.path, "*.toml"))
		for _, path := range paths {
			if name := strings.TrimSuffix(filepath.Base(path), ".toml"); validName.MatchString(name) && !slices.Contains(names, name) {
				names = append(names, name)
			}
		}
	}
	slices.Sort(names)
	return names
}

type source struct {
	path     string
	portable bool
}

func dirs(home, worktree string) []source {
	out := []source{{filepath.Join(home, UserDir), false}}
	if worktree != "" {
		out = append(out, source{filepath.Join(worktree, RepoDir), true})
	}
	return out
}

func load(name, path string, portable bool) (Spec, error) {
	spec := Spec{Name: name, Path: path, portable: portable}
	fail := func(key, format string, args ...any) (Spec, error) {
		return Spec{}, fmt.Errorf("%s: %s: %s", path, key, fmt.Sprintf(format, args...))
	}
	var f file
	md, err := toml.DecodeFile(path, &f)
	if err != nil {
		return Spec{}, fmt.Errorf("%s: %w", path, err)
	}
	if extra := md.Undecoded(); len(extra) > 0 {
		return Spec{}, fmt.Errorf("%s: unknown keys %v", path, extra)
	}
	if md.IsDefined("gate_when") {
		switch f.GateWhen {
		case loop.GateWhenAny, loop.GateWhenAll, loop.GateWhenAlways:
		default:
			return fail("gate_when", "%q is not any, all or always", f.GateWhen)
		}
	}
	spec.GateWhen = f.GateWhen
	if spec.Timeout, err = duration(f.Timeout); err != nil {
		return fail("timeout", "%v", err)
	}
	sections, chairs := order(md)
	for _, sectionName := range sections {
		key := "sections." + sectionName
		if !validName.MatchString(sectionName) {
			return fail(key, "want a lowercase name of letters, digits and dashes")
		}
		raw := f.Sections[sectionName]
		section := Section{Name: sectionName}
		if section.Timeout, err = duration(raw.Timeout); err != nil {
			return fail(key+".timeout", "%v", err)
		}
		for _, chairName := range chairs[sectionName] {
			key := key + ".chairs." + chairName
			if !validName.MatchString(chairName) {
				return fail(key, "want a lowercase name of letters, digits and dashes")
			}
			c := raw.Chairs[chairName]
			chair := ChairSpec{Name: chairName, Part: c.Part, Agents: c.Agents}
			if !slices.Contains(loop.ChairRoles, c.Part) {
				return fail(key+".part", "unknown part %q (want %s)", c.Part, strings.Join(loop.ChairRoles, ", "))
			}
			if len(c.Agents) == 0 {
				return fail(key+".agents", "want at least one agent")
			}
			for _, agent := range c.Agents {
				if err := checkAgent(agent, portable); err != nil {
					return fail(key+".agents", "%v", err)
				}
			}
			if chair.Timeout, err = duration(c.Timeout); err != nil {
				return fail(key+".timeout", "%v", err)
			}
			section.Chairs = append(section.Chairs, chair)
		}
		if len(section.Chairs) == 0 {
			return fail(key, "want at least one chair")
		}
		spec.Sections = append(spec.Sections, section)
	}
	return spec, nil
}

func order(md toml.MetaData) ([]string, map[string][]string) {
	var sections []string
	chairs := map[string][]string{}
	for _, key := range md.Keys() {
		if len(key) < 2 || key[0] != "sections" {
			continue
		}
		if !slices.Contains(sections, key[1]) {
			sections = append(sections, key[1])
		}
		if len(key) >= 4 && key[2] == "chairs" && !slices.Contains(chairs[key[1]], key[3]) {
			chairs[key[1]] = append(chairs[key[1]], key[3])
		}
	}
	return sections, chairs
}

func checkAgent(agent string, portable bool) error {
	base, _, err := loop.SplitTier(agent)
	if err != nil {
		return err
	}
	if portable && !slices.Contains(loop.AgentNames(), base) {
		return fmt.Errorf("%q is not an agent (%s); a repository ensemble names agents, with optional @small|@medium|@large", agent, strings.Join(loop.AgentNames(), ", "))
	}
	_, err = loop.ResolveChair(agent)
	return err
}

func duration(value string) (time.Duration, error) {
	if value == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(value)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("%q is not a duration such as \"40m\"", value)
	}
	return d, nil
}

func (s Spec) Bind(available []string) ([]Member, error) {
	var members []Member
	for _, section := range s.Sections {
		for _, c := range section.Chairs {
			m := Member{Section: section.Name, Part: c.Part, Name: c.Name}
			agent, ok := firstAvailable(c.Agents, available)
			if !ok {
				return nil, fmt.Errorf("%s: %s: none of %s is in [agents] available (%s)", s.Path, m.Address(s.Name), strings.Join(c.Agents, ", "), strings.Join(available, ", "))
			}
			chair, err := loop.ResolveChair(agent)
			if err != nil {
				return nil, fmt.Errorf("%s: %s: %w", s.Path, m.Address(s.Name), err)
			}
			chair.Timeout = firstDuration(c.Timeout, section.Timeout, s.Timeout)
			m.Chair = chair
			members = append(members, m)
		}
	}
	return members, nil
}

func firstAvailable(agents, available []string) (string, bool) {
	for _, agent := range agents {
		base, _, _ := loop.SplitTier(agent)
		if strings.Contains(base, ":") || slices.Contains(available, base) {
			return agent, true
		}
	}
	return "", false
}

func firstDuration(ds ...time.Duration) time.Duration {
	for _, d := range ds {
		if d != 0 {
			return d
		}
	}
	return 0
}

func ChairsOf(members []Member) (loop.Chairs, error) {
	byPart := map[string][]Member{}
	for _, m := range members {
		byPart[m.Part] = append(byPart[m.Part], m)
	}
	var chairs loop.Chairs
	for _, part := range loop.ChairRoles {
		seated := byPart[part]
		if len(seated) == 0 {
			return loop.Chairs{}, fmt.Errorf("ensemble seats no %s; the score needs one", part)
		}
		if part == "reviewer" {
			for _, m := range seated {
				chairs.Reviewers = append(chairs.Reviewers, m.Chair)
			}
			continue
		}
		if len(seated) > 1 {
			var names []string
			for _, m := range seated {
				names = append(names, m.Section+"."+m.Name)
			}
			return loop.Chairs{}, fmt.Errorf("ensemble seats %d %ss (%s); the score has one", len(seated), part, strings.Join(names, ", "))
		}
		chair := seated[0].Chair
		switch part {
		case "planner":
			chairs.Planner = chair
		case "implementer":
			chairs.Implementer = chair
		case "gate":
			chairs.Gate = chair
		case "classifier":
			chairs.Classifier = chair
		}
	}
	return chairs, nil
}
