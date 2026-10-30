package run

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/graf0/afk/forge"
	"github.com/graf0/afk/image"
)

func TestStackPaths(t *testing.T) {
	r := run{Options: Options{Ticket: forge.Ticket{Number: 7}, Paths: image.Paths{StateDir: "/state"}}, project: "o-r"}
	if r.stackName() != "ticket-7" || r.stackDir() != "/state/o-r/runs/ticket-7" || r.treePath() != "/state/o-r/runs/ticket-7/repo" ||
		r.contextFile(ticketFileName) != "/state/o-r/runs/ticket-7/context/TICKET.md" {
		t.Errorf("name %q, dir %q, tree %q, ticket %q", r.stackName(), r.stackDir(), r.treePath(), r.contextFile(ticketFileName))
	}
	r.id = "preflight-ab12"
	if r.stackName() != "preflight-ab12" || r.stackDir() != "/state/o-r/runs/preflight-ab12" {
		t.Errorf("preflight: name %q, dir %q", r.stackName(), r.stackDir())
	}
}

func TestClaudeSettings(t *testing.T) {
	read := func(data []byte) (settings map[string]any) {
		t.Helper()
		if err := json.Unmarshal(data, &settings); err != nil {
			t.Fatalf("%v: %s", err, data)
		}
		return
	}
	data, err := claudeSettings(nil, nil)
	if err != nil || len(read(data)) != 0 {
		t.Errorf("no settings, no plugins = %s, %v; want {}", data, err)
	}
	// The Project's settings stay; the effective plugins are all on.
	data, err = claudeSettings([]byte(`{"model":"x","enabledPlugins":{"a@m":true,"off@m":false}}`), []string{"a@m", "b@m"})
	if err != nil {
		t.Fatal(err)
	}
	settings := read(data)
	enabled, _ := settings["enabledPlugins"].(map[string]any)
	if settings["model"] != "x" || enabled["a@m"] != true || enabled["b@m"] != true || enabled["off@m"] != false {
		t.Errorf("settings = %s", data)
	}
	for _, bad := range []string{`{`, `{"enabledPlugins":[]}`} {
		if _, err = claudeSettings([]byte(bad), nil); err == nil {
			t.Errorf("settings %q: want an error", bad)
		}
	}
}

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
