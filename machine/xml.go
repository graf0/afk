package machine

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"net/netip"
	"strings"
	"text/template"
)

const (
	// metadataNS is the namespace of afk's element in the domain <metadata>.
	metadataNS = "https://github.com/graf0/afk/machine"

	// subnets is the address space of Sandbox VM networks, one /24 each:
	// 10.99.<n>.0/24 on bridge afkbr<n>.
	subnets = 256
)

var (
	xmlFuncs = template.FuncMap{"x": escapeXML}

	// domainTemplate follows docs/libvirt.md. The systemd credential in SMBIOS
	// type 11 installs the machine's sshd host key before sshd starts.
	domainTemplate = template.Must(template.New("domain").Funcs(xmlFuncs).Parse(`<domain type='kvm'>
  <name>{{x .Name}}</name>
  <metadata>
    <afk:machine xmlns:afk='` + metadataNS + `'>
      <project>{{x .Meta.Project}}</project>
      <kind>{{x .Meta.Kind}}</kind>
      <id>{{x .Meta.ID}}</id>
      <addr>{{x .Meta.Addr}}</addr>
      <hostkey>{{x .Meta.HostKey}}</hostkey>
    </afk:machine>
  </metadata>
  <memory unit='GiB'>2</memory>
  <vcpu>2</vcpu>
  <sysinfo type='smbios'>
    <oemStrings>
      <entry>io.systemd.credential.binary:tmpfiles.extra={{x .Credential}}</entry>
    </oemStrings>
  </sysinfo>
  <os>
    <type arch='x86_64' machine='q35'>hvm</type>
    <boot dev='hd'/>
    <smbios mode='sysinfo'/>
  </os>
  <features><acpi/><apic/></features>
  <cpu mode='host-passthrough'/>
  <clock offset='utc'/>
  <devices>
    <disk type='file' device='disk'>
      <driver name='qemu' type='qcow2' discard='unmap'/>
      <source file='{{x .Disk}}'><seclabel model='dac' relabel='no'/></source>
      <target dev='vda' bus='virtio'/>
    </disk>
    <interface type='network'>
      <source network='{{x .Name}}'/>
      <mac address='{{.Net.MAC}}'/>
      <model type='virtio'/>
      <filterref filter='{{x .Name}}'/>
    </interface>
    <memballoon model='virtio'><stats period='2'/></memballoon>
    <rng model='virtio'><backend model='random'>/dev/urandom</backend></rng>
    <channel type='unix'><target type='virtio' name='org.qemu.guest_agent.0'/></channel>
    <input type='tablet' bus='usb'/>
    <input type='keyboard' bus='usb'/>
    <graphics type='vnc' port='-1' listen='127.0.0.1'/>
    <video><model type='virtio'/></video>
  </devices>
</domain>
`))

	// networkTemplate follows docs/isolation.md: no <forward> (no NAT), no DNS,
	// the machine's address reserved by MAC.
	networkTemplate = template.Must(template.New("network").Funcs(xmlFuncs).Parse(`<network>
  <name>{{x .Name}}</name>
  <bridge name='{{.Net.Bridge}}' stp='off'/>
  <dns enable='no'/>
  <ip address='{{.Net.Gateway}}' netmask='255.255.255.0'>
    <dhcp>
      <range start='{{.Net.Host 100}}' end='{{.Net.Host 199}}'/>
      <host mac='{{.Net.MAC}}' ip='{{.Net.Addr}}'/>
    </dhcp>
  </ip>
</network>
`))

	// filterTemplate is the whitelist from docs/isolation.md: new connections
	// from the guest only to other machines of its subnet (never the gateway,
	// which is the host), plus DHCP.
	filterTemplate = template.Must(template.New("filter").Funcs(xmlFuncs).Parse(`<filter name='{{x .Name}}' chain='root'>
  <rule action='accept' direction='out' priority='90'><udp srcportstart='68' dstportstart='67'/></rule>
  <rule action='drop' direction='out' priority='100'><all dstipaddr='{{.Net.Gateway}}' state='NEW'/></rule>
  <rule action='accept' direction='out' priority='200'><all dstipaddr='{{.Net.Host 0}}' dstipmask='24' state='NEW'/></rule>
  <rule action='drop' direction='out' priority='300'><all state='NEW'/></rule>
  <rule action='drop' direction='out' priority='310'><all-ipv6 state='NEW'/></rule>
  <rule action='accept' direction='inout' priority='500'><all/></rule>
</filter>
`))
)

