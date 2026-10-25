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

func TestParseErrors(t *testing.T) {
	tests := []struct {
		name string
		toml string
		want []string // every line of the error, in order
	}{
		{"unknown key", minimalConfig + "[tool.pg]\nkind = \"vm\"\nfoo = 1\n", []string{`8:1: unknown key "tool.pg.foo"`}},
		{"unknown section", minimalConfig + "[netwrok]\nallow = []\n", []string{`6:2: unknown key "netwrok"`}},
		{"invalid toml", "[forge", []string{"1:"}},
		{"wrong type", "[forge]\nkind = 1\n", []string{"2:8: forge.kind: "}},
		{"missing forge kind", "[forge]\nrepo = \"a/b\"\n[gate]\ncmd = \"x\"\n", []string{"forge.kind: required"}},
		{"bad forge kind", "[forge]\nkind = \"gitlab\"\nrepo = \"a/b\"\n[gate]\ncmd = \"x\"\n", []string{`2:1: forge.kind: want one of [github gitea], got "gitlab"`}},
		{"bad repo", "[forge]\nkind = \"github\"\nrepo = \"x\"\n[gate]\ncmd = \"x\"\n", []string{`3:1: forge.repo: want owner/name, got "x"`}},
		{"missing repo", "[forge]\nkind = \"github\"\n[gate]\ncmd = \"x\"\n", []string{"forge.repo: required"}},
		{"gitea needs url", "[forge]\nkind = \"gitea\"\nrepo = \"a/b\"\n[gate]\ncmd = \"x\"\n", []string{"forge.url: required for gitea"}},
		{"gitea url must be https", "[forge]\nkind = \"gitea\"\nrepo = \"a/b\"\nurl = \"http://g\"\n[gate]\ncmd = \"x\"\n", []string{`4:1: forge.url: want an https URL, got "http://g"`}},
		{"github forbids url", "[forge]\nkind = \"github\"\nrepo = \"a/b\"\nurl = \"https://g\"\n[gate]\ncmd = \"x\"\n", []string{"4:1: forge.url: not allowed for github"}},
		{"empty label", minimalConfig + "[labels]\nwontfix = \"\"\n", []string{"7:1: labels.wontfix: must not be empty"}},
		{"duplicate labels", minimalConfig + "[labels]\nneeds_info = \"x\"\nwontfix = \"x\"\n", []string{`8:1: labels.wontfix: same as labels.needs_info ("x")`}},
		{"same models", minimalConfig + "[agent]\nmodel = \"m\"\nreview_model = \"m\"\n", []string{`8:1: agent.review_model: must differ from agent.model ("m")`}},
		{"bad plugin", minimalConfig + "[agent]\nplugins = [\"ok@m\", \"nomarket\"]\n", []string{`7:20: agent.plugins[1]: want name@marketplace, got "nomarket"`}},
		{"missing gate cmd", "[forge]\nkind = \"github\"\nrepo = \"a/b\"\n", []string{"gate.cmd: required"}},
		{"empty gate cmd", "[forge]\nkind = \"github\"\nrepo = \"a/b\"\n[gate]\ncmd = \" \"\n", []string{"5:1: gate.cmd: required"}},
		{"negative max_fixes", minimalConfig + "max_fixes = -1\n", []string{"6:1: gate.max_fixes: want >= 0, got -1"}},
		{"image section is gone", minimalConfig + "[image]\ntemplate = \"debian13\"\n", []string{`unknown key "image"`}},
		{"unknown machine key", minimalConfig + "[machine.sandbox]\nprovision = []\n", []string{`7:1: unknown key "machine.sandbox.provision"`}},
		{"bad max_age", minimalConfig + "[machine.sandbox]\nmax_age = \"week\"\n", []string{`7:1: machine.sandbox.max_age: want a duration like "168h" or "0", got "week"`}},
		{"negative max_age", minimalConfig + "[machine.sandbox]\nmax_age = \"-1h\"\n", []string{`7:1: machine.sandbox.max_age: want a duration`}},
		{"unknown template", minimalConfig + "[machine.sandbox]\ntemplate = \"arch\"\n", []string{`7:1: machine.sandbox.template: want one of [debian13], got "arch"`}},
		{"empty template", minimalConfig + "[machine.sandbox]\ntemplate = \"\"\n", []string{`7:1: machine.sandbox.template: required`}},
		{"zero memory", minimalConfig + "[machine.sandbox]\nmemory = 0\n", []string{`7:1: machine.sandbox.memory: want MiB > 0, got 0`}},
		{"negative memory", minimalConfig + "[machine.sandbox]\nmemory = -5\n", []string{`machine.sandbox.memory: want MiB > 0, got -5`}},
		{"zero cpus", minimalConfig + "[machine.sandbox]\ncpus = 0\n", []string{`7:1: machine.sandbox.cpus: want > 0, got 0`}},
		{"fractional memory", minimalConfig + "[machine.sandbox]\nmemory = 1.5\n", []string{`7:10: machine.sandbox.memory: `}},
		{"other machine", minimalConfig + "[machine.db]\nmemory = 512\n", []string{`machine.db: Tool VM is not supported yet`}},
		{"allow with scheme", minimalConfig + "[network]\nallow = [\"ok.com\", \"https://a.com\"]\n", []string{`7:20: network.allow[1]: invalid allowlist entry "https://a.com"`}},
		{"allow with port", minimalConfig + "[network]\nallow = [\"a.com:443\"]\n", []string{`7:10: network.allow[0]: invalid allowlist entry "a.com:443"`}},
		{"allow with path", minimalConfig + "[network]\nallow = [\"a.com/x\"]\n", []string{`7:10: network.allow[0]: invalid allowlist entry "a.com/x"`}},
		{"wildcard on ip", minimalConfig + "[network]\nallow = [\"*.10.0.0.1\"]\n", []string{`7:10: network.allow[0]: invalid allowlist entry "*.10.0.0.1": wildcard on IP address`}},
		{"multi-line array points at the element", minimalConfig + "[network]\nallow = [\n  \"ok.com\",\n  \"a.com:1\",\n]\n", []string{`9:3: network.allow[1]: invalid allowlist entry "a.com:1"`}},
		{"repo of dots", "[forge]\nkind = \"github\"\nrepo = \"../..\"\n[gate]\ncmd = \"x\"\n", []string{`3:1: forge.repo: want owner/name, got "../.."`}},
		{"repo dot segment", "[forge]\nkind = \"github\"\nrepo = \"o/.\"\n[gate]\ncmd = \"x\"\n", []string{`forge.repo: want owner/name`}},
		{"protect empty", minimalConfig + "[protect]\npaths = [\"\"]\n", []string{"7:10: protect.paths[0]: must not be empty"}},
		{"protect dotdot", minimalConfig + "[protect]\npaths = [\"a/../b\"]\n", []string{`7:10: protect.paths[0]: must not contain "..", got "a/../b"`}},
		{"protect absolute", minimalConfig + "[protect]\npaths = [\"/etc\"]\n", []string{`7:10: protect.paths[0]: must be relative, got "/etc"`}},
		{"bad tool kind", minimalConfig + "[tool.pg]\nkind = \"docker\"\n", []string{`7:1: tool.pg.kind: want one of [package host-port vm], got "docker"`}},
		{"missing tool kind", minimalConfig + "[tool.pg]\n", []string{"tool.pg.kind: required"}},
		{"inline table position", "forge = { kind = \"gitlab\", repo = \"a/b\" }\n[gate]\ncmd = \"x\"\n", []string{`1:11: forge.kind: want one of`}},
		{"all errors at once", "[forge]\nkind = \"github\"\nrepo = \"x\"\nbogus = 1\n[labels]\nwontfix = \"\"\n", []string{`4:1: unknown key "forge.bogus"`, `3:1: forge.repo`, "6:1: labels.wontfix", "gate.cmd: required"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse([]byte(tt.toml))
			if err == nil {
				t.Fatal("want error")
			}
			lines := strings.Split(err.Error(), "\n")
			if len(lines) != len(tt.want) {
				t.Fatalf("err has %d lines, want %d:\n%v", len(lines), len(tt.want), err)
			}
			for _, want := range tt.want {
				if !slices.ContainsFunc(lines, func(line string) bool { return strings.Contains(line, want) }) {
					t.Errorf("no line containing %q in:\n%v", want, err)
				}
			}
		})
	}
}

func TestParseAllowsWildcardHosts(t *testing.T) {
	_, err := Parse([]byte(minimalConfig + "[network]\nallow = [\"*.example.com\", \"10.0.0.1\"]\n"))
	if err != nil {
		t.Fatal(err)
	}
}

func TestParseNamedPrefixesEveryLine(t *testing.T) {
	_, err := ParseNamed("f.toml", []byte("[forge]\nkind = \"github\"\nrepo = \"x\"\n"))
	want := "f.toml: 3:1: forge.repo: want owner/name, got \"x\"\nf.toml: gate.cmd: required"
	if err == nil || err.Error() != want {
		t.Errorf("err = %v, want %q", err, want)
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
	cfg, err := Parse([]byte(minimalConfig + "[tool.pg]\nkind = \"vm\"\n"))
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
