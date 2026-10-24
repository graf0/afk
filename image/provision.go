package image

import (
	"bytes"
	_ "embed"
	"fmt"
	"text/template"
)

//go:embed template/debian13.pkr.hcl
var debian13Template string

//go:embed template/user-data.tmpl
var userDataTemplate string

//go:embed template/meta-data
var metaData string

// guestProxy is where the build tunnel appears inside the guest.
const guestProxy = "http://127.0.0.1:3128"

// afkSteps always run first: what afk needs in every image (sshd, git, Claude Code),
// independent of the project's provision steps. The guest reaches the internet
// only through the Egress proxy tunnel.
var afkSteps = []string{
	`printf 'Acquire::http::Proxy "` + guestProxy + `";\nAcquire::https::Proxy "` + guestProxy + `";\n' | sudo tee /etc/apt/apt.conf.d/90afk-proxy >/dev/null`,
	`sudo DEBIAN_FRONTEND=noninteractive apt-get update`,
	`sudo DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends openssh-server git curl ca-certificates`,
	`curl -fsSL https://claude.ai/install.sh | bash`,
	`sudo ln -sf "$HOME/.local/bin/claude" /usr/local/bin/claude`,
}

// finalSteps always run last (docs/images.md): without them clones get no
// address or the same machine-id (and so the same DHCP client-id).
var finalSteps = []string{
	`sudo rm -f /etc/apt/apt.conf.d/90afk-proxy /etc/netplan/50-cloud-init.yaml`,
	`printf 'network:\n  version: 2\n  ethernets:\n    all:\n      match: {name: "en*"}\n      dhcp4: true\n' | sudo tee /etc/netplan/90-afk.yaml >/dev/null`,
	`sudo chmod 600 /etc/netplan/90-afk.yaml`,
	`sudo touch /etc/cloud/cloud-init.disabled`,
	`sudo truncate -s 0 /etc/machine-id`,
}

// provisionSteps returns the full guest script: afk's steps, the project's
// steps ([image].provision), then the mandatory final steps.
func provisionSteps(project []string) (steps []string) {
	steps = make([]string, 0, len(afkSteps)+len(project)+len(finalSteps))
	steps = append(steps, afkSteps...)
	steps = append(steps, project...)
	steps = append(steps, finalSteps...)
	return
}

// renderSeed returns the cloud-init NoCloud files (file name to content):
// user "agent" with passwordless sudo and publicKey as its only login.
func renderSeed(publicKey string) (files map[string]string, err error) {
	tmpl, err := template.New("user-data").Parse(userDataTemplate)
	if err != nil {
		err = fmt.Errorf("parsing user-data template: %w", err)
		return
	}
	var userData bytes.Buffer
	if err = tmpl.Execute(&userData, struct{ PublicKey string }{publicKey}); err != nil {
		err = fmt.Errorf("rendering user-data: %w", err)
		return
	}
	files = map[string]string{"user-data": userData.String(), "meta-data": metaData}
	return
}