// subnet is the network of one machine: 10.99.<n>.0/24.
type subnet int

// metadata is afk's element in the domain <metadata>: what the machine is,
// so afk needs no database of its own.
type metadata struct {
	Project string `xml:"project"`
	Kind    Kind   `xml:"kind"`
	ID      string `xml:"id"`
	Addr    string `xml:"addr"`    // guest IPv4 address
	HostKey string `xml:"hostkey"` // sshd host key, authorized_keys format
}

// definition is the input of the XML templates.
type definition struct {
	Name       string // domain, network and nwfilter name
	Disk       string
	Credential string // base64 systemd tmpfiles.extra credential
	Net        subnet
	Meta       metadata
}

// --- subnet ---

// Host returns the address of host i in the subnet.
func (s subnet) Host(i int) (addr string) { return fmt.Sprintf("10.99.%d.%d", int(s), i) }

// Gateway is the host's address on the bridge.
func (s subnet) Gateway() (addr string) { return s.Host(1) }

// Addr is the machine's address, reserved by MAC.
func (s subnet) Addr() (addr string) { return s.Host(10) }

// MAC is the machine's MAC address.
func (s subnet) MAC() (mac string) { return fmt.Sprintf("52:54:00:af:%02x:0a", int(s)) }

// Bridge is the bridge name; deriving it from the subnet makes libvirt refuse
// a second network on the same subnet at define time ("bridge name already in
// use"), so concurrent `up`s cannot share one.
func (s subnet) Bridge() (name string) { return fmt.Sprintf("afkbr%d", int(s)) }

// parseUsedSubnets returns the subnets taken by networks (libvirt network
// XML), by address range or by bridge name.
func parseUsedSubnets(networks []string) (used map[subnet]bool, err error) {
	used = make(map[subnet]bool)
	for _, data := range networks {
		var network struct {
			Bridge struct {
				Name string `xml:"name,attr"`
			} `xml:"bridge"`
			IPs []struct {
				Address string `xml:"address,attr"`
			} `xml:"ip"`
		}
		if err = xml.Unmarshal([]byte(data), &network); err != nil {
			err = fmt.Errorf("parsing network XML: %w", err)
			return
		}
		var n int
		if _, scanErr := fmt.Sscanf(network.Bridge.Name, "afkbr%d", &n); scanErr == nil {
			used[subnet(n)] = true
		}
		for _, ip := range network.IPs {
			addr, parseErr := netip.ParseAddr(ip.Address)
			if parseErr == nil && addr.Is4() && addr.As4()[0] == 10 && addr.As4()[1] == 99 {
				used[subnet(addr.As4()[2])] = true
			}
		}
	}
	return
}

// freeSubnet returns the lowest subnet that is not used.
func freeSubnet(used map[subnet]bool) (free subnet, err error) {
	for s := range subnet(subnets) {
		if !used[s] {
			free = s
			return
		}
	}
	err = fmt.Errorf("all %d subnets of 10.99.0.0/16 are in use", subnets)
	return
}

// --- rendering ---

func render(tmpl *template.Template, def definition) (data string, err error) {
	var buf bytes.Buffer
	if err = tmpl.Execute(&buf, def); err != nil {
		err = fmt.Errorf("rendering %s XML: %w", tmpl.Name(), err)
		return
	}
	data = buf.String()
	return
}

func escapeXML(value any) (escaped string) {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(fmt.Sprint(value)))
	return b.String()
}

// parseMetadata decodes afk's metadata element as returned by libvirt.
func parseMetadata(data string) (meta metadata, err error) {
	if err = xml.Unmarshal([]byte(data), &meta); err != nil {
		err = fmt.Errorf("parsing machine metadata: %w", err)
	}
	return
}
