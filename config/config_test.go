package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// minimalConfig is the smallest valid Project config.
const minimalConfig = "[forge]\nkind = \"github\"\nrepo = \"o/r\"\n[gate]\ncmd = \"true\"\n"

var requiredProtect = []string{".afk.toml", "compose.yml", "compose.afk.yml", ".env", ".afk/", ".github/", ".gitea/", "**/.claude/", "**/.mcp.json"}

func TestParseDefaults(t *testing.T) {
	cfg, err := Parse([]byte(minimalConfig))
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
		Spec:          "spec",
	}
	if cfg.Labels != wantLabels {
		t.Errorf("labels = %+v, want %+v", cfg.Labels, wantLabels)
	}
	if cfg.Gate.MaxFixes != 3 {
		t.Errorf("gate.max_fixes = %d", cfg.Gate.MaxFixes)
	}
	if len(cfg.Agent.Plugins) != 0 || len(cfg.Agent.Marketplaces) != 0 {
		t.Errorf("plugins/marketplaces should be empty: %v %v", cfg.Agent.Plugins, cfg.Agent.Marketplaces)
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
plugins = ["p@x"]
marketplaces = ["o/r"]
[gate]
cmd = "make"
max_fixes = 5
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
	if !slices.Equal(cfg.Agent.Plugins, []string{"p@x"}) || !slices.Equal(cfg.Agent.Marketplaces, []string{"o/r"}) {
		t.Errorf("plugins = %v, marketplaces = %v", cfg.Agent.Plugins, cfg.Agent.Marketplaces)
	}
}

func TestParseProtectPaths(t *testing.T) {
	tests := []struct {
		name string
		toml string
		want []string
	}{
		{"user list appended", `paths = ["secrets/"]`, append(slices.Clone(requiredProtect), "secrets/")},
		{"duplicates removed", `paths = ["**/.claude/", "x/", "x/"]`, append(slices.Clone(requiredProtect), "x/")},
		{"empty list keeps required", `paths = []`, requiredProtect},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := Parse([]byte(minimalConfig + "[protect]\n" + tt.toml))
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(cfg.Protect.Paths, tt.want) {
				t.Errorf("paths = %v, want %v", cfg.Protect.Paths, tt.want)
			}
		})
	}
}

func TestLoad(t *testing.T) {
	t.Run("missing file names path", func(t *testing.T) {
		dir := t.TempDir()
		mustMkdir(t, filepath.Join(dir, ".git"))
		_, err := Load(dir)
		if err == nil || !strings.Contains(err.Error(), dir) || !strings.Contains(err.Error(), "afk init") ||
			strings.Contains(err.Error(), "no such file") {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("finds root from subdirectory", func(t *testing.T) {
		root := t.TempDir()
		mustMkdir(t, filepath.Join(root, ".git"))
		sub := filepath.Join(root, "a", "b")
		mustMkdir(t, sub)
		if err := os.WriteFile(filepath.Join(root, FileName), []byte("[forge]\nkind = \"gitea\"\nrepo = \"a/b\"\nurl = \"https://g.example\"\n[gate]\ncmd = \"true\"\n"), 0o644); err != nil {
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
		if err == nil || !strings.Contains(err.Error(), "git repository") {
			t.Errorf("err = %v", err)
		}
	})
	t.Run(".git file (worktree) counts as root", func(t *testing.T) {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, ".git"), []byte("gitdir: x"), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := Load(root)
		if err == nil || !strings.Contains(err.Error(), "no "+FileName+" in "+root) {
			t.Errorf("err = %v", err)
		}
	})
}

func TestMarshalRoundTrip(t *testing.T) {
	cfg, err := Parse([]byte(minimalConfig + "[protect]\npaths = [\"x/\"]\n"))
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
	if !slices.Equal(again.Protect.Paths, cfg.Protect.Paths) || again.Gate != cfg.Gate {
		t.Errorf("round trip mismatch:\n%s", out)
	}
}

func mustMkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

// TestParseReadmeExamples keeps the .afk.toml example of the README valid.
func TestParseReadmeExamples(t *testing.T) {
	readme, err := os.ReadFile(filepath.Join("..", "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	_, block, found := strings.Cut(string(readme), "```toml\n")
	if !found {
		t.Fatal("no toml block in README.md")
	}
	example, _, _ := strings.Cut(block, "```")
	cfg, err := Parse([]byte(example))
	if err != nil {
		t.Fatalf("README example: %v", err)
	}
	if cfg.Expose["web"] == "" || len(cfg.VM) != 1 || cfg.VM[0].OS != "windows" {
		t.Errorf("expose = %v, vm = %+v", cfg.Expose, cfg.VM)
	}
}

func TestParseExposeAndVM(t *testing.T) {
	tests := []struct {
		name, toml, want string // want: substring of the error, "" for none
	}{
		{"expose ok", "[expose]\nweb = \"192.168.122.1:8080:80\"\n", ""},
		{"expose bad", "[expose]\nweb = \"80\"\n", "expose.web"},
		{"vm ok", "[[vm]]\nname = \"win11\"\nuser = \"admin\"\nos = \"windows\"\n", ""},
		{"vm no user", "[[vm]]\nname = \"win11\"\nos = \"linux\"\n", "vm[0].user"},
		{"vm bad os", "[[vm]]\nname = \"win11\"\nuser = \"a\"\nos = \"mac\"\n", "vm[0].os"},
		{"vm name with space", "[[vm]]\nname = \"win 11\"\nuser = \"a\"\nos = \"linux\"\n", "vm[0].name"},
		{"vm user injecting ssh config", "[[vm]]\nname = \"a\"\nuser = \"u\\nHost *\"\nos = \"linux\"\n", "vm[0].user"},
		{"vm duplicate", "[[vm]]\nname = \"a\"\nuser = \"u\"\nos = \"linux\"\n[[vm]]\nname = \"a\"\nuser = \"u\"\nos = \"linux\"\n", "vm[1].name"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse([]byte(minimalConfig + tt.toml))
			if tt.want == "" && err != nil || tt.want != "" && (err == nil || !strings.Contains(err.Error(), tt.want)) {
				t.Errorf("err = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestLabelsNames(t *testing.T) {
	cfg, err := Parse([]byte(minimalConfig))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"needs-triage", "needs-info", "ready-for-agent", "ready-for-human", "wontfix", "spec"}
	if got := cfg.Labels.Names(); !slices.Equal(got, want) {
		t.Errorf("Names = %v, want %v", got, want)
	}
}
