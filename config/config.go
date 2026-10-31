// Package config loads and validates the Project config (.afk.toml).
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"

	"github.com/pelletier/go-toml/v2"
)

// FileName is the Project config file name, located at the repository root.
const FileName = ".afk.toml"

const (
	// ForgeGitHub and ForgeGitea are the values of forge.kind.
	ForgeGitHub = "github"
	ForgeGitea  = "gitea"

	// defaultMaxFixes is the default number of fix attempts after a failed gate.
	defaultMaxFixes = 3
)

var (
	// requiredProtectPaths are always protected, regardless of the user's list.
	// Claude Code picks up .claude/ and .mcp.json in any directory, so they
	// are protected at any depth ("**/"). The Dockerfiles of the Stack come
	// on top of these (stack.Protected): they depend on the compose files.
	// .env feeds the interpolation of the compose files, and .afk/ is where
	// the template keeps the Dockerfile of the Sandbox.
	requiredProtectPaths = []string{FileName, "compose.yml", "compose.afk.yml", ".env", ".afk/", ".github/", ".gitea/", "**/.claude/", "**/.mcp.json"}
)

// Config is the effective Project config, with defaults applied.
type Config struct {
	Forge   Forge             `toml:"forge"`
	Labels  Labels            `toml:"labels"`
	Agent   Agent             `toml:"agent"`
	Gate    Gate              `toml:"gate"`
	Protect Protect           `toml:"protect"`
	Expose  map[string]string `toml:"expose"` // compose service -> [address:]host_port:container_port
	VM      []VM              `toml:"vm"`
}

// Forge describes the system hosting the repository and tickets.
type Forge struct {
	Kind string `toml:"kind"` // github | gitea
	Repo string `toml:"repo"`
	URL  string `toml:"url"`
}

// Labels maps triage roles to label names in the tracker.
type Labels struct {
	NeedsTriage   string `toml:"needs_triage"`
	NeedsInfo     string `toml:"needs_info"`
	ReadyForAgent string `toml:"ready_for_agent"`
	ReadyForHuman string `toml:"ready_for_human"`
	Wontfix       string `toml:"wontfix"`
	Spec          string `toml:"spec"`
}

// Agent configures models and the Claude Code plugins available to the
// Agent in the Sandbox (ADR-0009).
type Agent struct {
	Model        string   `toml:"model"`
	ReviewModel  string   `toml:"review_model"`
	Plugins      []string `toml:"plugins"`      // <name>@<marketplace>
	Marketplaces []string `toml:"marketplaces"` // owner/repo or git URL
}

// Gate is the check an agent's change must pass.
type Gate struct {
	Cmd      string `toml:"cmd"`
	MaxFixes int    `toml:"max_fixes"`
}

// Protect lists paths the agent must not change: a file, a directory
// ("dir/"), either at any depth with the prefix "**/".
type Protect struct {
	Paths []string `toml:"paths"`
}

// VM is a test VM: an existing libvirt domain the Agent in a Chat drives.
type VM struct {
	Name string `toml:"name"` // libvirt domain
	User string `toml:"user"` // ssh account
	OS   string `toml:"os"`   // windows | linux
}

// --- accessors ---

// Names returns the names of all labels, in the order of the fields.
func (l Labels) Names() (names []string) {
	return []string{l.NeedsTriage, l.NeedsInfo, l.ReadyForAgent, l.ReadyForHuman, l.Wontfix, l.Spec}
}

// --- loading ---

// Load finds the repository root at or above dir and loads its Project config.
func Load(dir string) (cfg Config, err error) {
	root, err := FindRoot(dir)
	if err != nil {
		return
	}
	path := filepath.Join(root, FileName)
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		err = fmt.Errorf("no %s in %s (the project root): run `afk init`", FileName, root)
		return
	}
	if err != nil {
		err = fmt.Errorf("reading %s: %w", path, err)
		return
	}
	cfg, err = ParseNamed(path, data)
	return
}

// Parse decodes a Project config, applies defaults and validates it.
// The error joins all problems, one per line, each starting with "line:col:"
// when the offending key is in the file, then the key path.
func Parse(data []byte) (cfg Config, err error) {
	cfg, err = parse("", data)
	return
}

// ParseNamed is Parse with name (a file path, say) prefixed to every error line.
func ParseNamed(name string, data []byte) (cfg Config, err error) {
	cfg, err = parse(name+": ", data)
	return
}

func parse(prefix string, data []byte) (cfg Config, err error) {
	cfg = defaults()
	v := validator{data: data}
	decoder := toml.NewDecoder(bytes.NewReader(data)).DisallowUnknownFields()
	if decodeErr := decoder.Decode(&cfg); decodeErr != nil {
		strictErr, ok := errors.AsType[*toml.StrictMissingError](decodeErr)
		if !ok {
			// Any other decoding error (a wrong type) ends go-toml's decoding:
			// it is the only issue reported.
			v.reportDecodeError(decodeErr)
			err = v.err(prefix)
			return
		}
		// Unknown keys do not stop the decoding: the rest is validated too.
		v.reportUnknownKeys(strictErr)
	}
	cfg.validate(&v)
	if err = v.err(prefix); err != nil {
		return
	}
	cfg.Protect.Paths = mergeProtectPaths(cfg.Protect.Paths)
	return
}

// Marshal renders the config as TOML.
func (c Config) Marshal() (data []byte, err error) {
	data, err = toml.Marshal(c)
	if err != nil {
		err = fmt.Errorf("marshaling config: %w", err)
	}
	return
}

func defaults() (cfg Config) {
	cfg = Config{
		Labels: Labels{
			NeedsTriage:   "needs-triage",
			NeedsInfo:     "needs-info",
			ReadyForAgent: "ready-for-agent",
			ReadyForHuman: "ready-for-human",
			Wontfix:       "wontfix",
			Spec:          "spec",
		},
		Gate: Gate{MaxFixes: defaultMaxFixes},
	}
	return
}

// FindRoot walks up from dir to the first directory containing .git
// (a directory, or a file in worktrees).
func FindRoot(dir string) (root string, err error) {
	start, err := filepath.Abs(dir)
	if err != nil {
		err = fmt.Errorf("resolving %s: %w", dir, err)
		return
	}
	for cur := start; ; cur = filepath.Dir(cur) {
		if _, statErr := os.Stat(filepath.Join(cur, ".git")); statErr == nil {
			root = cur
			return
		}
		if filepath.Dir(cur) == cur {
			err = fmt.Errorf("no git repository at or above %s: run afk inside a clone of the Project", start)
			return
		}
	}
}

// mergeProtectPaths puts the required paths first, then the user's, without duplicates.
func mergeProtectPaths(user []string) (merged []string) {
	merged = slices.Clone(requiredProtectPaths)
	for _, path := range user {
		if !slices.Contains(merged, path) {
			merged = append(merged, path)
		}
	}
	return
}
