package image

import (
	"slices"
	"strings"
	"testing"
)

func TestProjectPluginSteps(t *testing.T) {
	steps, err := projectPluginSteps([]byte(`{
		"enabledPlugins": {"b@mine": true, "a@claude-plugins-official": true, "c@mine": true, "off@mine": false},
		"extraKnownMarketplaces": {
			"mine": {"source": {"source": "github", "repo": "o/it's"}},
			"unused": {"source": {"source": "directory", "path": "/x"}}
		},
		"hooks": {}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		`claude plugin marketplace add 'anthropics/claude-plugins-official'`,
		`claude plugin marketplace add 'o/it'\''s'`,
		"claude plugin install a@claude-plugins-official --scope user",
		"claude plugin install b@mine --scope user",
		"claude plugin install c@mine --scope user",
	}
	if !slices.Equal(steps, want) {
		t.Errorf("steps = %q, want %q", steps, want)
	}
}

func TestProjectPluginStepsNone(t *testing.T) {
	for _, settings := range []string{"", `{}`, `{"enabledPlugins": {"a@m": false}}`} {
		if steps, err := projectPluginSteps([]byte(settings)); err != nil || len(steps) != 0 {
			t.Errorf("%q: steps %q, err %v, want none", settings, steps, err)
		}
	}
}

func TestProjectPluginStepsErrors(t *testing.T) {
	for name, tc := range map[string]struct{ settings, want string }{
		"broken":              {`{`, "parsing"},
		"invalid id":          {`{"enabledPlugins": {"a;rm -rf /@m": true}}`, "invalid plugin id"},
		"unknown marketplace": {`{"enabledPlugins": {"a@nowhere": true}}`, "not in extraKnownMarketplaces"},
		"unsupported source":  {`{"enabledPlugins": {"a@m": true}, "extraKnownMarketplaces": {"m": {"source": {"source": "directory", "path": "/x"}}}}`, "only github"},
		"option":              {`{"enabledPlugins": {"a@m": true}, "extraKnownMarketplaces": {"m": {"source": {"source": "github", "repo": "--evil"}}}}`, "looks like an option"},
	} {
		if _, err := projectPluginSteps([]byte(tc.settings)); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want %q", name, err, tc.want)
		}
	}
}
