package chat

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeVirsh puts a virsh on the PATH that logs its argv, a line per call, and
// answers like libvirt for the few commands the driver parses.
func fakeVirsh(t *testing.T) (log string) {
	t.Helper()
	dir := t.TempDir()
	log = filepath.Join(dir, "calls")
	script := `#!/bin/sh
echo "$*" >> ` + log + `
case "$3" in
domstate) echo "running" ;;
domifaddr) printf ' Name MAC protocol Address\n----\n vnet0 52:54:00:00:00:01 ipv4 192.168.122.10/24\n' ;;
screenshot) while [ $# -gt 0 ]; do [ "$1" = --file ] && printf PNGDATA > "$2"; shift; done ;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "virsh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return
}

func TestVirsh(t *testing.T) {
	log := fakeVirsh(t)
	ctx, v := t.Context(), virsh{}
	if state, err := v.State(ctx, "win11"); err != nil || state != "running" {
		t.Errorf("State = %q, %v", state, err)
	}
	if addr := v.Address(ctx, "win11"); addr != "192.168.122.10" {
		t.Errorf("Address = %q", addr)
	}
	if png, err := v.Screenshot(ctx, "win11"); err != nil || string(png) != "PNGDATA" {
		t.Errorf("Screenshot = %q, %v", png, err)
	}
	for _, err := range []error{
		v.Start(ctx, "win11"), v.Shutdown(ctx, "win11"), v.Snapshot(ctx, "win11", "clean"), v.Revert(ctx, "win11", "clean"),
	} {
		if err != nil {
			t.Error(err)
		}
	}
	if _, err := v.QMP(ctx, "win11", `{"execute":"x"}`); err != nil {
		t.Error(err)
	}
	data, _ := os.ReadFile(log)
	calls := string(data)
	for _, want := range []string{
		"--connect qemu:///system domstate --domain win11",
		"start --domain win11",
		"shutdown --domain win11 --mode acpi",
		"snapshot-create-as --domain win11 --name clean",
		"snapshot-revert --domain win11 --snapshotname clean",
		`qemu-monitor-command --domain win11 --cmd {"execute":"x"}`,
	} {
		if !strings.Contains(calls, want) {
			t.Errorf("calls lack %q:\n%s", want, calls)
		}
	}
}

func TestVirshFailure(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	if err := (virsh{}).Start(t.Context(), "x"); err == nil {
		t.Error("Start without virsh: want an error")
	}
}
