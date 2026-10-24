package image

import (
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/graf0/afk/config"
)

func TestProjectName(t *testing.T) {
	tests := []struct {
		repo    string
		want    string
		wantErr bool
	}{
		{repo: "graf0/dfmx", want: "graf0-dfmx"},
		{repo: "org/my.repo_1", want: "org-my.repo_1"},
		{repo: "", wantErr: true},
		{repo: "../etc", wantErr: true},
		{repo: "a/../b", wantErr: true},
		{repo: ".hidden/x", wantErr: true},
		{repo: "a b/c", wantErr: true},
		{repo: "a//c", want: "a--c"},
	}
	for _, tt := range tests {
		got, err := ProjectName(tt.repo)
		if (err != nil) != tt.wantErr || got != tt.want {
			t.Errorf("ProjectName(%q) = %q, %v; want %q, error=%v", tt.repo, got, err, tt.want, tt.wantErr)
		}
	}
}

func TestBuildRejectsUnsupportedTemplateBeforeAnyWork(t *testing.T) {
	paths := Paths{CacheDir: filepath.Join(t.TempDir(), "cache"), StateDir: t.TempDir(), BaseDir: t.TempDir()}
	cfg := config.Config{Forge: config.Forge{Kind: "github", Repo: "a/b"}, Image: config.Image{Template: "ws2025"}}

	_, err := Build(t.Context(), Options{Paths: paths, Config: cfg, Out: io.Discard})
	if err == nil || !strings.Contains(err.Error(), "ws2025") {
		t.Errorf("err = %v, want unsupported template error", err)
	}
	if _, statErr := os.Stat(paths.CacheDir); statErr == nil {
		t.Error("cache dir created before validation")
	}
}

func writeTree(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestInstallArtifactMovesTreeToNewVersion(t *testing.T) {
	project := t.TempDir()
	src := filepath.Join(project, ".build-1", "out")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTree(t, src, map[string]string{"debian13.qcow2": "new"})
	dest := filepath.Join(project, "20261024T153000Z")

	if err := installArtifact(src, dest, os.Getgid()); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dest, "debian13.qcow2"))
	if err != nil || string(data) != "new" {
		t.Errorf("image = %q, %v; want new", data, err)
	}
	info, err := os.Stat(filepath.Join(dest, "debian13.qcow2"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Errorf("image mode = %v, want 0640", info.Mode().Perm())
	}
	dirInfo, _ := os.Stat(dest)
	if dirInfo.Mode()&os.ModeSetgid == 0 {
		t.Error("image dir lacks setgid")
	}
	if _, err = os.Stat(src); err == nil {
		t.Error("source still exists after the move")
	}
}

func TestInstallArtifactNeverReplacesExistingVersion(t *testing.T) {
	project := t.TempDir()
	dest := filepath.Join(project, "20261024T153000Z")
	if err := os.Mkdir(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTree(t, dest, map[string]string{"debian13.qcow2": "old"})
	src := filepath.Join(project, ".build-1", "out")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTree(t, src, map[string]string{"debian13.qcow2": "new"})

	if err := installArtifact(src, dest, os.Getgid()); err == nil {
		t.Fatal("err = nil, want error for an existing version")
	}
	data, err := os.ReadFile(filepath.Join(dest, "debian13.qcow2"))
	if err != nil || string(data) != "old" {
		t.Errorf("existing image = %q, %v; want untouched", data, err)
	}
}

func TestInstallArtifactMissingSource(t *testing.T) {
	project := t.TempDir()
	if err := installArtifact(filepath.Join(project, "missing"), filepath.Join(project, "v"), os.Getgid()); err == nil {
		t.Fatal("err = nil, want error for missing artifact")
	}
}

func TestListenLoopbackAndFreePort(t *testing.T) {
	ln, port, err := listenLoopback()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	if port == 0 || ln.Addr().String() != "127.0.0.1:"+strconv.Itoa(port) {
		t.Errorf("listener %v, port %d", ln.Addr(), port)
	}
	free, err := freePort()
	if err != nil || free == 0 || free == port {
		t.Errorf("freePort = %d, %v", free, err)
	}
}

func TestTempDirCleanup(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "new-parent")
	dir, cleanup, err := tempDir(parent, "x-*")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(dir) != parent {
		t.Errorf("dir = %q, want inside %q", dir, parent)
	}
	cleanup()
	if _, err = os.Stat(dir); err == nil {
		t.Error("dir survived cleanup")
	}
}
