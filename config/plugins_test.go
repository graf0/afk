package config

import (
	"slices"
	"strings"
	"testing"
)

func TestEffectivePlugins(t *testing.T) {
	cfg := Agent{
		Plugins:      []string{"z@mine", "b@mine"},
		Marketplaces: []string{"o/mine", "https://git.example/x.git"},
	}
	plugins, marketplaces, err := EffectivePlugins(cfg, []byte(`{
		"enabledPlugins": {"b@mine": true, "a@claude-plugins-official": true, "off@mine": false},
		"extraKnownMarketplaces": {
			"mine": {"source": {"source": "github", "repo": "o/mine"}},
			"other": {"source": {"source": "git", "url": "https://git.example/y.git"}},
			"unused": {"source": {"source": "directory", "path": "/x"}}
		},
		"hooks": {}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"a@claude-plugins-official", "b@mine", "z@mine"}; !slices.Equal(plugins, want) {
		t.Errorf("plugins = %q, want %q", plugins, want)
	}
	want := []string{"anthropics/claude-plugins-official", "https://git.example/x.git", "https://git.example/y.git", "o/mine"}
	if !slices.Equal(marketplaces, want) {
		t.Errorf("marketplaces = %q, want %q", marketplaces, want)
	}
}

func TestEffectivePluginsNone(t *testing.T) {
	for _, settings := range []string{"", `{}`, `{"enabledPlugins": {"a@m": false}}`} {
		plugins, marketplaces, err := EffectivePlugins(Agent{}, []byte(settings))
		if err != nil || len(plugins) != 0 || len(marketplaces) != 0 {
			t.Errorf("%q: %q, %q, %v, want none", settings, plugins, marketplaces, err)
		}
	}
}

func TestEffectivePluginsErrors(t *testing.T) {
	for name, tc := range map[string]struct {
		cfg      Agent
		settings string
		want     string
	}{
		"broken settings":    {settings: `{`, want: "parsing"},
		"invalid id":         {cfg: Agent{Plugins: []string{"a;rm -rf /@m"}}, want: "invalid plugin id"},
		"invalid id (no @)":  {settings: `{"enabledPlugins": {"a": true}}`, want: "invalid plugin id"},
		"option in config":   {cfg: Agent{Marketplaces: []string{"--evil"}}, want: "looks like an option"},
		"option in settings": {settings: `{"extraKnownMarketplaces": {"m": {"source": {"source": "github", "repo": "--evil"}}}}`, want: "looks like an option"},
	} {
		if _, _, err := EffectivePlugins(tc.cfg, []byte(tc.settings)); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want %q", name, err, tc.want)
		}
	}
}
