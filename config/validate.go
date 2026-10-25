package config

import (
	"errors"
	"fmt"
	"maps"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
	"github.com/pelletier/go-toml/v2/unstable"

	"github.com/graf0/afk/egress"
)

var (
	forgeKinds = []string{"github", "gitea"}
	toolKinds  = []string{"package", "host-port", "vm"}

	// ImageTemplates are the [machine.<name>].template values the image package can
	// build; image's tests assert that it knows exactly these.
	ImageTemplates = []string{"debian13"}

	repoPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
)

// --- issues ---

// position is a place in the config file; the zero value is "nowhere".
type position struct{ line, col int }

// issue is one problem found in the config.
type issue struct {
	pos position
	msg string
}

func (i issue) String() (text string) {
	if i.pos == (position{}) {
		return i.msg
	}
	return fmt.Sprintf("%d:%d: %s", i.pos.line, i.pos.col, i.msg)
}

// validator collects issues, locating them by key path in data.
type validator struct {
	data      []byte
	positions map[string]position // built on the first lookup: valid configs never need it
	issues    []issue
}

// report adds an issue for the key at path ("a.b", "a.b[2]" for a list
// element). A key absent from the file (required but missing) has no position.
func (v *validator) report(path, format string, args ...any) {
	v.issues = append(v.issues, issue{pos: v.locate(path), msg: path + ": " + fmt.Sprintf(format, args...)})
}

// reportChoice reports got, not among choices, for the key at path.
func (v *validator) reportChoice(path string, choices []string, got string) {
	if got == "" {
		v.report(path, "required (want one of %v)", choices)
		return
	}
	v.report(path, "want one of %v, got %q", choices, got)
}

func (v *validator) reportDecodeError(err error) {
	var detail *toml.DecodeError
	if !errors.As(err, &detail) {
		v.issues = append(v.issues, issue{msg: fmt.Sprintf("parsing config: %v", err)})
		return
	}
	found := issue{pos: positionOf(detail), msg: detail.Error()}
	if key := detail.Key(); len(key) > 0 {
		found.msg = strings.Join(key, ".") + ": " + found.msg
	}
	v.issues = append(v.issues, found)
}

// reportUnknownKeys reports each unknown key at the start of its name, like
// any other key.
func (v *validator) reportUnknownKeys(strictErr *toml.StrictMissingError) {
	for i := range strictErr.Errors {
		detail := &strictErr.Errors[i]
		key := detail.Key()
		path := strings.Join(key, ".")
		pos := v.locate(path)
		if pos == (position{}) {
			pos = positionOf(detail)
		}
		msg := fmt.Sprintf("unknown key %q", path)
		if len(key) >= 2 && key[0] == "machine" && key[1] != SandboxMachine {
			msg = fmt.Sprintf("machine.%s: Tool VM is not supported yet: only machine.%s is allowed", key[1], SandboxMachine)
		}
		v.issues = append(v.issues, issue{pos: pos, msg: msg})
	}
}

// err joins the issues, each on its own line with prefix, ordered by
// position (those without one last).
func (v *validator) err(prefix string) (err error) {
	slices.SortStableFunc(v.issues, func(a, b issue) int {
		if aNone, bNone := a.pos == (position{}), b.pos == (position{}); aNone != bNone {
			if aNone {
				return 1
			}
			return -1
		}
		if a.pos.line != b.pos.line {
			return a.pos.line - b.pos.line
		}
		return a.pos.col - b.pos.col
	})
	errs := make([]error, 0, len(v.issues))
	for _, found := range v.issues {
		errs = append(errs, errors.New(prefix+found.String()))
	}
	err = errors.Join(errs...)
	return
}

func positionOf(detail *toml.DecodeError) (pos position) {
	pos.line, pos.col = detail.Position()
	return
}

// --- rules ---

func (c Config) validate(v *validator) {
	c.validateForge(v)
	c.validateLabels(v)
	c.validateAgent(v)
	c.validateGate(v)
	c.validateMachines(v)
	c.validateNetwork(v)
	c.validateProtect(v)
	c.validateTools(v)
}

func (c Config) validateForge(v *validator) {
	if !slices.Contains(forgeKinds, c.Forge.Kind) {
		v.reportChoice("forge.kind", forgeKinds, c.Forge.Kind)
	}
	switch {
	case c.Forge.Repo == "":
		v.report("forge.repo", "required")
	case !validRepo(c.Forge.Repo):
		v.report("forge.repo", "want owner/name, got %q", c.Forge.Repo)
	}
	switch {
	case c.Forge.Kind == "gitea" && c.Forge.URL == "":
		v.report("forge.url", "required for gitea")
	case c.Forge.Kind == "github" && c.Forge.URL != "":
		v.report("forge.url", "not allowed for github")
	case c.Forge.URL != "":
		// https only: the forge token is sent in a header.
		if u, err := url.Parse(c.Forge.URL); err != nil || u.Scheme != "https" || u.Host == "" {
			v.report("forge.url", "want an https URL, got %q", c.Forge.URL)
		}
	}
}

// validRepo reports whether repo is owner/name, with no segment of only dots.
func validRepo(repo string) (ok bool) {
	if !repoPattern.MatchString(repo) {
		return false
	}
	return !slices.ContainsFunc(strings.Split(repo, "/"), func(segment string) bool {
		return strings.Trim(segment, ".") == ""
	})
}

