package config

import (
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// minimalConfig is the smallest valid Project config.
const minimalConfig = "[forge]\nkind = \"github\"\nrepo = \"o/r\"\n[gate]\ncmd = \"true\"\n"

var requiredProtect = []string{".afk.toml", ".github/", ".gitea/", "**/.claude/", "**/.mcp.json"}

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
	wantSandbox := Machine{Template: "debian13", MaxAge: "168h", MemoryMiB: 2048, CPUs: 2}
	if !machineEqual(cfg.Machine.Sandbox, wantSandbox) {
		t.Errorf("sandbox = %+v, want %+v", cfg.Machine.Sandbox, wantSandbox)
	}
	if len(cfg.Network.Allow) != 0 || len(cfg.Agent.Plugins) != 0 || len(cfg.Agent.Marketplaces) != 0 {
		t.Errorf("allow/plugins/marketplaces should be empty: %v %v %v", cfg.Network.Allow, cfg.Agent.Plugins, cfg.Agent.Marketplaces)
	}
	if !slices.Equal(cfg.Protect.Paths, requiredProtect) {
		t.Errorf("protect.paths = %v", cfg.Protect.Paths)
	}
}

func machineEqual(a, b Machine) (equal bool) {
	return a.Template == b.Template && a.MaxAge == b.MaxAge && a.MemoryMiB == b.MemoryMiB && a.CPUs == b.CPUs && slices.Equal(a.Build, b.Build)
}

func TestParseMachine(t *testing.T) {
	t.Run("overrides keep the other defaults", func(t *testing.T) {
		cfg, err := Parse([]byte(minimalConfig + "[machine.sandbox]\nbuild = [\"a\", \"b\"]\nmemory = 4096\nmax_age = \"0\"\n"))
		if err != nil {
			t.Fatal(err)
		}
		want := Machine{Template: "debian13", Build: []string{"a", "b"}, MaxAge: "0", MemoryMiB: 4096, CPUs: 2}
		if got := cfg.Machine.Sandbox; !machineEqual(got, want) {
			t.Errorf("sandbox = %+v, want %+v", got, want)
		}
	})
	t.Run("cpus", func(t *testing.T) {
		cfg, err := Parse([]byte(minimalConfig + "[machine.sandbox]\ncpus = 6\n"))
		if err != nil {
			t.Fatal(err)
		}
		if got := cfg.Machine.Sandbox; got.CPUs != 6 || got.MemoryMiB != 2048 {
			t.Errorf("sandbox = %+v", got)
		}
	})
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
[network]
allow = ["proxy.golang.org"]
[tool.postgres]
build = ["apt-get install postgresql"]
setup = ["migrate", "seed"]
env = { DATABASE_URL = "postgres:///app" }
prompt = "Postgres"
[tool.redis]
on = "sandbox"
[tool.redis.env]
REDIS_URL = "redis://localhost"
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
	if cfg.Tool["postgres"].On != "sandbox" || cfg.Tool["postgres"].Prompt != "Postgres" || !slices.Equal(cfg.Tool["postgres"].Build, []string{"apt-get install postgresql"}) || cfg.Tool["redis"].On != "sandbox" {
		t.Errorf("tool = %+v", cfg.Tool)
	}
	if pg := cfg.Tool["postgres"]; !slices.Equal(pg.Setup, []string{"migrate", "seed"}) || pg.Env["DATABASE_URL"] != "postgres:///app" {
		t.Errorf("postgres = %+v", pg)
	}
	if want := map[string]string{"DATABASE_URL": "postgres:///app", "REDIS_URL": "redis://localhost"}; !maps.Equal(cfg.ToolEnv(), want) {
		t.Errorf("ToolEnv = %v, want %v", cfg.ToolEnv(), want)
	}
	if got := cfg.Tool["postgres"].EnvNames(); !slices.Equal(got, []string{"DATABASE_URL"}) || cfg.Tool["x"].EnvNames() != nil {
		t.Errorf("EnvNames = %v", got)
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
		if err == nil || !strings.Contains(err.Error(), filepath.Join(dir, FileName)) {
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
	cfg, err := Parse([]byte(minimalConfig + "[tool.pg]\nprompt = \"p\"\n"))
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
	if !slices.Equal(again.Protect.Paths, cfg.Protect.Paths) || again.Tool["pg"].On != cfg.Tool["pg"].On || again.Tool["pg"].Prompt != "p" || again.Gate != cfg.Gate {
		t.Errorf("round trip mismatch:\n%s", out)
	}
}

func mustMkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

// TestParseReadmeExamples keeps the .afk.toml examples of the README valid:
// the Getting Started one and the full one before the reference.
func TestParseReadmeExamples(t *testing.T) {
	readme, err := os.ReadFile(filepath.Join("..", "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	var configs []Config
	rest := string(readme)
	for {
		_, block, found := strings.Cut(rest, "```toml\n")
		if !found {
			break
		}
		var example string
		example, rest, _ = strings.Cut(block, "```")
		cfg, err := Parse([]byte(example))
		if err != nil {
			t.Fatalf("README example %d: %v", len(configs)+1, err)
		}
		configs = append(configs, cfg)
	}
	if len(configs) != 2 {
		t.Fatalf("%d toml blocks in README.md, want 2", len(configs))
	}
	cfg := configs[1]
	if len(cfg.Tool["postgres"].Setup) != 2 || cfg.Tool["postgres"].Env["DATABASE_URL"] == "" || cfg.Tool["playwright"].Env["PLAYWRIGHT_BROWSERS_PATH"] == "" {
		t.Errorf("tools = %+v", cfg.Tool)
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
