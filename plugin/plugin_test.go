package plugin

import (
	"archive/tar"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const root = "/home/u/.claude/plugins"

// install is id installed where Claude Code puts it.
func install(id string) (installed map[string][]Install) {
	name, market, _ := strings.Cut(id, "@")
	return map[string][]Install{id: {{Scope: "user", InstallPath: root + "/cache/" + market + "/" + name + "/1.0"}}}
}

func TestSelectAllowlist(t *testing.T) {
	enabled := map[string]bool{"a@m": true, "b@m": true, "c@m": true}
	installed := map[string][]Install{
		"a@m": {{Scope: "project", InstallPath: root + "/cache/m/a/project"}, {Scope: "user", InstallPath: root + "/cache/m/a/1.0"}},
		"b@m": {{Scope: "user", InstallPath: root + "/cache/m/b/2"}},
		"c@m": {{Scope: "user", InstallPath: root + "/cache/m/c/1"}},
	}
	got, _, err := Select(enabled, installed, []string{"b@m", "a@m"}, root)
	if err != nil {
		t.Fatal(err)
	}
	want := []Plugin{{ID: "b@m", Dir: root + "/cache/m/b/2"}, {ID: "a@m", Dir: root + "/cache/m/a/1.0"}}
	if !slices.Equal(got, want) {
		t.Errorf("got %v, want %v (c is not on the allowlist)", got, want)
	}
	if got, _, err = Select(enabled, installed, nil, root); err != nil || len(got) != 0 {
		t.Errorf("empty allowlist: got %v, %v, want nothing", got, err)
	}
}

func TestSelectNotInstalled(t *testing.T) {
	enabled := map[string]bool{"a@m": true, "b@m": true}
	for name, installed := range map[string]map[string][]Install{
		"absent":     install("b@m"),
		"not a user": {"a@m": {{Scope: "project", InstallPath: root + "/cache/m/a/1"}}},
	} {
		if _, _, err := Select(enabled, installed, []string{"a@m"}, root); err == nil || !strings.Contains(err.Error(), "not installed") {
			t.Errorf("%s: err = %v, want not installed", name, err)
		}
	}
}

func TestSelectDisabled(t *testing.T) {
	got, disabled, err := Select(map[string]bool{"a@m": false}, install("a@m"), []string{"a@m"}, root)
	if err != nil || len(got) != 0 || !slices.Equal(disabled, []string{"a@m"}) {
		t.Errorf("got %v, disabled %v, %v, want nothing and a@m reported", got, disabled, err)
	}
}

func TestSelectRejects(t *testing.T) {
	enabled := map[string]bool{"a@m": true}
	for name, path := range map[string]string{
		"outside":   "/etc",
		"traversal": root + "/../settings",
		"root":      root,
		"relative":  "cache/m/a/1",
		"cache":     root + "/cache",
		"market":    root + "/cache/m",
		"name":      root + "/cache/m/a",
		"deeper":    root + "/cache/m/a/1/x",
		"other id":  root + "/cache/m/b/1",
		"other mkt": root + "/cache/n/a/1",
		"dot":       root + "/cache/m/a/.",
	} {
		installed := map[string][]Install{"a@m": {{Scope: "user", InstallPath: path}}}
		if _, _, err := Select(enabled, installed, []string{"a@m"}, root); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
	for _, id := range []string{"a", "../x@m", "a@m/b", "-a@m", ""} {
		if _, _, err := Select(map[string]bool{id: true}, install(id), []string{id}, root); err == nil {
			t.Errorf("id %q: want error", id)
		}
	}
}

func TestParse(t *testing.T) {
	enabled, err := ParseEnabled([]byte(`{"enabledPlugins": {"a@m": true, "b@m": false}, "hooks": {}}`))
	if err != nil || !enabled["a@m"] || enabled["b@m"] {
		t.Errorf("enabled = %v, %v", enabled, err)
	}
	installed, err := ParseInstalled([]byte(`{"version": 2, "plugins": {"a@m": [{"scope": "user", "installPath": "/x", "version": "1"}]}}`))
	if err != nil || installed["a@m"][0] != (Install{Scope: "user", InstallPath: "/x"}) {
		t.Errorf("installed = %v, %v", installed, err)
	}
	if _, err = ParseEnabled([]byte("{")); err == nil {
		t.Error("broken settings: want error")
	}
}

// claudeDir is a fake ~/.claude with a plugin, its neighbours and secrets.
func claudeDir(t *testing.T) (plugins string, p Plugin) {
	t.Helper()
	home := t.TempDir()
	plugins = filepath.Join(home, "plugins")
	dir := filepath.Join(plugins, "cache", "m", "a", "1.0")
	for path, content := range map[string]string{
		filepath.Join(dir, "skills", "x", "SKILL.md"):    "skill",
		filepath.Join(dir, ".git", "config"):             "git",
		filepath.Join(plugins, "cache", "m", "other"):    "other plugin",
		filepath.Join(plugins, "installed_plugins.json"): "{}",
		filepath.Join(home, ".credentials.json"):         "secret",
		filepath.Join(home, "settings.json"):             "hooks",
		filepath.Join(home, "history.jsonl"):             "history",
		filepath.Join(home, "skills", "synced", "s"):     "synced",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Symlinks out of the plugin: to a secret and to a directory.
	for name, target := range map[string]string{
		"link-file": filepath.Join(home, ".credentials.json"),
		"link-dir":  filepath.Join(home, "skills"),
	} {
		if err := os.Symlink(target, filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	return plugins, Plugin{ID: "a@m", Dir: dir}
}

func tarNames(t *testing.T, data []byte) (names []string, contents map[string]string) {
	t.Helper()
	contents = map[string]string{}
	reader := tar.NewReader(bytes.NewReader(data))
	for {
		header, err := reader.Next()
		if err == io.EOF {
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		if header.Typeflag != tar.TypeDir && header.Typeflag != tar.TypeReg {
			t.Errorf("%s: type %c, want a directory or a regular file", header.Name, header.Typeflag)
		}
		if header.Uid != 0 || header.Uname != "" {
			t.Errorf("%s: owner %d %q leaks", header.Name, header.Uid, header.Uname)
		}
		body, _ := io.ReadAll(reader)
		names, contents[header.Name] = append(names, header.Name), string(body)
	}
}

// Only the plugin's directory goes into the archive: no credentials, no
// settings.json, no other plugins, nothing behind symlinks.
func TestTarHasOnlyPluginFiles(t *testing.T) {
	plugins, p := claudeDir(t)
	var buf bytes.Buffer
	if err := Tar(&buf, plugins, []Plugin{p}); err != nil {
		t.Fatal(err)
	}
	names, contents := tarNames(t, buf.Bytes())
	for _, name := range names {
		if name != "a@m" && !strings.HasPrefix(name, "a@m/") {
			t.Errorf("%q is outside the plugin directory", name)
		}
		if strings.Contains(name, "..") || strings.Contains(name, ".git") || strings.Contains(name, "link-") {
			t.Errorf("%q must not be packed", name)
		}
	}
	if contents["a@m/skills/x/SKILL.md"] != "skill" {
		t.Errorf("SKILL.md missing: %q", names)
	}
	for _, body := range contents {
		if body == "secret" || body == "synced" || body == "hooks" {
			t.Errorf("host file leaked into the archive: %q", body)
		}
	}
}

func TestTarRejectsPluginOutsideRoot(t *testing.T) {
	plugins, p := claudeDir(t)
	// The plugin directory itself is a symlink out of the plugins directory.
	link := filepath.Join(plugins, "cache", "evil")
	if err := os.Symlink(filepath.Dir(plugins), link); err != nil {
		t.Fatal(err)
	}
	if err := Tar(io.Discard, plugins, []Plugin{{ID: "evil@m", Dir: link}}); err == nil {
		t.Error("symlinked plugin directory: want error")
	}
	if err := Tar(io.Discard, plugins, []Plugin{p}); err != nil {
		t.Errorf("plain plugin: %v", err)
	}
}

// A hard link to a secret looks like a regular file inside the plugin.
func TestTarRejectsHardlinks(t *testing.T) {
	plugins, p := claudeDir(t)
	secret := filepath.Join(filepath.Dir(plugins), ".credentials.json")
	if err := os.Link(secret, filepath.Join(p.Dir, "hard")); err != nil {
		t.Fatal(err)
	}
	if err := Tar(io.Discard, plugins, []Plugin{p}); err == nil || !strings.Contains(err.Error(), "hard link") {
		t.Errorf("err = %v, want a hard link error", err)
	}
}

func TestTarDropsSpecialModeBits(t *testing.T) {
	plugins, p := claudeDir(t)
	script := filepath.Join(p.Dir, "run.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(script, 0o755|os.ModeSetuid|os.ModeSetgid); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := Tar(&buf, plugins, []Plugin{p}); err != nil {
		t.Fatal(err)
	}
	reader := tar.NewReader(&buf)
	for {
		header, err := reader.Next()
		if err != nil {
			t.Fatal("run.sh not in the archive")
		}
		if header.Name == "a@m/run.sh" {
			if header.Mode != 0o755 {
				t.Errorf("mode = %o, want 755", header.Mode)
			}
			return
		}
	}
}
