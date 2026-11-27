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
	Description string
	Score       string
	Name        string
	Path        string
	GateWhen    string
	Timeout     time.Duration
	Sections    []Section
	portable    bool
}

type Section struct {
	Name    string
	Timeout time.Duration
	Chairs  []ChairSpec
}

func (s Spec) Portable() bool { return s.portable }

type ChairSpec struct {
	Reply       string
	NativeAgent string
	Focus       string
	Tier        string
	Name        string
	Part        string
	Agents      []string
	Timeout     time.Duration
}

type file struct {
	Description string `toml:"description"`
	Score       string `toml:"score"`
	GateWhen    string `toml:"gate_when"`
	Timeout     string `toml:"timeout"`
	Sections    map[string]struct {
		Timeout string `toml:"timeout"`
		Chairs  map[string]struct {
			Reply       string   `toml:"reply"`
			NativeAgent string   `toml:"native_agent"`
			Focus       string   `toml:"focus"`
			Part        string   `toml:"part"`
			Tier        string   `toml:"tier"`
			Agents      []string `toml:"agents"`
			Timeout     string   `toml:"timeout"`
		} `toml:"chairs"`
	} `toml:"sections"`
}

var validNativeAgent = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

var validName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

func Find(name, home, worktree string, agents loop.Agents) (Spec, bool, error) {
	path, portable, found := Locate(name, home, worktree)
	if !found {
		return Spec{}, false, nil
	}
	spec, err := load(name, path, portable, agents)
	return spec, err == nil, err
}

func Path(name, home, worktree string) (string, bool) {
	path, _, found := Locate(name, home, worktree)
	return path, found
}

