package image

import (
	"github.com/graf0/afk/agent"
	"github.com/graf0/afk/config"
)

// pluginInstallSteps are the guest commands that add every marketplace and install
// every plugin of config.EffectivePlugins (user scope, so they are on in every
// session in the guest). They run in the build, through the Egress proxy,
// with no secrets.
func pluginInstallSteps(cfg config.Agent, settings []byte) (steps []string, err error) {
	plugins, marketplaces, err := config.EffectivePlugins(cfg, settings)
	if err != nil {
		return
	}
	for _, source := range marketplaces {
		steps = append(steps, "claude plugin marketplace add "+agent.ShellQuote(source))
	}
	for _, id := range plugins {
		steps = append(steps, "claude plugin install "+id+" --scope user")
	}
	return
}
