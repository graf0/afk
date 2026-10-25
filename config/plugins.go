package config

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// officialSource is the marketplace of Anthropic's plugins; a fresh Claude
// Code home does not know it, so it is always added when there is a plugin.
const officialSource = "anthropics/claude-plugins-official"

// pluginIDPattern is <name>@<marketplace>, safe as a shell word.
var pluginIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*@[A-Za-z0-9][A-Za-z0-9._-]*$`)

// EffectivePlugins are the plugin ids ("<name>@<marketplace>") of
// [agent].plugins and the enabled ones of the Project's .claude/settings.json
// (nil: none), deduplicated and sorted, and the marketplace sources they may
// come from: [agent].marketplaces and extraKnownMarketplaces (github repo or
// git url), plus the official one when there is any plugin (ADR-0009).
// Beyond the format, whether they exist is for `claude` to say at build time.
func EffectivePlugins(cfg Agent, settings []byte) (plugins, marketplaces []string, err error) {
	var parsed struct {
		EnabledPlugins         map[string]bool `json:"enabledPlugins"`
		ExtraKnownMarketplaces map[string]struct {
			Source struct {
				Repo string `json:"repo"` // source: github
				URL  string `json:"url"`  // source: git
			} `json:"source"`
		} `json:"extraKnownMarketplaces"`
	}
	if len(settings) > 0 {
		if err = json.Unmarshal(settings, &parsed); err != nil {
			err = fmt.Errorf("parsing .claude/settings.json: %w", err)
			return
		}
	}
	plugins = slices.Clone(cfg.Plugins)
	for id, enabled := range parsed.EnabledPlugins {
		if enabled {
			plugins = append(plugins, id)
		}
	}
	marketplaces = slices.Clone(cfg.Marketplaces)
	for _, market := range parsed.ExtraKnownMarketplaces {
		marketplaces = append(marketplaces, market.Source.Repo, market.Source.URL)
	}
	marketplaces = slices.DeleteFunc(marketplaces, func(source string) bool { return source == "" })
	for _, id := range plugins {
		if !pluginIDPattern.MatchString(id) {
			err = fmt.Errorf("invalid plugin id %q: want <name>@<marketplace>", id)
			return
		}
	}
	for _, source := range marketplaces {
		if strings.HasPrefix(source, "-") {
			err = fmt.Errorf("marketplace source %q looks like an option", source)
			return
		}
	}
	slices.Sort(plugins)
	plugins = slices.Compact(plugins)
	if len(plugins) > 0 {
		marketplaces = append(marketplaces, officialSource)
	}
	slices.Sort(marketplaces)
	marketplaces = slices.Compact(marketplaces)
	return
}
