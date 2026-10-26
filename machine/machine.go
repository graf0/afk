// Package machine runs Sandbox VMs through libvirt (docs/libvirt.md): a qcow2
// overlay on a Base image, an isolated network with a whitelist nwfilter
// (ADR-0001) and ssh pinned to a host key generated per machine.
package machine

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/digitalocean/go-libvirt"
	"github.com/digitalocean/go-libvirt/socket/dialers"
	"golang.org/x/crypto/ssh"

	"github.com/graf0/afk/image"
)

const (
	// DefaultRoot holds machine directories: runs/<id>, chats/<id>
	// (docs/architecture.md).
	DefaultRoot = "/var/lib/afk"

	namePrefix = "afk-"
	diskName   = "disk.qcow2"
	diskSize   = "64G"

	// defineAttempts bounds retries when a concurrent `up` takes the subnet.
	defineAttempts = 5
)

var idPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)

// --- naming ---

// Kind says what a machine is for: a Run or a Chat.
type Kind string

// Kinds of machines.
const (
	KindRun  Kind = "run"
	KindChat Kind = "chat"
)

// Name returns the libvirt name of the machine of kind with id.
func Name(kind Kind, id string) (name string, err error) {
	if kind != KindRun && kind != KindChat {
		err = fmt.Errorf("invalid machine kind %q", kind)
		return
	}
	if !idPattern.MatchString(id) {
		err = fmt.Errorf("invalid machine id %q: want %s", id, idPattern)
		return
	}
	name = namePrefix + string(kind) + "-" + id
	return
}

// parseName is the inverse of Name.
func parseName(name string) (kind Kind, id string, err error) {
	rest, ok := strings.CutPrefix(name, namePrefix)
	kindStr, id, ok2 := strings.Cut(rest, "-")
	kind = Kind(kindStr)
	if canonical, nameErr := Name(kind, id); !ok || !ok2 || nameErr != nil || canonical != name {
		err = fmt.Errorf("%q is not an afk machine name (afk-run-<id> or afk-chat-<id>)", name)
	}
	return
}

// --- machine and connection ---

// Spec describes a machine to start.
type Spec struct {
	Project   string // Project name (image.ProjectName)
	Kind      Kind
	ID        string // unique per Kind: [a-z0-9-], at most 40 characters
	BaseDisk  string // Base image disk the overlay is based on
	MemoryMiB int    // RAM of the VM
	CPUs      int    // vCPUs of the VM
}

// Machine is a running Sandbox VM.
type Machine struct {
	Name    string // libvirt name of the domain, its network and nwfilter
	Project string
	Kind    Kind
	ID      string
	Addr    string        // guest IPv4 address
	HostKey ssh.PublicKey // sshd host key of the guest, pinned by SSH
}

// Conn is a connection to libvirt (qemu:///system).
type Conn struct {
	Root string // machine directories, DefaultRoot unless set
	lv   *libvirt.Libvirt
}

// Connect connects to the system libvirt daemon.
func Connect() (c *Conn, err error) {
	lv := libvirt.NewWithDialer(dialers.NewLocal())
	if err = lv.Connect(); err != nil {
		err = fmt.Errorf("connecting to libvirt: %w", err)
		return
	}
	c = &Conn{lv: lv}
	return
}

// Close disconnects from libvirt. Machines keep running.
func (c *Conn) Close() (err error) {
	if err = c.lv.Disconnect(); err != nil {
		err = fmt.Errorf("disconnecting from libvirt: %w", err)
	}
	return
}

// --- lifecycle ---

// dir is the machine's directory (overlay, later NVRAM).
func (c *Conn) dir(kind Kind, id string) (dir string) {
	root := c.Root
	if root == "" {
		root = DefaultRoot
	}
	return filepath.Join(root, string(kind)+"s", id)
}

// Overlays returns the disks (overlays) of all machines' directories under
// root (DefaultRoot when empty): of Runs and Chats.
func Overlays(root string) (disks []string, err error) {
	if root == "" {
		root = DefaultRoot
	}
	disks = []string{} // not nil: no machines is not an unknown list
	for _, kind := range []Kind{KindRun, KindChat} {
		pattern := filepath.Join(root, string(kind)+"s", "*", diskName)
		found, globErr := filepath.Glob(pattern)
		if globErr != nil {
			err = fmt.Errorf("listing overlays %s: %w", pattern, globErr)
			return
		}
		disks = append(disks, found...)
	}
	return
}

