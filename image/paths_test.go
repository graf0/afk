package image

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestNewVersionSortsInTimeOrder(t *testing.T) {
	earlier := newVersion(time.Date(2026, 10, 24, 9, 5, 0, 0, time.FixedZone("x", 3600)))
	later := newVersion(time.Date(2026, 10, 24, 15, 30, 0, 0, time.UTC))
	if earlier != "20261024T080500Z" || later != "20261024T153000Z" {
		t.Errorf("versions = %q, %q", earlier, later)
	}
	if earlier >= later || !versionPattern.MatchString(later) {
		t.Errorf("%q must sort before %q and match the pattern", earlier, later)
	}
}

func TestLatest(t *testing.T) {
	paths := Paths{BaseDir: t.TempDir()}
	project := paths.projectDir("graf0-dfmx")
	for _, name := range []string{"20261024T080500Z", "20261024T153000Z", "20251231T235959Z", ".build-123", "stray", "20261025"} {
		if err := os.MkdirAll(filepath.Join(project, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(project, "99991231T235959Z"), nil, 0o644); err != nil { // a file, not an image
		t.Fatal(err)
	}

	got, err := Latest(paths, "graf0-dfmx")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(project, "20261024T153000Z"); got != want {
		t.Errorf("Latest = %q, want %q", got, want)
	}
}

func TestLatestWithoutImages(t *testing.T) {
	paths := Paths{BaseDir: t.TempDir()}
	if err := os.MkdirAll(filepath.Join(paths.projectDir("building"), ".build-1"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, project := range []string{"missing", "building"} {
		_, err := Latest(paths, project)
		if err == nil || !strings.Contains(err.Error(), "afk image build") {
			t.Errorf("%s: err = %v, want a hint to build", project, err)
		}
	}
}

func TestEnsureSharedDirSetsGroupAndMode(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "afk", "base")
	if err := ensureSharedDir(dir, os.Getgid()); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode() & (os.ModeSetgid | os.ModePerm); got != os.ModeSetgid|0o775 {
		t.Errorf("mode = %v, want setgid 775", got)
	}
	if got := info.Sys().(*syscall.Stat_t).Gid; int(got) != os.Getgid() {
		t.Errorf("gid = %d, want %d", got, os.Getgid())
	}

	// An existing directory with a different mode is corrected.
	if err = os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err = ensureSharedDir(dir, os.Getgid()); err != nil {
		t.Fatal(err)
	}
	if info, _ = os.Stat(dir); info.Mode()&(os.ModeSetgid|os.ModePerm) != os.ModeSetgid|0o775 {
		t.Errorf("mode = %v after fix, want setgid 775", info.Mode())
	}
}

func TestEnsureSharedDirExplainsMissingPermissions(t *testing.T) {
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	err := ensureSharedDir(filepath.Join(file, "sub"), os.Getgid())
	if err == nil || !strings.Contains(err.Error(), "must belong to group kvm and be writable") {
		t.Errorf("err = %v, want a hint about group kvm", err)
	}
}
