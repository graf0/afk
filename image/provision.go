package image

import (
	"bytes"
	_ "embed"
	"fmt"
	"maps"
	"slices"
	"text/template"

	"github.com/graf0/afk/config"
)

// guestProxy is where the build tunnel appears inside the guest.
const guestProxy = "http://127.0.0.1:3128"

//go:embed template/debian13.pkr.hcl
var debian13HCL string

//go:embed template/user-data.tmpl
var userDataTemplate string

//go:embed template/meta-data
var metaData string

// templates are the supported [machine.<name>].template values.
var templates = map[string]imageTemplate{
	"debian13": {
		Name:        "debian13",
		ImageURL:    "https://cloud.debian.org/images/cloud/trixie/latest/debian-13-genericcloud-amd64.qcow2",
		ChecksumURL: "file:https://cloud.debian.org/images/cloud/trixie/latest/SHA512SUMS",
		HCL:         debian13HCL,
		DiskName:    "debian13.qcow2",
	},
}

// initialSteps always run first: what afk needs in every image (sshd, git, Claude Code),
// independent of the machine's build steps. The guest reaches the internet
// only through the Egress proxy tunnel.
var initialSteps = []string{
	`printf 'Acquire::http::Proxy "` + guestProxy + `";\nAcquire::https::Proxy "` + guestProxy + `";\n' | sudo tee /etc/apt/apt.conf.d/90afk-proxy >/dev/null`,
	`sudo DEBIAN_FRONTEND=noninteractive apt-get update`,
	`sudo DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends openssh-server git curl ca-certificates`,
	`curl -fsSL https://claude.ai/install.sh | bash`,
	`sudo ln -sf "$HOME/.local/bin/claude" /usr/local/bin/claude`,
}

// finalSteps run in their own, last provisioner (docs/images.md), so nothing in
// [machine.<name>].build (`exit`, `exec`) can skip them: without them clones get no
// address or the same machine-id (and so the same DHCP client-id).
var finalSteps = []string{
	`sudo rm -f /etc/apt/apt.conf.d/90afk-proxy /etc/netplan/50-cloud-init.yaml`,
	`printf 'network:\n  version: 2\n  ethernets:\n    all:\n      match: {name: "en*"}\n      dhcp4: true\n' | sudo tee /etc/netplan/90-afk.yaml >/dev/null`,
	`sudo chmod 600 /etc/netplan/90-afk.yaml`,
	`sudo touch /etc/cloud/cloud-init.disabled`,
	`sudo truncate -s 0 /etc/machine-id`,
}

// imageTemplate is a supported Base image template: where its source image
// comes from and the embedded Packer template that builds it.
type imageTemplate struct {
	Name        string
	ImageURL    string // source cloud image
	ChecksumURL string // Packer iso_checksum for ImageURL
	HCL         string // embedded Packer template
	DiskName    string // file name of the built disk
}

// hclFile is the file name the template is written under.
func (t imageTemplate) hclFile() (name string) { return t.Name + ".pkr.hcl" }

// provisionSteps splits the guest script in two provisioners: afk's steps,
// the Project's Claude Code plugins (pluginSteps), the machine's own
// ([machine.<name>].build), the build of its Tools (alphabetically by name),
// and the mandatory final steps. A Tool's services are started by whatever
// its build installs (systemd units of apt packages start at boot); afk adds
// no service manager.
func provisionSteps(pluginSteps, machineBuild []string, tools map[string]config.Tool) (provision, final []string) {
	provision = make([]string, 0, len(initialSteps)+len(pluginSteps)+len(machineBuild))
	provision = append(provision, initialSteps...)
	provision = append(provision, pluginSteps...)
	provision = append(provision, machineBuild...)
	for _, name := range slices.Sorted(maps.Keys(tools)) {
		provision = append(provision, tools[name].Build...)
	}
	final = append([]string(nil), finalSteps...)
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
