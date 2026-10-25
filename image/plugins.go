package image

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/graf0/afk/agent"
	"github.com/graf0/afk/plugin"
)

// officialMarketplace is not added by Claude Code on its own in a fresh
// home; its source is used when the settings do not name one.
const (
	officialMarketplace = "claude-plugins-official"
	officialSource      = "anthropics/claude-plugins-official"
)

// marketplaceSource is an extraKnownMarketplaces entry of settings.json.
type marketplaceSource struct {
	Source struct {
		Kind string `json:"source"`
		Repo string `json:"repo"` // github
		URL  string `json:"url"`  // git
	} `json:"source"`
}

// projectPluginSteps are the guest commands that install the plugins
// enabled in the Project's .claude/settings.json (user scope, so they are
// on in every session in the guest), adding the marketplaces they come from
// first. Empty settings give no steps. They run in the build, through the
// Egress proxy, with no secrets.
func projectPluginSteps(settings []byte) (steps []string, err error) {
	if len(settings) == 0 {
		return
	}
	var parsed struct {
		EnabledPlugins         map[string]bool              `json:"enabledPlugins"`
		ExtraKnownMarketplaces map[string]marketplaceSource `json:"extraKnownMarketplaces"`
	}
	if err = json.Unmarshal(settings, &parsed); err != nil {
		err = fmt.Errorf("parsing .claude/settings.json: %w", err)
		return
	}
	var installs, marketplaces []string
	added := map[string]bool{}
	for _, id := range slices.Sorted(maps.Keys(parsed.EnabledPlugins)) {
		if !parsed.EnabledPlugins[id] {
			continue
		}
		if !plugin.ValidID(id) {
			err = fmt.Errorf(".claude/settings.json: invalid plugin id %q: want <name>@<marketplace>", id)
			return
		}
		installs = append(installs, "claude plugin install "+id+" --scope user")
		_, market, _ := strings.Cut(id, "@")
		if added[market] {
			continue
		}
		source, known := parsed.ExtraKnownMarketplaces[market]
		if !known && market == officialMarketplace {
			source.Source.Kind, source.Source.Repo = "github", officialSource
		} else if !known {
			err = fmt.Errorf(".claude/settings.json: plugin %s: marketplace %q is not in extraKnownMarketplaces", id, market)
			return
		}
		var location string
		switch source.Source.Kind {
		case "github":
			location = source.Source.Repo
		case "git":
			location = source.Source.URL
		}
		if strings.HasPrefix(location, "-") {
			err = fmt.Errorf(".claude/settings.json: marketplace %q: source %q looks like an option", market, location)
			return
		}
		if location == "" {
			err = fmt.Errorf(".claude/settings.json: marketplace %q: only github (repo) and git (url) sources are supported", market)
			return
		}
		added[market] = true
		marketplaces = append(marketplaces, "claude plugin marketplace add "+agent.ShellQuote(location))
	}
	steps = append(marketplaces, installs...)
	return
}
