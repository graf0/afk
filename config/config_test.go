package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

var requiredProtect = []string{".afk.toml", ".github/", ".gitea/", ".claude/"}

func TestParseDefaults(t *testing.T) {
	cfg, err := Parse([]byte("[forge]\nkind = \"github\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Forge.Kind != "github" {
		t.Errorf("forge.kind = %q", cfg.Forge.Kind)
	}
	wantLabels := Labels{
		NeedsTriage:   "needs-triage",
		NeedsInfo:     "needs-info",
		ReadyForAgent: "ready-for-agent",
		ReadyForHuman: "ready-for-human",
		Wontfix:       "wontfix",
	}
	if cfg.Labels != wantLabels {
		t.Errorf("labels = %+v, want %+v", cfg.Labels, wantLabels)
	}
	if cfg.Gate.MaxFixes != 3 {
		t.Errorf("gate.max_fixes = %d", cfg.Gate.MaxFixes)
	}
	if cfg.Image.Template != "debian13" {
		t.Errorf("image.template = %q", cfg.Image.Template)
	}
	if len(cfg.Network.Allow) != 0 || len(cfg.Agent.FactoryPlugins) != 0 {
		t.Errorf("allow/factory_plugins should be empty: %v %v", cfg.Network.Allow, cfg.Agent.FactoryPlugins)
	}
	if !slices.Equal(cfg.Protect.Paths, requiredProtect) {
		t.Errorf("protect.paths = %v", cfg.Protect.Paths)
	}
}

func TestParseOverrides(t *testing.T) {
	cfg, err := Parse([]byte(`
[forge]
kind = "gitea"
repo = "a/b"
url = "https://gitea.example"
[labels]
ready_for_agent = "go"
[agent]
model = "m"
factory_plugins = ["p@x"]
[gate]
cmd = "make"
max_fixes = 5
[network]
allow = ["proxy.golang.org"]
[tool.postgres]
kind = "package"
[tool.redis]
kind = "host-port"
`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Labels.ReadyForAgent != "go" || cfg.Labels.NeedsInfo != "needs-info" {
		t.Errorf("labels = %+v", cfg.Labels)
	}
	if cfg.Gate.MaxFixes != 5 || cfg.Gate.Cmd != "make" {
		t.Errorf("gate = %+v", cfg.Gate)
	}
	if cfg.Tool["postgres"].Kind != "package" || cfg.Tool["redis"].Kind != "host-port" {
		t.Errorf("tool = %+v", cfg.Tool)
	}
	if !slices.Equal(cfg.Agent.FactoryPlugins, []string{"p@x"}) {
		t.Errorf("factory_plugins = %v", cfg.Agent.FactoryPlugins)
	}
}

func TestParseProtectPaths(t *testing.T) {
	tests := []struct {
		name string
		toml string
		want []string
	}{
		{"user list appended", `paths = ["secrets/"]`, append(slices.Clone(requiredProtect), "secrets/")},
		{"duplicates removed", `paths = [".claude/", "x/", "x/"]`, append(slices.Clone(requiredProtect), "x/")},
		{"empty list keeps required", `paths = []`, requiredProtect},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := Parse([]byte("[forge]\nkind = \"github\"\n[protect]\n" + tt.toml))
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(cfg.Protect.Paths, tt.want) {
				t.Errorf("paths = %v, want %v", cfg.Protect.Paths, tt.want)
			}
		})
	}
}

func TestParseErrors(t *testing.T) {
	tests := []struct {
		name    string
		toml    string
		wantErr string
	}{
		{"unknown key", "[forge]\nkind = \"github\"\nrepoo = \"x\"\n", "repoo"},
		{"unknown section", "[forge]\nkind = \"github\"\n[netwrok]\nallow = []\n", "netwrok"},
		{"unknown tool key", "[forge]\nkind = \"github\"\n[tool.pg]\nkind = \"vm\"\nfoo = 1\n", "foo"},
		{"bad forge kind", "[forge]\nkind = \"gitlab\"\n", "forge.kind"},
		{"missing forge kind", "[forge]\nrepo = \"a/b\"\n", "forge.kind"},
		{"bad tool kind", "[forge]\nkind = \"github\"\n[tool.pg]\nkind = \"docker\"\n", "tool.pg.kind"},
		{"missing tool kind", "[forge]\nkind = \"github\"\n[tool.pg]\n", "tool.pg.kind"},
		{"invalid toml", "[forge", "parsing"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse([]byte(tt.toml))
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("err = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestLoad(t *testing.T) {
	t.Run("missing file names path", func(t *testing.T) {
		dir := t.TempDir()
		mustMkdir(t, filepath.Join(dir, ".git"))
		_, err := Load(dir)
		if err == nil || !strings.Contains(err.Error(), filepath.Join(dir, FileName)) {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("finds root from subdirectory", func(t *testing.T) {
		root := t.TempDir()
		mustMkdir(t, filepath.Join(root, ".git"))
		sub := filepath.Join(root, "a", "b")
		mustMkdir(t, sub)
		if err := os.WriteFile(filepath.Join(root, FileName), []byte("[forge]\nkind = \"gitea\"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		cfg, err := Load(sub)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Forge.Kind != "gitea" {
			t.Errorf("kind = %q", cfg.Forge.Kind)
		}
	})
	t.Run("no git root", func(t *testing.T) {
		_, err := Load(t.TempDir())
		if err == nil || !strings.Contains(err.Error(), ".git") {
			t.Errorf("err = %v", err)
		}
	})
	t.Run(".git file (worktree) counts as root", func(t *testing.T) {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, ".git"), []byte("gitdir: x"), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := Load(root)
		if err == nil || !strings.Contains(err.Error(), filepath.Join(root, FileName)) {
			t.Errorf("err = %v", err)
		}
	})
}

func TestMarshalRoundTrip(t *testing.T) {
	cfg, err := Parse([]byte("[forge]\nkind = \"github\"\n[tool.pg]\nkind = \"vm\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	out, err := cfg.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	again, err := Parse(out)
	if err != nil {
		t.Fatalf("reparsing %q: %v", out, err)
	}
	if !slices.Equal(again.Protect.Paths, cfg.Protect.Paths) || again.Tool["pg"] != cfg.Tool["pg"] || again.Gate != cfg.Gate {
		t.Errorf("round trip mismatch:\n%s", out)
	}
}

func mustMkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
}
