package image

import (
	"os"
	"path/filepath"
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

	_, err := Build(t.Context(), Options{Paths: paths, Config: cfg, Out: &syncBuffer{}})
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

func TestInstallArtifactReplacesOldImage(t *testing.T) {
	base := t.TempDir()
	dest := filepath.Join(base, "graf0-dfmx")
	if err := os.Mkdir(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTree(t, dest, map[string]string{"debian13.qcow2": "old", "stale": "x"})
	src := filepath.Join(base, ".build-1", "out")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTree(t, src, map[string]string{"debian13.qcow2": "new"})

	if err := installArtifact(src, dest, os.Getgid()); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dest, "debian13.qcow2"))
	if err != nil || string(data) != "new" {
		t.Errorf("image = %q, %v; want new", data, err)
	}
	if _, err = os.Stat(filepath.Join(dest, "stale")); err == nil {
		t.Error("old image directory content survived")
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
	entries, _ := os.ReadDir(base)
	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".old") {
			t.Errorf("leftover %s", entry.Name())
		}
	}
}

func TestInstallArtifactKeepsOldImageOnFailure(t *testing.T) {
	base := t.TempDir()
	dest := filepath.Join(base, "p")
	if err := os.Mkdir(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTree(t, dest, map[string]string{"debian13.qcow2": "old"})

	if err := installArtifact(filepath.Join(base, "missing"), dest, -1); err == nil {
		t.Fatal("err = nil, want error for missing artifact")
	}
	data, err := os.ReadFile(filepath.Join(dest, "debian13.qcow2"))
	if err != nil || string(data) != "old" {
		t.Errorf("old image = %q, %v; want untouched", data, err)
	}
}
