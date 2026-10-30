// Package config loads and validates the Project config (.afk.toml).
package config

import (
	"bytes"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"

	"github.com/pelletier/go-toml/v2"
)

// FileName is the Project config file name, located at the repository root.
const FileName = ".afk.toml"

const (
	// SandboxMachine is the name of the Sandbox VM in [machine.<name>].
	SandboxMachine = "sandbox"

	// ForgeGitHub and ForgeGitea are the values of forge.kind.
	ForgeGitHub = "github"
	ForgeGitea  = "gitea"

	// defaultMaxFixes is the default number of fix attempts after a failed gate.
	defaultMaxFixes = 3

	// defaultTemplate is the default Template of the Sandbox VM's Base image.
	defaultTemplate = "debian13"

	// defaultMaxAge is the default Expiry of the Sandbox VM's Base image.
	defaultMaxAge = "168h"

	// defaultMemoryMiB is the default RAM of the Sandbox VM.
	defaultMemoryMiB = 2048

	// defaultCPUs is the default number of vCPUs of the Sandbox VM.
	defaultCPUs = 2
)

var (
	// requiredProtectPaths are always protected, regardless of the user's list.
	// Claude Code picks up .claude/ and .mcp.json in any directory, so they
	// are protected at any depth ("**/").
	requiredProtectPaths = []string{FileName, ".github/", ".gitea/", "**/.claude/", "**/.mcp.json"}
)

// Config is the effective Project config, with defaults applied.
type Config struct {
	Forge   Forge             `toml:"forge"`
	Labels  Labels            `toml:"labels"`
	Agent   Agent             `toml:"agent"`
	Gate    Gate              `toml:"gate"`
	Machine Machines          `toml:"machine"`
	Network Network           `toml:"network"`
	Protect Protect           `toml:"protect"`
	Tool    map[string]Tool   `toml:"tool"`
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

// Agent configures models and the Claude Code plugins installed in the
// Base image (ADR-0009).
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

// Machines are the machines of the Project. Only the Sandbox VM is supported
// for now; any other [machine.<name>] is rejected.
type Machines struct {
	Sandbox Machine `toml:"sandbox"`
}

// Machine describes a machine and its Base image. Only SandboxMachine is
// supported for now.
type Machine struct {
	Template  string   `toml:"template"`
	Build     []string `toml:"build"`
	MaxAge    string   `toml:"max_age"` // Expiry of the Base image; "0": none
	MemoryMiB int      `toml:"memory"`
	CPUs      int      `toml:"cpus"`
}

// Network is the Run's network allowlist.
type Network struct {
	Allow []string `toml:"allow"`
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

// Tool is a tool the Agent can use (ADR-0011).
type Tool struct {
	On     string            `toml:"on"`     // machine the Tool runs on; only SandboxMachine for now
	Build  []string          `toml:"build"`  // shell commands in the machine's Base image, after the machine's build
	Setup  []string          `toml:"setup"`  // shell commands in /work after the clone, before the first session
	Env    map[string]string `toml:"env"`    // variables of setup, the Agent's sessions and the gate
	Prompt string            `toml:"prompt"` // what the Agent is told about the Tool
}

// --- accessors ---

// Names returns the names of all labels, in the order of the fields.
func (l Labels) Names() (names []string) {
	return []string{l.NeedsTriage, l.NeedsInfo, l.ReadyForAgent, l.ReadyForHuman, l.Wontfix, l.Spec}
}

// EnvNames are the names of the Tool's env variables, sorted.
func (t Tool) EnvNames() (names []string) { return slices.Sorted(maps.Keys(t.Env)) }

// ToolEnv is the env of all Tools together; validation guarantees that no
// name is defined twice.
func (c Config) ToolEnv() (env map[string]string) {
	env = make(map[string]string)
	for _, tool := range c.Tool {
		maps.Copy(env, tool.Env)
	}
	return
}

// --- loading ---

// Load finds the repository root at or above dir and loads its Project config.
func Load(dir string) (cfg Config, err error) {
	root, err := FindRoot(dir)
	if err != nil {
		err = fmt.Errorf("finding project root: %w", err)
		return
	}
	path := filepath.Join(root, FileName)
	data, err := os.ReadFile(path)
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
	toolDefaults(&cfg)
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
		Machine: Machines{Sandbox: Machine{
			Template:  defaultTemplate,
			MaxAge:    defaultMaxAge,
			MemoryMiB: defaultMemoryMiB,
			CPUs:      defaultCPUs,
		}},
	}
	return
}

// toolDefaults sets the machine of Tools that name none.
func toolDefaults(cfg *Config) {
	for name, tool := range cfg.Tool {
		if tool.On == "" {
			tool.On = SandboxMachine
			cfg.Tool[name] = tool
		}
	}
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
			err = fmt.Errorf("no .git found in %s or any parent directory", start)
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
