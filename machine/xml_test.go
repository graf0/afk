package machine

import (
	"encoding/base64"
	"encoding/xml"
	"strings"
	"testing"
)

func testDefinition() (def definition) {
	return definition{
		Name:       "afk-run-abc",
		Disk:       "/var/lib/afk/runs/abc/disk.qcow2",
		Credential: base64.StdEncoding.EncodeToString([]byte("f+~ /x 0600 root root - eA==\n")),
		MemoryMiB:  4096,
		CPUs:       6,
		Net:        5,
		Meta: metadata{
			Project: "graf0-dfmx",
			Kind:    KindRun,
			ID:      "abc",
			Addr:    "10.99.5.10",
			HostKey: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIK",
		},
	}
}

func mustRender(t *testing.T, tmpl string, def definition) (data string) {
	t.Helper()
	templates := map[string]func() (string, error){
		"domain":  func() (string, error) { return render(domainTemplate, def) },
		"network": func() (string, error) { return render(networkTemplate, def) },
		"filter":  func() (string, error) { return render(filterTemplate, def) },
	}
	data, err := templates[tmpl]()
	if err != nil {
		t.Fatal(err)
	}
	var wellFormed struct{}
	if err = xml.Unmarshal([]byte(data), &wellFormed); err != nil {
		t.Fatalf("%s XML is not well-formed: %v\n%s", tmpl, err, data)
	}
	return
}

func TestDomainXML(t *testing.T) {
	def := testDefinition()
	data := mustRender(t, "domain", def)

	var domain struct {
		Name   string `xml:"name"`
		Memory struct {
			Unit  string `xml:"unit,attr"`
			Value int    `xml:",chardata"`
		} `xml:"memory"`
		VCPU     int `xml:"vcpu"`
		Metadata struct {
			Inner string `xml:",innerxml"`
		} `xml:"metadata"`
		Seclabel []struct{} `xml:"seclabel"`
		Sysinfo  struct {
			Type    string   `xml:"type,attr"`
			Entries []string `xml:"oemStrings>entry"`
		} `xml:"sysinfo"`
		OS struct {
			Smbios struct {
				Mode string `xml:"mode,attr"`
			} `xml:"smbios"`
		} `xml:"os"`
		Disk struct {
			Source struct {
				File     string `xml:"file,attr"`
				Seclabel struct {
					Model   string `xml:"model,attr"`
					Relabel string `xml:"relabel,attr"`
				} `xml:"seclabel"`
			} `xml:"source"`
		} `xml:"devices>disk"`
		Interface struct {
			Source struct {
				Network string `xml:"network,attr"`
			} `xml:"source"`
			MAC struct {
				Address string `xml:"address,attr"`
			} `xml:"mac"`
			Filter struct {
				Filter string `xml:"filter,attr"`
			} `xml:"filterref"`
		} `xml:"devices>interface"`
	}
	if err := xml.Unmarshal([]byte(data), &domain); err != nil {
		t.Fatal(err)
	}
	if domain.Name != def.Name {
		t.Errorf("name = %q", domain.Name)
	}
	if domain.Memory.Unit != "MiB" || domain.Memory.Value != def.MemoryMiB || domain.VCPU != def.CPUs {
		t.Errorf("memory = %d %s, vcpu = %d; want %d MiB and %d vCPUs", domain.Memory.Value, domain.Memory.Unit, domain.VCPU, def.MemoryMiB, def.CPUs)
	}
	if len(domain.Seclabel) != 0 {
		t.Error("seclabel on the domain breaks swtpm; it belongs on the disk source")
	}
	if s := domain.Disk.Source; s.File != def.Disk || s.Seclabel.Model != "dac" || s.Seclabel.Relabel != "no" {
		t.Errorf("disk source = %+v, want %s with dac relabel=no", s, def.Disk)
	}
	if i := domain.Interface; i.Source.Network != def.Name || i.Filter.Filter != def.Name || i.MAC.Address != "52:54:00:af:05:0a" {
		t.Errorf("interface = %+v", i)
	}
	if domain.Sysinfo.Type != "smbios" || domain.OS.Smbios.Mode != "sysinfo" {
		t.Errorf("sysinfo type %q, os smbios mode %q: the credential would not reach the guest", domain.Sysinfo.Type, domain.OS.Smbios.Mode)
	}
	if want := []string{"io.systemd.credential.binary:tmpfiles.extra=" + def.Credential}; len(domain.Sysinfo.Entries) != 1 || domain.Sysinfo.Entries[0] != want[0] {
		t.Errorf("oemStrings = %q, want %q", domain.Sysinfo.Entries, want)
	}

	// libvirt returns the metadata element on its own; it must round-trip.
	if !strings.Contains(domain.Metadata.Inner, "xmlns:afk='"+metadataNS+"'") {
		t.Errorf("metadata lacks afk namespace:\n%s", domain.Metadata.Inner)
	}
	meta, err := parseMetadata(domain.Metadata.Inner)
	if err != nil {
		t.Fatal(err)
	}
	if meta != def.Meta {
		t.Errorf("metadata = %+v, want %+v", meta, def.Meta)
	}
}

