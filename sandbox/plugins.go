package sandbox

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/graf0/afk/stack"
)

// pluginView is what the Sandbox gets of the host's Claude plugins: only the
// plugins in ids ("<name>@<marketplace>", the effective plugins of the
// Project, ADR-0009). Claude finds plugins through two metadata files of
// ~/.claude/plugins, which are rewritten here to list only those, and through
// the paths in them, so the install directory of each plugin and the directory
// of its marketplace are mounted read-only at the same paths. files go to
// ctxDir (the caller writes them), dataDir is the plugins' own data
// directory: a writable, per-Run one, not the host's. Without ids there is
// nothing (nil).
func pluginView(home, ctxDir, dataDir string, ids []string) (files map[string][]byte, mounts []stack.Mount, err error) {
	if len(ids) == 0 {
		return
	}
	plugins := filepath.Join(home, ".claude", "plugins")
	var installed struct {
		Version json.RawMessage              `json:"version"`
		Plugins map[string][]json.RawMessage `json:"plugins"`
	}
	if err = readJSON(filepath.Join(plugins, "installed_plugins.json"), &installed); err != nil {
		return
	}
	var known map[string]json.RawMessage
	if err = readJSON(filepath.Join(plugins, "known_marketplaces.json"), &known); err != nil {
		return
	}
	view := struct {
		Version json.RawMessage              `json:"version"`
		Plugins map[string][]json.RawMessage `json:"plugins"`
	}{Version: installed.Version, Plugins: map[string][]json.RawMessage{}}
	viewKnown := map[string]json.RawMessage{}
	seen := map[string]bool{}
	ro := func(path string) {
		if !seen[path] {
			seen[path] = true
			mounts = append(mounts, stack.Mount{Source: path, Target: path, ReadOnly: true, Dir: true})
		}
	}
	for _, id := range ids {
		entry, installPath, found := userInstall(installed.Plugins[id])
		if !found {
			err = fmt.Errorf("plugin %s is not installed for the user on this host: run `claude plugin install %s`", id, id)
			return
		}
		if !within(filepath.Join(plugins, "cache"), installPath) {
			err = fmt.Errorf("plugin %s: install path %s is not under %s", id, installPath, filepath.Join(plugins, "cache"))
			return
		}
		view.Plugins[id] = []json.RawMessage{entry}
		ro(installPath)
		_, marketplace, _ := strings.Cut(id, "@")
		raw, ok := known[marketplace]
		if !ok {
			err = fmt.Errorf("plugin %s: marketplace %q is not known on this host", id, marketplace)
			return
		}
		var market struct {
			InstallLocation string `json:"installLocation"`
		}
		if err = json.Unmarshal(raw, &market); err != nil {
			err = fmt.Errorf("marketplace %q: %w", marketplace, err)
			return
		}
		if !within(filepath.Join(plugins, "marketplaces"), market.InstallLocation) {
			err = fmt.Errorf("marketplace %q: location %s is not under %s", marketplace, market.InstallLocation, filepath.Join(plugins, "marketplaces"))
			return
		}
		viewKnown[marketplace] = raw
		ro(market.InstallLocation)
	}
	files = map[string][]byte{}
	if files["installed_plugins.json"], err = json.MarshalIndent(view, "", "  "); err != nil {
		return
	}
	if files["known_marketplaces.json"], err = json.MarshalIndent(viewKnown, "", "  "); err != nil {
		return
	}
	for name := range files {
		mounts = append(mounts, stack.Mount{Source: filepath.Join(ctxDir, name), Target: filepath.Join(plugins, name), ReadOnly: true})
	}
	mounts = append(mounts, stack.Mount{Source: dataDir, Target: filepath.Join(plugins, "data"), Dir: true})
	return
}

// userInstall is the user-scope install among the entries of a plugin.
func userInstall(entries []json.RawMessage) (entry json.RawMessage, installPath string, found bool) {
	for _, raw := range entries {
		var parsed struct {
			Scope       string `json:"scope"`
			InstallPath string `json:"installPath"`
		}
		if json.Unmarshal(raw, &parsed) == nil && parsed.Scope == "user" && parsed.InstallPath != "" {
			return raw, parsed.InstallPath, true
		}
	}
	return
}

func readJSON(path string, into any) (err error) {
	data, err := os.ReadFile(path)
	if err != nil {
		err = fmt.Errorf("reading the host's plugins: %w", err)
		return
	}
	if err = json.Unmarshal(data, into); err != nil {
		err = fmt.Errorf("parsing %s: %w", path, err)
	}
	return
}

// within reports whether path is dir or inside it, lexically.
func within(dir, path string) (ok bool) {
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && filepath.IsAbs(path)
}

// claudeSettings is the settings.json of the Agent: the Project's from the
// base branch, with every effective plugin enabled (ADR-0009).
func claudeSettings(project []byte, plugins []string) (data []byte, err error) {
	settings := map[string]json.RawMessage{}
	if len(project) > 0 {
		if err = json.Unmarshal(project, &settings); err != nil {
			err = fmt.Errorf("parsing .claude/settings.json: %w", err)
			return
		}
	}
	enabled := map[string]bool{}
	if raw, ok := settings["enabledPlugins"]; ok {
		if err = json.Unmarshal(raw, &enabled); err != nil {
			err = fmt.Errorf("parsing enabledPlugins in .claude/settings.json: %w", err)
			return
		}
	}
	for _, id := range plugins {
		enabled[id] = true
	}
	if len(enabled) > 0 {
		if settings["enabledPlugins"], err = json.Marshal(enabled); err != nil {
			return
		}
	}
	data, err = json.MarshalIndent(settings, "", "  ")
	return
}