// Up creates and starts the machine described by spec. It does not wait for
// the guest to boot; SSH does. When Up fails or ctx is cancelled, everything
// it created is destroyed, including the QEMU process.
func (c *Conn) Up(ctx context.Context, spec Spec) (m Machine, err error) {
	name, err := Name(spec.Kind, spec.ID)
	if err != nil {
		return
	}
	if spec.Project == "" || spec.BaseDisk == "" {
		err = fmt.Errorf("machine %s: project and base disk are required", name)
		return
	}
	if spec.MemoryMiB <= 0 || spec.CPUs <= 0 {
		err = fmt.Errorf("machine %s: memory and cpus must be positive, got %d MiB and %d", name, spec.MemoryMiB, spec.CPUs)
		return
	}
	dir := c.dir(spec.Kind, spec.ID)
	gid, err := image.SharedGroupID()
	if err != nil {
		return
	}
	if err = image.EnsureSharedDir(filepath.Dir(dir), gid); err != nil {
		return
	}
	// A name in use belongs to another machine (or the leftovers of a crashed
	// one, for `afk vm kill`): nothing of it may be redefined or cleaned up here.
	if err = c.checkFree(name); err != nil {
		return
	}
	// Exclusive: of two concurrent Ups with the same name only one gets past
	// this point, so the cleanup below removes only what this Up created.
	if err = os.Mkdir(dir, 0o775); err != nil {
		err = fmt.Errorf("creating machine directory: %w", err)
		return
	}
	defer func() {
		if err != nil {
			if killErr := c.kill(name, dir); killErr != nil {
				err = errors.Join(err, fmt.Errorf("cleaning up: %w", killErr))
			}
			m = Machine{}
		}
	}()
	if err = image.EnsureSharedDir(dir, gid); err != nil {
		return
	}

	def := definition{Name: name, Disk: filepath.Join(dir, diskName), MemoryMiB: spec.MemoryMiB, CPUs: spec.CPUs}
	if err = createOverlay(ctx, spec.BaseDisk, def.Disk); err != nil {
		return
	}
	hostKey, credential, err := newHostKey()
	if err != nil {
		return
	}
	def.Credential = credential

	if err = c.defineNetwork(&def); err != nil {
		return
	}
	def.Meta = metadata{
		Project: spec.Project,
		Kind:    spec.Kind,
		ID:      spec.ID,
		Addr:    def.Net.Addr(),
		HostKey: strings.TrimSpace(string(ssh.MarshalAuthorizedKey(hostKey))),
	}
	if err = ctx.Err(); err != nil {
		return
	}
	filterXML, err := render(filterTemplate, def)
	if err != nil {
		return
	}
	if _, err = c.lv.NwfilterDefineXML(filterXML); err != nil {
		err = fmt.Errorf("defining nwfilter %s: %w", name, err)
		return
	}
	domainXML, err := render(domainTemplate, def)
	if err != nil {
		return
	}
	dom, err := c.lv.DomainDefineXML(domainXML)
	if err != nil {
		err = fmt.Errorf("defining domain %s: %w", name, err)
		return
	}
	if err = ctx.Err(); err != nil {
		return
	}
	if err = c.lv.DomainCreate(dom); err != nil {
		err = fmt.Errorf("starting domain %s: %w", name, err)
		return
	}
	if err = ctx.Err(); err != nil {
		return
	}
	m = Machine{Name: name, Project: spec.Project, Kind: spec.Kind, ID: spec.ID, Addr: def.Meta.Addr, HostKey: hostKey}
	return
}

