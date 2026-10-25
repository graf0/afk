package run

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/graf0/afk/config"
)

func TestTailBuffer(t *testing.T) {
	var b tailBuffer
	_, _ = b.Write([]byte(strings.Repeat("a", maxStderr)))
	_, _ = b.Write([]byte("end"))
	if got := b.String(); len(got) != maxStderr || !strings.HasSuffix(got, "aend") {
		t.Errorf("len %d, suffix %q", len(got), got[len(got)-4:])
	}
}

func TestLimitWriter(t *testing.T) {
	var out bytes.Buffer
	w := &limitWriter{w: &out, left: 5}
	if n, err := w.Write([]byte("abc")); n != 3 || err != nil {
		t.Fatalf("Write = %d, %v", n, err)
	}
	if _, err := w.Write([]byte("def")); err == nil {
		t.Error("write over the limit: want error")
	}
	if out.String() != "abc" {
		t.Errorf("out = %q", out.String())
	}
}

func TestRotatingLog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.jsonl")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	l := &rotatingLog{path: path, max: 8, file: file}
	for _, line := range []string{"1111\n", "222\n", "33\n", "4\n"} {
		if _, err = l.Write([]byte(line)); err != nil {
			t.Fatal(err)
		}
	}
	if err = l.Close(); err != nil {
		t.Fatal(err)
	}
	// "1111\n" is dropped: lines stay whole, at most two files remain.
	for name, want := range map[string]string{path + ".1": "222\n33\n", path: "4\n"} {
		if got, _ := os.ReadFile(name); string(got) != want {
			t.Errorf("%s = %q, want %q", filepath.Base(name), got, want)
		}
	}
}

func TestRunPrompt(t *testing.T) {
	r := run{
		Options: Options{Base: Base{Branch: "trunk", Config: config.Config{Protect: config.Protect{Paths: []string{".afk.toml"}}}}},
		branch:  "ticket-7",
	}
	prompt := r.prompt()
	for _, want := range []string{guestTicket, guestRepo, "`ticket-7`", "`origin/trunk`", "`.afk.toml`"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt lacks %q", want)
		}
	}
}

// hostClaude is a fake ~/.claude with the plugin a@m installed and enabled.
func hostClaude(t *testing.T) (dir string) {
	t.Helper()
	dir = t.TempDir()
	plugin := filepath.Join(dir, "plugins", "cache", "m", "a", "1.0")
	files := map[string]string{
		filepath.Join(dir, "settings.json"):                      `{"enabledPlugins": {"a@m": true}}`,
		filepath.Join(dir, "plugins", "installed_plugins.json"):  `{"plugins": {"a@m": [{"scope": "user", "installPath": "` + plugin + `"}]}}`,
		filepath.Join(plugin, "skills", "implement", "SKILL.md"): "skill",
	}
	for path, content := range files {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return
}

func pluginRun(t *testing.T, claudeDir string, allow ...string) (r run) {
	t.Helper()
	cfg := config.Config{Agent: config.Agent{FactoryPlugins: allow}}
	return run{Options: Options{Base: Base{Config: cfg}, ClaudeDir: claudeDir}}
}

func TestSelectPlugins(t *testing.T) {
	dir := hostClaude(t)
	r := pluginRun(t, dir, "a@m")
	if err := r.selectPlugins(); err != nil {
		t.Fatal(err)
	}
	if len(r.plugins) != 1 || r.plugins[0].ID != "a@m" {
		t.Fatalf("plugins = %v", r.plugins)
	}
	if got := r.guestPluginDirs(); len(got) != 1 || got[0] != guestPlugins+"/a@m" {
		t.Errorf("guest dirs = %q", got)
	}
	if got := r.task().Plugins; len(got) != 1 || got[0] != "a" {
		t.Errorf("task plugins = %q", got)
	}
}

// The default (empty) allowlist must not read ~/.claude at all, and a plugin
// the host lacks fails the Run before the VM.
func TestSelectPluginsAllowlist(t *testing.T) {
	r := pluginRun(t, filepath.Join(t.TempDir(), "missing"))
	if err := r.selectPlugins(); err != nil || r.plugins != nil {
		t.Errorf("empty allowlist: %v, %v", r.plugins, err)
	}
	r = pluginRun(t, hostClaude(t), "b@m")
	if err := r.selectPlugins(); err == nil || !strings.Contains(err.Error(), "b@m is not installed") {
		t.Errorf("missing plugin: err = %v", err)
	}
}

func TestSelectPluginsWarnsAboutDisabled(t *testing.T) {
	dir := hostClaude(t)
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(`{"enabledPlugins": {"a@m": false}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	r := pluginRun(t, dir, "a@m")
	r.Out = &out
	if err := r.selectPlugins(); err != nil || len(r.plugins) != 0 {
		t.Fatalf("plugins = %v, err = %v", r.plugins, err)
	}
	if !strings.Contains(out.String(), "warning: plugin a@m") {
		t.Errorf("no warning: %q", out.String())
	}
}