func Locate(name, home, worktree string) (string, bool, bool) {
	if !validName.MatchString(name) {
		return "", false, false
	}
	for _, dir := range dirs(home, worktree) {
		path := filepath.Join(dir.path, name+".toml")
		if _, err := os.Lstat(path); err == nil {
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

func load(name, path string, portable bool, agents loop.Agents) (Spec, error) {
	if err := checkName(name, path); err != nil {
		return Spec{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Spec{}, fmt.Errorf("%s: %w", path, err)
	}
	return parse(name, path, string(data), portable, agents, nil)
}

func Check(name, path, text string, agents loop.Agents) (Spec, error) {
	if err := checkName(name, path); err != nil {
		return Spec{}, err
	}
	return parse(name, path, text, false, agents, nil)
}

func CheckText(name, path, text string, agents loop.Agents, available []string) (Spec, error) {
	if err := checkName(name, path); err != nil {
		return Spec{}, err
	}
	return parse(name, path, text, false, agents, &available)
}

func CheckPortable(name, path, text string, agents loop.Agents) (Spec, error) {
	if err := checkName(name, path); err != nil {
		return Spec{}, err
	}
	return parse(name, path, text, true, agents, nil)
}

func parse(name, path, text string, portable bool, agents loop.Agents, available *[]string) (Spec, error) {
	spec := Spec{Name: name, Path: path, portable: portable}
	fail := func(key, format string, args ...any) (Spec, error) {
		return Spec{}, fmt.Errorf("%s: %s: %s", path, key, fmt.Sprintf(format, args...))
	}
	var f file
	md, err := toml.Decode(text, &f)
	if err != nil {
		return Spec{}, fmt.Errorf("%s: %w", path, err)
	}
	if extra := md.Undecoded(); len(extra) > 0 {
		return Spec{}, fmt.Errorf("%s: unknown keys %v", path, extra)
	}
	if md.IsDefined("description") && (strings.TrimSpace(f.Description) == "" || len(f.Description) > 512) {
		return fail("description", "must be non-empty text of at most 512 bytes")
	}
	spec.Description = f.Description
	if md.IsDefined("gate_when") {
		switch f.GateWhen {
		case loop.GateWhenAny, loop.GateWhenAll, loop.GateWhenAlways:
		default:
			return fail("gate_when", "%q is not any, all or always", f.GateWhen)
		}
	}
	if md.IsDefined("score") && !validName.MatchString(f.Score) {
		return fail("score", "invalid name %q", f.Score)
	}
	if message, ok := loop.ReservedNameMessage(f.Score); ok {
		return fail(fmt.Sprintf("score %q", f.Score), "%s", message)
	}
	spec.Score = f.Score
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
		if message, ok := loop.ReservedNameMessage(sectionName); ok {
			return fail(fmt.Sprintf("section %q", sectionName), "%s", message)
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
			if message, ok := loop.ReservedNameMessage(chairName); ok {
				return fail(fmt.Sprintf("chair %q", chairName), "%s", message)
			}
			c := raw.Chairs[chairName]
			chair := ChairSpec{Reply: c.Reply, Name: chairName, Part: c.Part, Agents: c.Agents, Tier: c.Tier, Focus: c.Focus, NativeAgent: c.NativeAgent}
			if md.IsDefined("sections", sectionName, "chairs", chairName, "reply") {
				if c.Reply != "file" && c.Reply != loop.ReplyMessage {
					return Spec{}, fmt.Errorf("%s: %s: invalid reply %q (want file or message)", path, (Member{Section: sectionName, Name: chairName}).Address(name), c.Reply)
				}
				if c.Reply == "file" {
					chair.Reply = ""
				}
			}
			if md.IsDefined("sections", sectionName, "chairs", chairName, "native_agent") {
				address := (Member{Section: sectionName, Name: chairName}).Address(name)
				if !validNativeAgent.MatchString(c.NativeAgent) {
					return Spec{}, fmt.Errorf("%s: %s: invalid native_agent %q", path, address, c.NativeAgent)
				}
				conflict := md.IsDefined("sections", sectionName, "chairs", chairName, "tier")
				for _, entry := range c.Agents {
					parts := strings.SplitN(entry, ":", 3)
					conflict = conflict || strings.Contains(entry, "@") || len(parts) == 3 && parts[2] != ""
				}
				if conflict {
					return Spec{}, fmt.Errorf("%s: %s: native_agent cannot use a tier or explicit model; the agent definition sets the model", path, address)
				}
			}
			if md.IsDefined("sections", sectionName, "chairs", chairName, "focus") && strings.TrimSpace(c.Focus) == "" {
				return fail(key+".focus", "want text, not an empty or whitespace-only focus")
			}
			if len(c.Focus) > 1024 {
				return fail(key+".focus", "is %d bytes; the limit is 1024", len(c.Focus))
			}
			if (f.Score == "" || f.Score == "default") && !slices.Contains(loop.AllRoles(), c.Part) {
				return fail(key+".part", "unknown part %q (want %s); optional parts: %s", c.Part, strings.Join(loop.ChairRoles, ", "), strings.Join(loop.OptionalRoles, ", "))
			}
			if f.Score != "" && f.Score != "default" && !validName.MatchString(c.Part) {
				return fail(key+".part", "invalid name %q", c.Part)
			}
			if len(c.Agents) == 0 {
				return fail(key+".agents", "want at least one agent")
			}
			if md.IsDefined("sections", sectionName, "chairs", chairName, "tier") && !slices.Contains(loop.TierNames, c.Tier) {
				return fail(key+".tier", "unknown tier %q (want %s)", c.Tier, strings.Join(loop.TierNames, ", "))
			}
			for _, agent := range c.Agents {
				base, pin, err := loop.SplitTier(agent)
				if err == nil && available != nil && (strings.Contains(base, ":") || !slices.Contains(*available, base)) {
					names := strings.Join(*available, ", ")
					if names == "" {
						names = "none"
					}
					address := (Member{Section: sectionName, Name: chairName}).Address(name)
					return Spec{}, fmt.Errorf("%s: %s: agent %q is not allowed; text ensembles may only use names from [agents] available (%s)", path, address, agent, names)
				}
				if err == nil && c.Tier != "" && pin != "" && pin != c.Tier {
					return fail(key+".tier", "tier %q conflicts with agent pin %q", c.Tier, pin)
				}
				if c.Tier != "" && err == nil && pin == "" {
					agent += "@" + c.Tier
				}

				if err := checkAgent(agent, portable || c.NativeAgent != "", agents); err != nil {
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

func checkAgent(agent string, portable bool, agents loop.Agents) error {
	base, _, err := loop.SplitTier(agent)
	if err != nil {
		return err
	}
	if portable && !slices.Contains(agents.Names(), base) {
		return fmt.Errorf("%q is not an agent (%s); a repository ensemble names agents, with optional @small|@medium|@large|@xlarge", agent, strings.Join(agents.Names(), ", "))
	}
	_, err = agents.ResolveChair(agent)
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

func (s Spec) Bind(agents loop.Agents, available []string) ([]Member, error) {
	var members []Member
	for _, section := range s.Sections {
		for _, c := range section.Chairs {
			agent, ok := "", false
			for _, entry := range c.Agents {
				if !Eligible(entry, available) {
					continue
				}
				resolved, err := agents.ResolveChair(entry)
				if (c.NativeAgent != "" || c.Reply == loop.ReplyMessage) && (err != nil || resolved.Provider != "claude_code") {
					continue
				}
				agent, ok = entry, true
				break
			}
			if !ok {
				m := Member{Section: section.Name, Name: c.Name}
				unavailable := &UnavailableError{Path: s.Path, Address: m.Address(s.Name), Section: section.Name, Chair: c.Name, Agents: c.Agents, Available: available, NativeAgent: c.NativeAgent}
				if c.Reply == loop.ReplyMessage {
					var claude []string
					for _, entry := range c.Agents {
						resolved, err := agents.ResolveChair(entry)
						if err == nil && resolved.Provider == "claude_code" {
							claude = append(claude, entry)
						}
					}
					if len(claude) == 0 {
						unavailable.WrongProvider = true
					} else {
						unavailable.Agents = claude
					}
				}
				return nil, unavailable
			}
			m, err := s.Seat(agents, section.Name, c.Name, agent)
			if err != nil {
				return nil, err
			}
			members = append(members, m)
		}
	}
	return members, nil
}

func FirstAvailable(agents, available []string) (string, bool) {
	for _, agent := range agents {
		if Eligible(agent, available) {
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

func ChairsOf(members []Member) (loop.Chairs, error) { return ChairsFor(loop.DefaultScore(), members) }

func ChairsFor(score loop.Score, members []Member) (loop.Chairs, error) {
	byPart := map[string][]Member{}
	for _, m := range members {
		if m.Chair.Reply == loop.ReplyMessage {
			for _, step := range score.StepsOf(m.Part) {
				if step.Kind == loop.KindProduce && step.Writes != "reply" {
					addr := m.Chair.Address
					if addr == "" {
						addr = m.Section + "." + m.Name
					}
					return loop.Chairs{}, fmt.Errorf("ensemble chair %s cannot use reply = message for worktree-changing step %s", addr, step.Name)
				}
			}
		}
		if _, ok := score.Part(m.Part); !ok && m.Part != "classifier" {
			return loop.Chairs{}, fmt.Errorf("ensemble chair %s.%s plays unknown part %q", m.Section, m.Name, m.Part)
		}
		byPart[m.Part] = append(byPart[m.Part], m)
	}
	var chairs loop.Chairs
	for _, step := range score.Steps {
		if score.Name != "default" && step.Kind != loop.KindProduce && len(byPart[score.Classifier]) == 0 {
			return loop.Chairs{}, fmt.Errorf("ensemble seats no classifier; step %s needs one", step.Name)
		}
	}
	for _, part := range score.Parts {
		seated := byPart[part.Name]
		if len(seated) == 0 {
			if part.Optional {
				continue
			}
			return loop.Chairs{}, fmt.Errorf("ensemble seats no %s; the score needs one", part.Name)
		}
		review := slices.ContainsFunc(score.StepsOf(part.Name), func(step loop.ScoreStep) bool {
			return step.Kind == loop.KindReview || step.Chairs == loop.ChairsAll
		})
		if review {
			for _, m := range seated {
				if part.Name == "reviewer" {
					chairs.Reviewers = append(chairs.Reviewers, m.Chair)
				} else {
					if chairs.Parts == nil {
						chairs.Parts = map[string][]loop.Chair{}
					}
					chairs.Parts[part.Name] = append(chairs.Parts[part.Name], m.Chair)
				}
			}
			if part.Name == "planner" {
				chairs.Planner = seated[0].Chair
			}
			if part.Name == "implementer" {
				chairs.Implementer = seated[0].Chair
			}
			if part.Name == "gate" {
				chairs.Gate = seated[0].Chair
			}
			if part.Name == "tester" {
				c := seated[0].Chair
				chairs.Tester = &c
			}
			continue
		}
		if len(seated) > 1 {
			var names []string
			for _, m := range seated {
				names = append(names, m.Section+"."+m.Name)
			}
			return loop.Chairs{}, fmt.Errorf("ensemble seats %d %ss (%s); the score has one", len(seated), part.Name, strings.Join(names, ", "))
		}
		chair := seated[0].Chair
		switch part.Name {
		case "planner":
			chairs.Planner = chair
		case "implementer":
			chairs.Implementer = chair
		case "gate":
			chairs.Gate = chair
		case "classifier":
			chairs.Classifier = chair
		case "tester":
			chairs.Tester = &chair
		default:
			if chairs.Parts == nil {
				chairs.Parts = map[string][]loop.Chair{}
			}
			chairs.Parts[part.Name] = []loop.Chair{chair}
		}
	}
	return chairs, nil
}

func ScoreName(name, home, worktree string) (string, error) {
	path, _, found := Locate(name, home, worktree)
	if !found {
		return "", nil
	}
	if message, ok := loop.ReservedNameMessage(name); ok {
		return "", fmt.Errorf("%s: ensemble %q: %s", path, name, message)
	}
	var f struct {
		Score string `toml:"score"`
	}
	md, err := toml.DecodeFile(path, &f)
	if err != nil {
		return "", fmt.Errorf("%s: %w", path, err)
	}
	if md.IsDefined("score") && !validName.MatchString(f.Score) {
		return "", fmt.Errorf("%s: score: invalid name %q", path, f.Score)
	}
	if message, ok := loop.ReservedNameMessage(f.Score); ok {
		return "", fmt.Errorf("%s: score %q: %s", path, f.Score, message)
	}
	return f.Score, nil
}

type UnavailableError struct {
	Path, Address, Section, Chair string
	NativeAgent                   string
	WrongProvider                 bool
	Agents, Available             []string
}

func (e *UnavailableError) Error() string {
	if e.WrongProvider {
		return fmt.Sprintf("%s: %s: reply = %q is allowed only on claude_code; other providers are refused until proven live", e.Path, e.Address, loop.ReplyMessage)
	}
	if e.NativeAgent != "" {
		return fmt.Sprintf("%s: %s: no listed agent is an available claude_code agent; only claude_code can play native agent %s", e.Path, e.Address, e.NativeAgent)
	}
	return fmt.Sprintf("%s: %s: none of %s is in [agents] available (%s)", e.Path, e.Address, strings.Join(e.Agents, ", "), strings.Join(e.Available, ", "))
}
func (s Spec) Ready(score loop.Score, agents loop.Agents, available []string) error {
	if err := s.replyFits(score); err != nil {
		return err
	}
	members, err := s.Bind(agents, available)
	if err != nil {
		return err
	}
	_, err = ChairsFor(score, members)
	return err
}

func (s Spec) replyFits(score loop.Score) error {
	for _, section := range s.Sections {
		for _, chair := range section.Chairs {
			if chair.Reply == loop.ReplyMessage {
				for _, step := range score.StepsOf(chair.Part) {
					if step.Kind == loop.KindProduce && step.Writes != "reply" {
						return fmt.Errorf("%s: %s: reply = message cannot play worktree-changing step %s", s.Path, (Member{Section: section.Name, Name: chair.Name}).Address(s.Name), step.Name)
					}
				}
			}
		}
	}
	return nil
}
func (s Spec) Fits(score loop.Score) error {
	if err := s.replyFits(score); err != nil {
		return err
	}
	var members []Member
	for _, section := range s.Sections {
		for _, chair := range section.Chairs {
			members = append(members, Member{Section: section.Name, Name: chair.Name, Part: chair.Part})
		}
	}
	_, err := ChairsFor(score, members)
	if err != nil {
		return fmt.Errorf("%s: %w", s.Path, err)
	}
	return nil
}

func checkName(name, path string) error {
	if message, ok := loop.ReservedNameMessage(name); ok {
		return fmt.Errorf("%s: ensemble %q: %s", path, name, message)
	}
	if !validName.MatchString(name) {
		return fmt.Errorf("%s: ensemble %q: want a lowercase name of letters, digits and dashes", path, name)
	}
	return nil
}

func Eligible(entry string, available []string) bool {
	base, _, _ := loop.SplitTier(entry)
	return strings.Contains(base, ":") || slices.Contains(available, base)
}

func (s Spec) Listed(section, chair string, available []string, agents loop.Agents) (string, []string) {
	for _, sec := range s.Sections {
		if sec.Name == section {
			for _, c := range sec.Chairs {
				if c.Name == chair {
					for i, entry := range c.Agents {
						if Eligible(entry, available) {
							resolved, err := agents.ResolveChair(entry)
							if (c.NativeAgent != "" || c.Reply == loop.ReplyMessage) && (err != nil || resolved.Provider != "claude_code") {
								continue
							}
							return entry, c.Agents[i+1:]
						}
					}
				}
			}
		}
	}
	return "", nil
}

func (s Spec) Seat(agents loop.Agents, section, chair, entry string) (Member, error) {
	for _, sec := range s.Sections {
		if sec.Name == section {
			for _, c := range sec.Chairs {
				if c.Name == chair {
					m := Member{Section: section, Name: chair, Part: c.Part}
					if c.Tier != "" {
						_, pin, _ := loop.SplitTier(entry)
						if pin == "" {
							entry += "@" + c.Tier
						}
					}
					resolved, err := agents.ResolveChair(entry)
					if err != nil {
						return Member{}, fmt.Errorf("%s: %s: %w", s.Path, m.Address(s.Name), err)
					}
					if c.Reply == loop.ReplyMessage && resolved.Provider != "claude_code" {
						return Member{}, fmt.Errorf("%s: %s: reply = %q is allowed only on claude_code; other providers are refused until proven live", s.Path, m.Address(s.Name), c.Reply)
					}
					if c.NativeAgent != "" {
						if resolved.Provider != "claude_code" {
							return Member{}, fmt.Errorf("%s cannot play native agent %s (only claude_code can)", entry, c.NativeAgent)
						}
						resolved.Agent = agents.NameOf(resolved)
						resolved.NativeAgent = c.NativeAgent
						resolved.NativeBase = resolved.Profile
						resolved.Profile, err = loop.NativeProfileName(resolved.Agent, c.NativeAgent)
						if err != nil {
							return Member{}, err
						}
					}
					resolved.Reply = c.Reply
					if resolved.Reply == "file" {
						resolved.Reply = ""
					}
					resolved.Focus = c.Focus
					resolved.Timeout = firstDuration(c.Timeout, sec.Timeout, s.Timeout)
					m.Chair = resolved
					return m, nil
				}
			}
		}
	}
	return Member{}, fmt.Errorf("%s: unknown chair %s.%s", s.Path, section, chair)
}
