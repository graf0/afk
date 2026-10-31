package chat

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// libvirtURI is the libvirt connection the test VMs live on.
const libvirtURI = "qemu:///system"

// vmDriver is what the VM routes ask of libvirt. A domain name is passed on
// only after it was found in [[vm]]; a snapshot name only after it matched
// snapshotPattern; a QMP command is built by afk, never by the Agent.
type vmDriver interface {
	State(ctx context.Context, domain string) (state string, err error)
	// Address is the IPv4 address of the guest, "" when libvirt does not know one.
	Address(ctx context.Context, domain string) (address string)
	Start(ctx context.Context, domain string) (err error)
	// Shutdown asks the guest to power off (ACPI).
	Shutdown(ctx context.Context, domain string) (err error)
	// Screenshot is the screen of the domain, a PNG.
	Screenshot(ctx context.Context, domain string) (png []byte, err error)
	// QMP sends one QMP command and returns the reply.
	QMP(ctx context.Context, domain, command string) (reply string, err error)
	Snapshot(ctx context.Context, domain, name string) (err error)
	Revert(ctx context.Context, domain, name string) (err error)
}

// virsh is the vmDriver on the virsh command, run as argv, never by a shell.
type virsh struct{}

func (virsh) run(ctx context.Context, args ...string) (out string, err error) {
	cmd := exec.CommandContext(ctx, "virsh", append([]string{"--connect", libvirtURI}, args...)...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err = cmd.Run(); err != nil {
		err = fmt.Errorf("virsh %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
		return
	}
	out = stdout.String()
	return
}

func (v virsh) State(ctx context.Context, domain string) (state string, err error) {
	out, err := v.run(ctx, "domstate", "--domain", domain)
	state = strings.TrimSpace(out)
	return
}

func (v virsh) Address(ctx context.Context, domain string) (address string) {
	out, err := v.run(ctx, "domifaddr", "--domain", domain, "--source", "lease")
	if err != nil {
		return
	}
	for line := range strings.SplitSeq(out, "\n") {
		fields := strings.Fields(line)
		// vnet0  52:54:00:..  ipv4  192.168.122.10/24
		if len(fields) >= 4 && fields[2] == "ipv4" {
			address, _, _ = strings.Cut(fields[3], "/")
			return
		}
	}
	return
}

func (v virsh) Start(ctx context.Context, domain string) (err error) {
	_, err = v.run(ctx, "start", "--domain", domain)
	return
}

func (v virsh) Shutdown(ctx context.Context, domain string) (err error) {
	_, err = v.run(ctx, "shutdown", "--domain", domain, "--mode", "acpi")
	return
}

func (v virsh) Screenshot(ctx context.Context, domain string) (png []byte, err error) {
	dir, err := os.MkdirTemp("", "afk-screenshot-")
	if err != nil {
		err = fmt.Errorf("creating a directory for the screenshot: %w", err)
		return
	}
	defer func() { _ = os.RemoveAll(dir) }()
	file := filepath.Join(dir, "screen.png")
	if _, err = v.run(ctx, "screenshot", "--domain", domain, "--file", file); err != nil {
		return
	}
	if png, err = os.ReadFile(file); err != nil {
		err = fmt.Errorf("reading the screenshot: %w", err)
	}
	return
}

func (v virsh) QMP(ctx context.Context, domain, command string) (reply string, err error) {
	return v.run(ctx, "qemu-monitor-command", "--domain", domain, "--cmd", command)
}

func (v virsh) Snapshot(ctx context.Context, domain, name string) (err error) {
	_, err = v.run(ctx, "snapshot-create-as", "--domain", domain, "--name", name)
	return
}

func (v virsh) Revert(ctx context.Context, domain, name string) (err error) {
	_, err = v.run(ctx, "snapshot-revert", "--domain", domain, "--snapshotname", name)
	return
}
