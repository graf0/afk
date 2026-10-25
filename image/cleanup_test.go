package image

import (
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// writeOverlay writes a qcow2 file with a minimal header whose backing file is
// backing (empty: none), in dir/disk.qcow2.
func writeOverlay(t *testing.T, dir, backing string) (path string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	header := make([]byte, 72)
	copy(header, qcow2Magic)
	binary.BigEndian.PutUint32(header[4:], 3)
	if backing != "" {
		binary.BigEndian.PutUint64(header[8:], uint64(len(header)))
		binary.BigEndian.PutUint32(header[16:], uint32(len(backing)))
		header = append(header, backing...)
	}
	path = filepath.Join(dir, "disk.qcow2")
	if err := os.WriteFile(path, header, 0o600); err != nil {
		t.Fatal(err)
	}
	return
}

func TestBackingFile(t *testing.T) {
	dir := t.TempDir()
	if got, err := backingFile(writeOverlay(t, filepath.Join(dir, "a"), "/base/v1/disk.qcow2")); err != nil || got != "/base/v1/disk.qcow2" {
		t.Errorf("backingFile = %q, %v", got, err)
	}
	if got, err := backingFile(writeOverlay(t, filepath.Join(dir, "b"), "")); err != nil || got != "" {
		t.Errorf("no backing: %q, %v", got, err)
	}
	if got, err := backingFile(filepath.Join(dir, "missing")); err != nil || got != "" {
		t.Errorf("missing: %q, %v", got, err)
	}
	if got, err := backingFile(writeOverlay(t, filepath.Join(dir, "r"), "../base/v1/disk.qcow2")); err != nil || got != filepath.Join(dir, "base/v1/disk.qcow2") {
		t.Errorf("relative backing = %q, %v", got, err)
	}
	huge := writeOverlay(t, filepath.Join(dir, "h"), "x")
	data, _ := os.ReadFile(huge)
	binary.BigEndian.PutUint64(data[8:], math.MaxUint64)
	if err := os.WriteFile(huge, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := backingFile(huge); err == nil {
		t.Error("want an error for an offset out of range")
	}
	junk := filepath.Join(dir, "junk")
	if err := os.WriteFile(junk, []byte(strings.Repeat("x", 100)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := backingFile(junk); err == nil {
		t.Error("want an error for a file that is not qcow2")
	}
}

func cleanupEnv(t *testing.T, versions ...string) (paths Paths, machineDir string) {
	t.Helper()
	paths = Paths{BaseDir: filepath.Join(t.TempDir(), "base")}
	machineDir = paths.machineDir("p", "sandbox")
	for _, version := range versions {
		if err := os.MkdirAll(filepath.Join(machineDir, version), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return
}

func listDir(t *testing.T, dir string) (names []string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return
}

func TestCleanup(t *testing.T) {
	const v1, v2, v3, v4, v5 = "20261021T000000Z", "20261022T000000Z", "20261023T000000Z", "20261024T000000Z", "20261025T000000Z"
	paths, machineDir := cleanupEnv(t, v1, v2, v3, v4, v5, ".build-123")
	vms := t.TempDir()
	// v1 is kept by an overlay (absolute backing), v2 by one with a relative
	// backing; v3 is not used; v4 is the previous newest, v5 the newest.
	overlays := []string{
		writeOverlay(t, filepath.Join(vms, "a"), filepath.Join(machineDir, v1, "disk.qcow2")),
		writeOverlay(t, filepath.Join(paths.BaseDir, "..", "vm", "b"), "../../base/p/sandbox/"+v2+"/disk.qcow2"),
		writeOverlay(t, filepath.Join(vms, "c"), "/elsewhere/"+v3+"/disk.qcow2"),
		writeOverlay(t, filepath.Join(vms, "d"), ""),
	}

	removed, err := Cleanup(paths, "p", "sandbox", overlays)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{v3}; !slices.Equal(removed, want) {
		t.Errorf("removed = %v, want %v", removed, want)
	}
	want := []string{".build-123", v1, v2, v4, v5}
	if got := listDir(t, machineDir); !slices.Equal(got, want) {
		t.Errorf("left = %v, want %v", got, want)
	}
}

func TestCleanupNothingToDo(t *testing.T) {
	for _, versions := range [][]string{{".build-1"}, {"20261024T000000Z"}, {"20261021T000000Z", "20261024T000000Z"}} {
		paths, machineDir := cleanupEnv(t, versions...)
		removed, err := Cleanup(paths, "p", "sandbox", []string{})
		if err != nil || len(removed) != 0 {
			t.Errorf("%v: removed %v, %v", versions, removed, err)
		}
		if got := listDir(t, machineDir); !slices.Equal(got, versions) {
			t.Errorf("left = %v, want %v", got, versions)
		}
	}
}

func TestCleanupErrorsRemoveNothing(t *testing.T) {
	versions := []string{"20261021T000000Z", "20261022T000000Z", "20261023T000000Z"}
	paths, machineDir := cleanupEnv(t, versions...)
	junk := filepath.Join(t.TempDir(), "disk.qcow2")
	if err := os.WriteFile(junk, []byte(strings.Repeat("x", 100)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Cleanup(paths, "p", "sandbox", []string{junk}); err == nil {
		t.Error("want an error for an unreadable overlay")
	}
	if _, err := Cleanup(paths, "p", "sandbox", nil); err == nil {
		t.Error("want an error without the list of overlays")
	}
	if got := listDir(t, machineDir); !slices.Equal(got, versions) {
		t.Errorf("left = %v, want all versions", got)
	}
}
