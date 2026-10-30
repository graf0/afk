package sandbox

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/graf0/afk/stack"
)

// hostPlugins is a ~/.claude/plugins of two marketplaces, with a plugin in
// the user scope and one in a project scope only.
func hostPlugins(t *testing.T) (home string) {
	t.Helper()
	home = t.TempDir()
	plugins := filepath.Join(home, ".claude", "plugins")
	write := func(name, content string) {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(plugins, name)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(plugins, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("installed_plugins.json", `{"version":2,"plugins":{
		"a@official":[{"scope":"project","projectPath":"/x","installPath":"`+plugins+`/cache/official/a/other"},{"scope":"user","installPath":"`+plugins+`/cache/official/a/1.0"}],
		"b@mine":[{"scope":"user","installPath":"`+plugins+`/cache/mine/b/2.0"}],
		"c@official":[{"scope":"user","installPath":"`+plugins+`/cache/official/c/1"}],
		"p@official":[{"scope":"project","installPath":"`+plugins+`/cache/official/p/1"}],
		"evil@official":[{"scope":"user","installPath":"/etc"}]}}`)
	write("known_marketplaces.json", `{"official":{"installLocation":"`+plugins+`/marketplaces/official"},"mine":{"installLocation":"`+plugins+`/marketplaces/mine"},"other":{"installLocation":"/etc"}}`)
	return
}

func TestPluginView(t *testing.T) {
	home := hostPlugins(t)
	plugins := filepath.Join(home, ".claude", "plugins")
	files, mounts, err := pluginView(home, "/ctx", "/data", []string{"a@official", "b@mine"})
	if err != nil {
		t.Fatal(err)
	}
	var installed struct{ Plugins map[string][]map[string]any }
	if err = json.Unmarshal(files["installed_plugins.json"], &installed); err != nil {
		t.Fatal(err)
	}
	if len(installed.Plugins) != 2 || len(installed.Plugins["a@official"]) != 1 || installed.Plugins["a@official"][0]["scope"] != "user" {
		t.Errorf("installed_plugins.json = %s: want the two plugins, user scope only", files["installed_plugins.json"])
	}
	var known map[string]any
	if err = json.Unmarshal(files["known_marketplaces.json"], &known); err != nil || len(known) != 2 || known["other"] != nil {
		t.Errorf("known_marketplaces.json = %s, %v", files["known_marketplaces.json"], err)
	}
	got := map[string]stack.Mount{}
	for _, m := range mounts {
		got[m.Target] = m
	}
	for _, target := range []string{
		plugins + "/cache/official/a/1.0", plugins + "/cache/mine/b/2.0", plugins + "/marketplaces/official", plugins + "/marketplaces/mine",
		plugins + "/installed_plugins.json", plugins + "/known_marketplaces.json",
	} {
		if m, ok := got[target]; !ok || !m.ReadOnly {
			t.Errorf("%s is not mounted read-only: %+v", target, m)
		}
	}
	if m := got[plugins+"/data"]; m.Source != "/data" || m.ReadOnly || !m.Dir {
		t.Errorf("data mount = %+v, want the Run's own writable directory", m)
	}
	if _, ok := got[plugins+"/cache/official/c/1"]; ok || len(got) != 7 {
		t.Errorf("mounts = %d: a plugin of the host that the Project did not name is mounted", len(got))
	}
}

func TestPluginViewWithoutPluginsMountsNothing(t *testing.T) {
	files, mounts, err := pluginView(t.TempDir(), "/ctx", "/data", nil)
	if files != nil || mounts != nil || err != nil {
		t.Errorf("= %v, %v, %v", files, mounts, err)
	}
}

func TestPluginViewRefuses(t *testing.T) {
	home := hostPlugins(t)
	for name, ids := range map[string][]string{
		"not installed":          {"nope@official"},
		"project scope only":     {"p@official"},
		"install path elsewhere": {"evil@official"},
	} {
		if _, _, err := pluginView(home, "/ctx", "/data", ids); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
	if _, _, err := pluginView(t.TempDir(), "/ctx", "/data", []string{"a@official"}); err == nil {
		t.Error("a host without plugins: want an error")
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
