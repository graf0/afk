package config

import (
	"slices"
	"strings"
	"testing"
)

func TestParseErrors(t *testing.T) {
	tests := []struct {
		name string
		toml string
		want []string // every line of the error, in order
	}{
		{"unknown key", minimalConfig + "[tool.pg]\nfoo = 1\n", []string{`7:1: unknown key "tool.pg.foo"`}},
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
		{"duplicate labels ignoring case", minimalConfig + "[labels]\nneeds_info = \"x\"\nwontfix = \"X\"\n", []string{`8:1: labels.wontfix: same as labels.needs_info ("X")`}},
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
		{"tool kind is gone", minimalConfig + "[tool.pg]\nkind = \"package\"\n", []string{`7:1: unknown key "tool.pg.kind"`}},
		{"tool on other machine", minimalConfig + "[tool.pg]\non = \"db\"\n", []string{`7:1: tool.pg.on: want "sandbox"`}},
		{"env collision", minimalConfig + "[tool.a]\nenv = { X = \"1\" }\n[tool.b]\nenv = { Y = \"1\", X = \"2\" }\n", []string{`9:18: tool.b.env.X: already defined by tool.a.env`}},
		{"env collision in a table", minimalConfig + "[tool.a.env]\nX = \"1\"\n[tool.b.env]\nX = \"2\"\n", []string{`9:1: tool.b.env.X: already defined by tool.a.env`}},
		{"env name is no shell name", minimalConfig + "[tool.a]\nenv = { \"A-B\" = \"1\", \"1A\" = \"2\" }\n", []string{`tool.a.env.1A: want a shell variable name`, `tool.a.env.A-B: want a shell variable name`}},
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
