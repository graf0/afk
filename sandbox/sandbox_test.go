package sandbox

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/graf0/afk/stack"
)

// A Run's home is the Agent's: a symlink in the way of a mount point is
// removed (not followed), a wrong type replaced, existing points kept.
func TestMountPoint(t *testing.T) {
	home, outside := t.TempDir(), t.TempDir()
	secret := filepath.Join(outside, "secret")
	if err := os.WriteFile(secret, []byte("host"), 0o644); err != nil {
		t.Fatal(err)
	}
	for path, target := range map[string]string{"bin": outside, ".claude": outside} {
		if err := os.Symlink(target, filepath.Join(home, path)); err != nil {
			t.Fatal(err)
		}
	}
	for rel, isDir := range map[string]bool{"bin/claude": false, ".claude/settings.json": false, ".claude/plugins": true} {
		if err := mountPoint(home, rel, isDir); err != nil {
			t.Fatalf("%s: %v", rel, err)
		}
		info, err := os.Lstat(filepath.Join(home, rel))
		if err != nil || info.IsDir() != isDir || info.Mode()&os.ModeSymlink != 0 {
			t.Errorf("%s: %v, %v", rel, info, err)
		}
	}
	if got, _ := os.ReadFile(secret); string(got) != "host" {
		t.Error("the file behind the symlink was touched")
	}
	entries, _ := os.ReadDir(outside)
	if len(entries) != 1 {
		t.Errorf("the symlink target gained %d entries", len(entries)-1)
	}
	// A directory where a file goes is replaced; a file that is there stays.
	if err := os.Remove(filepath.Join(home, "bin", "claude")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(home, "bin", "claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".claude", "settings.json"), []byte("kept"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"bin/claude", ".claude/settings.json"} {
		if err := mountPoint(home, rel, false); err != nil {
			t.Fatal(err)
		}
	}
	if info, _ := os.Lstat(filepath.Join(home, "bin", "claude")); info.IsDir() {
		t.Error("directory stayed where a file goes")
	}
	if got, _ := os.ReadFile(filepath.Join(home, ".claude", "settings.json")); !slices.Equal(got, []byte("kept")) {
		t.Errorf("existing file rewritten: %q", got)
	}
}

// The Agent put a symlink where a file mount point goes, to a file of the
// host: it is replaced, and what it pointed to is not written through.
func TestMountPointSymlinkToHostFile(t *testing.T) {
	home, outside := t.TempDir(), t.TempDir()
	secret := filepath.Join(outside, "secret")
	if err := os.WriteFile(secret, []byte("host"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(home, ".claude", "settings.json")); err != nil {
		t.Fatal(err)
	}
	if err := mountPoint(home, ".claude/settings.json", false); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(secret); string(got) != "host" {
		t.Errorf("the host file behind the symlink = %q", got)
	}
	if info, err := os.Lstat(filepath.Join(home, ".claude", "settings.json")); err != nil || !info.Mode().IsRegular() {
		t.Errorf("mount point: %v, %v", info, err)
	}
}

func TestMountPoints(t *testing.T) {
	hostHome, agentHome := "/home/u", t.TempDir()
	mounts := []stack.Mount{
		{Target: "/home/u/bin/claude"},
		{Target: "/home/u/.claude/plugins", Dir: true},
		{Target: "/run/afk", Dir: true}, // outside the home: nothing to make
		{Target: "/home/u", Dir: true},  // the home itself
		{Target: "/home", Dir: true},    // above it
	}
	if err := mountPoints(hostHome, agentHome, "/home/u/.local/tree", mounts); err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"bin": true, "bin/claude": false, ".claude": true, ".claude/plugins": true, ".local": true, ".local/tree": true}
	for rel, isDir := range want {
		if info, err := os.Lstat(filepath.Join(agentHome, rel)); err != nil || info.IsDir() != isDir {
			t.Errorf("%s: %v, %v", rel, info, err)
		}
	}
	entries, _ := os.ReadDir(agentHome)
	if len(entries) != 3 {
		t.Errorf("home has %d entries, want bin, .claude and .local only", len(entries))
	}
}

func TestBuildAndPrepare(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	contextDir, dataDir, agentHome := filepath.Join(t.TempDir(), "ctx"), filepath.Join(t.TempDir(), "data"), t.TempDir()
	view, err := Build(Options{ContextDir: contextDir, DataDir: dataDir, NoClaude: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Mounts) != 1 || view.Mounts[0].Target != filepath.Join(home, ".claude", "settings.json") || !view.Mounts[0].ReadOnly {
		t.Errorf("mounts = %+v, want the settings, read-only", view.Mounts)
	}
	view.Files["extra"] = []byte("x") // the caller's own
	if err = view.Prepare(agentHome, filepath.Join(home, "tree")); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(contextDir, "settings.json"), filepath.Join(contextDir, "extra"), dataDir,
		filepath.Join(agentHome, ".claude", "settings.json"), filepath.Join(agentHome, "tree")} {
		if _, err = os.Lstat(path); err != nil {
			t.Error(err)
		}
	}
}