// Get returns the machine called name, from its domain metadata.
func (c *Conn) Get(name string) (m Machine, err error) {
	if _, _, err = parseName(name); err != nil {
		return
	}
	dom, err := c.lv.DomainLookupByName(name)
	if err != nil {
		err = fmt.Errorf("looking up machine %s: %w", name, err)
		return
	}
	data, err := c.lv.DomainGetMetadata(dom, int32(libvirt.DomainMetadataElement), libvirt.OptString{metadataNS}, libvirt.DomainAffectCurrent)
	if err != nil {
		err = fmt.Errorf("reading metadata of %s: %w", name, err)
		return
	}
	meta, err := parseMetadata(data)
	if err != nil {
		return
	}
	hostKey, _, _, _, err := ssh.ParseAuthorizedKey([]byte(meta.HostKey))
	if err != nil {
		err = fmt.Errorf("parsing host key of %s: %w", name, err)
		return
	}
	m = Machine{Name: name, Project: meta.Project, Kind: meta.Kind, ID: meta.ID, Addr: meta.Addr, HostKey: hostKey}
	return
}

// List returns the machines of project, running or not, from domain
// metadata, ordered by name.
func (c *Conn) List(project string) (machines []Machine, err error) {
	domains, _, err := c.lv.ConnectListAllDomains(1, 0)
	if err != nil {
		err = fmt.Errorf("listing domains: %w", err)
		return
	}
	for _, dom := range domains {
		if _, _, nameErr := parseName(dom.Name); nameErr != nil { // not afk's
			continue
		}
		var m Machine
		if m, err = c.Get(dom.Name); isCode(err, libvirt.ErrNoDomain) { // removed meanwhile
			err = nil
			continue
		}
		if err != nil {
			return
		}
		if m.Project == project {
			machines = append(machines, m)
		}
	}
	slices.SortFunc(machines, func(a, b Machine) int { return cmp.Compare(a.Name, b.Name) })
	return
}

// Kill destroys the machine called name: its domain (stopping QEMU), network,
// nwfilter and directory. Missing parts are skipped, so Kill also cleans up
// after a crashed `up`.
func (c *Conn) Kill(name string) (err error) {
	kind, id, err := parseName(name)
	if err != nil {
		return
	}
	err = c.kill(name, c.dir(kind, id))
	return
}

func (c *Conn) kill(name, dir string) (err error) {
	var errs []error
	if dom, lookupErr := c.lv.DomainLookupByName(name); lookupErr == nil {
		if destroyErr := c.lv.DomainDestroy(dom); destroyErr != nil && !isCode(destroyErr, libvirt.ErrOperationInvalid) { // not running
			errs = append(errs, fmt.Errorf("stopping domain: %w", destroyErr))
		}
		if undefineErr := c.lv.DomainUndefineFlags(dom, 0); undefineErr != nil {
			errs = append(errs, fmt.Errorf("undefining domain: %w", undefineErr))
		}
	} else if !isCode(lookupErr, libvirt.ErrNoDomain) {
		errs = append(errs, fmt.Errorf("looking up domain: %w", lookupErr))
	}
	if network, lookupErr := c.lv.NetworkLookupByName(name); lookupErr == nil {
		if destroyErr := c.lv.NetworkDestroy(network); destroyErr != nil && !isCode(destroyErr, libvirt.ErrOperationInvalid) { // not active
			errs = append(errs, fmt.Errorf("stopping network: %w", destroyErr))
		}
		if undefineErr := c.lv.NetworkUndefine(network); undefineErr != nil {
			errs = append(errs, fmt.Errorf("undefining network: %w", undefineErr))
		}
	} else if !isCode(lookupErr, libvirt.ErrNoNetwork) {
		errs = append(errs, fmt.Errorf("looking up network: %w", lookupErr))
	}
	if filter, lookupErr := c.lv.NwfilterLookupByName(name); lookupErr == nil {
		if undefineErr := c.lv.NwfilterUndefine(filter); undefineErr != nil {
			errs = append(errs, fmt.Errorf("undefining nwfilter: %w", undefineErr))
		}
	} else if !isCode(lookupErr, libvirt.ErrNoNwfilter) {
		errs = append(errs, fmt.Errorf("looking up nwfilter: %w", lookupErr))
	}
	if removeErr := os.RemoveAll(dir); removeErr != nil {
		errs = append(errs, fmt.Errorf("removing %s: %w", dir, removeErr))
	}
	if err = errors.Join(errs...); err != nil {
		err = fmt.Errorf("killing machine %s: %w", name, err)
	}
	return
}

