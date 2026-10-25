package image

import (
	"slices"
	"testing"

	"github.com/graf0/afk/config"
)

func TestPluginInstallSteps(t *testing.T) {
	steps, err := pluginInstallSteps(
		config.Agent{Plugins: []string{"b@mine"}, Marketplaces: []string{"o/it's"}},
		[]byte(`{"enabledPlugins": {"a@claude-plugins-official": true, "b@mine": true}}`))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		`claude plugin marketplace add 'anthropics/claude-plugins-official'`,
		`claude plugin marketplace add 'o/it'\''s'`,
		"claude plugin install a@claude-plugins-official --scope user",
		"claude plugin install b@mine --scope user",
	}
	if !slices.Equal(steps, want) {
		t.Errorf("steps = %q, want %q", steps, want)
	}
	if steps, err = pluginInstallSteps(config.Agent{}, nil); err != nil || len(steps) != 0 {
		t.Errorf("no plugins: steps %q, err %v, want none", steps, err)
	}
}
