// Package config loads and validates the Project config (.afk.toml).
package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// FileName is the Project config file name, located at the repository root.
const FileName = ".afk.toml"

// defaultMaxFixes is the default number of fix attempts after a failed gate.
const defaultMaxFixes = 3

var (
	// requiredProtectPaths are always protected, regardless of the user's list.
	requiredProtectPaths = []string{FileName, ".github/", ".gitea/", ".claude/"}

	forgeKinds = []string{"github", "gitea"}
	toolKinds  = []string{"package", "host-port", "vm"}
)

// Config is the effective Project config, with defaults applied.
type Config struct {
	Forge   Forge           `toml:"forge"`
	Labels  Labels          `toml:"labels"`
	Agent   Agent           `toml:"agent"`
	Gate    Gate            `toml:"gate"`
	Image   Image           `toml:"image"`
	Network Network         `toml:"network"`
	Protect Protect         `toml:"protect"`
	Tool    map[string]Tool `toml:"tool"`
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
	Ready         string `toml:"ready"`
	ReadyForHuman string `toml:"ready_for_human"`
	Wontfix       string `toml:"wontfix"`
}

// Agent configures models and the Factory plugin allowlist.
type Agent struct {
	Model          string   `toml:"model"`
	ReviewModel    string   `toml:"review_model"`
	FactoryPlugins []string `toml:"factory_plugins"`
}

// Gate is the check an agent's change must pass.
type Gate struct {
	Cmd      string `toml:"cmd"`
	MaxFixes int    `toml:"max_fixes"`
}

// Image configures the Base image.
type Image struct {
	Template  string   `toml:"template"`
	MaxAge    string   `toml:"max_age"`
	Provision []string `toml:"provision"`
}

// Network is the Run's network allowlist.
type Network struct {
	Allow []string `toml:"allow"`
}

// Protect lists paths the agent must not change.
type Protect struct {
	Paths []string `toml:"paths"`
}

// Tool is a dependency available to a Run.
type Tool struct {
	Kind string `toml:"kind"` // package | host-port | vm
}

// --- loading ---

// Load finds the repository root at or above dir and loads its Project config.
func Load(dir string) (cfg Config, err error) {
	root, err := findRoot(dir)
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
	cfg, err = Parse(data)
	if err != nil {
		err = fmt.Errorf("%s: %w", path, err)
	}
	return
}

// Parse decodes a Project config, applies defaults and validates it.
func Parse(data []byte) (cfg Config, err error) {
	cfg = defaults()
	decoder := toml.NewDecoder(bytes.NewReader(data)).DisallowUnknownFields()
	if err = decoder.Decode(&cfg); err != nil {
		var strictErr *toml.StrictMissingError
		if errors.As(err, &strictErr) {
			err = unknownKeysError(strictErr)
		} else {
			err = fmt.Errorf("parsing config: %w", err)
		}
		return
	}
	cfg.Protect.Paths = mergeProtectPaths(cfg.Protect.Paths)
	err = cfg.validate()
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
			Ready:         "ready-for-agent",
			ReadyForHuman: "ready-for-human",
			Wontfix:       "wontfix",
		},
		Gate: Gate{MaxFixes: defaultMaxFixes},
	}
	return
}

// findRoot walks up from dir to the first directory containing .git
// (a directory, or a file in worktrees).
func findRoot(dir string) (root string, err error) {
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

func unknownKeysError(strictErr *toml.StrictMissingError) (err error) {
	errs := make([]error, 0, len(strictErr.Errors))
	for _, detail := range strictErr.Errors {
		errs = append(errs, fmt.Errorf("unknown config key %q", strings.Join(detail.Key(), ".")))
	}
	err = errors.Join(errs...)
	return
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

// --- validation ---

func (c Config) validate() (err error) {
	if !slices.Contains(forgeKinds, c.Forge.Kind) {
		err = fmt.Errorf("invalid forge.kind %q: want one of %v", c.Forge.Kind, forgeKinds)
		return
	}
	for name, tool := range c.Tool {
		if !slices.Contains(toolKinds, tool.Kind) {
			err = fmt.Errorf("invalid tool.%s.kind %q: want one of %v", name, tool.Kind, toolKinds)
			return
		}
	}
	return
}