// --- helpers ---

// checkFree fails when a domain, network or nwfilter called name exists.
func (c *Conn) checkFree(name string) (err error) {
	var taken []string
	if _, lookupErr := c.lv.DomainLookupByName(name); lookupErr == nil {
		taken = append(taken, "domain")
	} else if !isCode(lookupErr, libvirt.ErrNoDomain) {
		err = fmt.Errorf("looking up domain %s: %w", name, lookupErr)
		return
	}
	if _, lookupErr := c.lv.NetworkLookupByName(name); lookupErr == nil {
		taken = append(taken, "network")
	} else if !isCode(lookupErr, libvirt.ErrNoNetwork) {
		err = fmt.Errorf("looking up network %s: %w", name, lookupErr)
		return
	}
	if _, lookupErr := c.lv.NwfilterLookupByName(name); lookupErr == nil {
		taken = append(taken, "nwfilter")
	} else if !isCode(lookupErr, libvirt.ErrNoNwfilter) {
		err = fmt.Errorf("looking up nwfilter %s: %w", name, lookupErr)
		return
	}
	if len(taken) > 0 {
		err = fmt.Errorf("machine %s already exists (%s): kill it first", name, strings.Join(taken, ", "))
	}
	return
}

// defineNetwork defines and starts def's network on the lowest free subnet
// and sets def.Net. Two concurrent calls can pick the same subnet; libvirt then
// refuses the second define (bridge name in use), which retries.
func (c *Conn) defineNetwork(def *definition) (err error) {
	for attempt := 1; ; attempt++ {
		var used map[subnet]bool
		if used, err = c.usedSubnets(); err != nil {
			return
		}
		if def.Net, err = freeSubnet(used); err != nil {
			return
		}
		var data string
		if data, err = render(networkTemplate, *def); err != nil {
			return
		}
		network, defineErr := c.lv.NetworkDefineXML(data)
		if defineErr == nil {
			if err = c.lv.NetworkCreate(network); err != nil {
				err = fmt.Errorf("starting network %s: %w", def.Name, err)
			}
			return
		}
		if used, err = c.usedSubnets(); err != nil {
			return
		}
		if !used[def.Net] || attempt == defineAttempts {
			err = fmt.Errorf("defining network %s on %s/24: %w", def.Name, def.Net.Host(0), defineErr)
			return
		}
	}
}

// usedSubnets returns the subnets taken by existing libvirt networks.
func (c *Conn) usedSubnets() (used map[subnet]bool, err error) {
	networks, _, err := c.lv.ConnectListAllNetworks(1, 0)
	if err != nil {
		err = fmt.Errorf("listing networks: %w", err)
		return
	}
	xmls := make([]string, 0, len(networks))
	for _, network := range networks {
		data, descErr := c.lv.NetworkGetXMLDesc(network, 0)
		if isCode(descErr, libvirt.ErrNoNetwork) { // removed meanwhile
			continue
		}
		if descErr != nil {
			err = fmt.Errorf("reading network %s: %w", network.Name, descErr)
			return
		}
		xmls = append(xmls, data)
	}
	used, err = parseUsedSubnets(xmls)
	return
}

// createOverlay creates the machine's disk on top of base (docs/libvirt.md).
// -u: base is not opened, other machines may be using it.
func createOverlay(ctx context.Context, base, disk string) (err error) {
	out, err := exec.CommandContext(ctx, "qemu-img", "create", "-q", "-u", "-f", "qcow2", "-F", "qcow2", "-b", base, disk, diskSize).CombinedOutput()
	if err != nil {
		err = fmt.Errorf("creating overlay %s: %w: %s", disk, err, strings.TrimSpace(string(out)))
		return
	}
	// QEMU runs as libvirt-qemu and reaches the disk through group kvm.
	if err = os.Chmod(disk, 0o660); err != nil {
		err = fmt.Errorf("setting mode of %s: %w", disk, err)
	}
	return
}

// isCode reports whether err is a libvirt error with code.
func isCode(err error, code libvirt.ErrorNumber) (ok bool) {
	var lvErr libvirt.Error
	return errors.As(err, &lvErr) && lvErr.Code == uint32(code)
}