func TestDomainXMLEscapes(t *testing.T) {
	def := testDefinition()
	def.Disk = "/var/lib/afk/runs/a'b<c>/disk.qcow2"
	def.Meta.Project = "p&q"
	data := mustRender(t, "domain", def)
	if strings.Contains(data, "a'b<c>") {
		t.Errorf("path not escaped:\n%s", data)
	}
}

func TestNetworkXML(t *testing.T) {
	data := mustRender(t, "network", testDefinition())
	want := `<network>
  <name>afk-run-abc</name>
  <bridge name='afkbr5' stp='off'/>
  <dns enable='no'/>
  <ip address='10.99.5.1' netmask='255.255.255.0'>
    <dhcp>
      <range start='10.99.5.100' end='10.99.5.199'/>
      <host mac='52:54:00:af:05:0a' ip='10.99.5.10'/>
    </dhcp>
  </ip>
</network>
`
	if data != want {
		t.Errorf("network XML:\n%s\nwant:\n%s", data, want)
	}
	if strings.Contains(data, "<forward") {
		t.Error("network must not forward (no NAT)")
	}
}

// The whitelist from docs/isolation.md, for subnet 5.
func TestFilterXML(t *testing.T) {
	data := mustRender(t, "filter", testDefinition())
	want := `<filter name='afk-run-abc' chain='root'>
  <rule action='accept' direction='out' priority='90'><udp srcportstart='68' dstportstart='67'/></rule>
  <rule action='drop' direction='out' priority='100'><all dstipaddr='10.99.5.1' state='NEW'/></rule>
  <rule action='accept' direction='out' priority='200'><all dstipaddr='10.99.5.0' dstipmask='24' state='NEW'/></rule>
  <rule action='drop' direction='out' priority='300'><all state='NEW'/></rule>
  <rule action='drop' direction='out' priority='310'><all-ipv6 state='NEW'/></rule>
  <rule action='accept' direction='inout' priority='500'><all/></rule>
</filter>
`
	if data != want {
		t.Errorf("filter XML:\n%s\nwant:\n%s", data, want)
	}
}

func TestSubnetAllocation(t *testing.T) {
	networks := []string{
		`<network><name>default</name><bridge name='virbr0'/><ip address='192.168.122.1' netmask='255.255.255.0'/></network>`,
		`<network><name>afk-run-a</name><bridge name='afkbr0'/><ip address='10.99.0.1' netmask='255.255.255.0'/></network>`,
		`<network><name>other</name><bridge name='br9'/><ip address='10.99.1.1' netmask='255.255.255.0'/></network>`, // address taken
		`<network><name>afk-run-b</name><bridge name='afkbr2'/></network>`,                                           // bridge taken
		`<network><name>isolated</name></network>`,
	}
	used, err := parseUsedSubnets(networks)
	if err != nil {
		t.Fatal(err)
	}
	got, err := freeSubnet(used)
	if err != nil || got != 3 {
		t.Errorf("freeSubnet = %d, %v; want 3", got, err)
	}

	if _, err = parseUsedSubnets([]string{"<network"}); err == nil {
		t.Error("broken XML accepted")
	}
	all := make(map[subnet]bool)
	for s := range subnet(subnets) {
		all[s] = true
	}
	if _, err = freeSubnet(all); err == nil {
		t.Error("freeSubnet with all subnets used succeeded")
	}
}