func (c Config) validateLabels(v *validator) {
	l := c.Labels
	roles := []struct{ key, label string }{
		{"needs_triage", l.NeedsTriage},
		{"needs_info", l.NeedsInfo},
		{"ready_for_agent", l.ReadyForAgent},
		{"ready_for_human", l.ReadyForHuman},
		{"wontfix", l.Wontfix},
		{"spec", l.Spec},
	}
	for i, role := range roles {
		path := "labels." + role.key
		if role.label == "" {
			v.report(path, "must not be empty")
			continue
		}
		for _, earlier := range roles[:i] {
			if earlier.label == role.label {
				v.report(path, "same as labels.%s (%q)", earlier.key, role.label)
				break
			}
		}
	}
}

// validateAgent checks only the format of plugin ids: a marketplace's name
// is defined in its marketplace.json, so it cannot be matched against the
// sources in [agent].marketplaces without fetching them (ADR-0009).
func (c Config) validateAgent(v *validator) {
	if c.Agent.Model != "" && c.Agent.Model == c.Agent.ReviewModel {
		v.report("agent.review_model", "must differ from agent.model (%q)", c.Agent.Model)
	}
	for i, id := range c.Agent.Plugins {
		if !pluginIDPattern.MatchString(id) {
			v.report(fmt.Sprintf("agent.plugins[%d]", i), "want name@marketplace, got %q", id)
		}
	}
}

func (c Config) validateGate(v *validator) {
	if strings.TrimSpace(c.Gate.Cmd) == "" {
		v.report("gate.cmd", "required")
	}
	if c.Gate.MaxFixes < 0 {
		v.report("gate.max_fixes", "want >= 0, got %d", c.Gate.MaxFixes)
	}
}

func (c Config) validateMachines(v *validator) {
	m := c.Machine.Sandbox
	path := "machine." + SandboxMachine
	if !slices.Contains(ImageTemplates, m.Template) {
		v.reportChoice(path+".template", ImageTemplates, m.Template)
	}
	if age, err := time.ParseDuration(m.MaxAge); err != nil || age < 0 {
		v.report(path+".max_age", `want a duration like "168h" or "0", got %q`, m.MaxAge)
	}
	if m.MemoryMiB <= 0 {
		v.report(path+".memory", "want MiB > 0, got %d", m.MemoryMiB)
	}
	if m.CPUs <= 0 {
		v.report(path+".cpus", "want > 0, got %d", m.CPUs)
	}
}

// validateNetwork accepts what the Egress proxy accepts: host names, IP
// literals and "*.domain" wildcards; no scheme, port or path.
func (c Config) validateNetwork(v *validator) {
	for i, entry := range c.Network.Allow {
		if _, err := egress.Allowlist([]string{entry}); err != nil {
			v.report(fmt.Sprintf("network.allow[%d]", i), "%v", err)
		}
	}
}

func (c Config) validateProtect(v *validator) {
	for i, path := range c.Protect.Paths {
		switch {
		case path == "":
			v.report(fmt.Sprintf("protect.paths[%d]", i), "must not be empty")
		case strings.HasPrefix(path, "/"):
			v.report(fmt.Sprintf("protect.paths[%d]", i), "must be relative, got %q", path)
		case slices.Contains(strings.Split(path, "/"), ".."):
			v.report(fmt.Sprintf("protect.paths[%d]", i), `must not contain "..", got %q`, path)
		}
	}
}

func (c Config) validateTools(v *validator) {
	for _, name := range slices.Sorted(maps.Keys(c.Tool)) {
		if kind := c.Tool[name].Kind; !slices.Contains(toolKinds, kind) {
			v.reportChoice("tool."+name+".kind", toolKinds, kind)
		}
	}
}

// --- positions ---

// locate returns the position of the key at path ("a.b", "a.b[2]"), if it is
// in the file.
func (v *validator) locate(path string) (pos position) {
	if v.positions == nil {
		v.positions = keyPositions(v.data)
	}
	return v.positions[path]
}

// keyPositions maps the dotted path of every key, table header and array
// element in data to its position. go-toml decodes into the struct without
// positions, so validation locates keys with its parser. The data decoded
// already, so a parser error cannot happen; whatever was read is kept.
func keyPositions(data []byte) (positions map[string]position) {
	positions = make(map[string]position)
	var parser unstable.Parser
	parser.Reset(data)
	var table string
	for parser.NextExpression() {
		node := parser.Expression()
		switch node.Kind {
		case unstable.Table:
			table = recordKey(&parser, node, "", positions)
		case unstable.KeyValue:
			recordKeyValue(&parser, node, table, positions)
		}
	}
	return
}

func startOf(parser *unstable.Parser, node *unstable.Node) (pos position) {
	start := parser.Shape(node.Raw).Start
	return position{start.Line, start.Column}
}

// recordKey records the key of a table header or key/value under prefix and
// returns its full dotted path.
func recordKey(parser *unstable.Parser, node *unstable.Node, prefix string, positions map[string]position) (path string) {
	parts := []string{}
	if prefix != "" {
		parts = append(parts, prefix)
	}
	var pos position
	for it := node.Key(); it.Next(); {
		if pos == (position{}) {
			pos = startOf(parser, it.Node())
		}
		parts = append(parts, string(it.Node().Data))
	}
	path = strings.Join(parts, ".")
	positions[path] = pos
	return
}

// recordKeyValue records a key/value, the elements of an array value and the
// keys of an inline table value.
func recordKeyValue(parser *unstable.Parser, node *unstable.Node, prefix string, positions map[string]position) {
	path := recordKey(parser, node, prefix, positions)
	value := node.Value()
	switch value.Kind {
	case unstable.Array:
		i := 0
		for it := value.Children(); it.Next(); i++ {
			positions[fmt.Sprintf("%s[%d]", path, i)] = startOf(parser, it.Node())
		}
	case unstable.InlineTable:
		for it := value.Children(); it.Next(); {
			recordKeyValue(parser, it.Node(), path, positions)
		}
	}
}
