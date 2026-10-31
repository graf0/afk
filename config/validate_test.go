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
		{"unknown key", minimalConfig + "[agent]\nfoo = 1\n", []string{`7:1: unknown key "agent.foo"`}},
		{"unknown section", minimalConfig + "[netwrok]\nallow = []\n", []string{`6:2: unknown key "netwrok"`}},
		{"machine section is gone", minimalConfig + "[machine.sandbox]\nmemory = 4096\n", []string{`6:2: unknown key "machine.sandbox" (removed: the Sandbox is defined by compose.afk.yml)`}},
		{"network section is gone", minimalConfig + "[network]\nallow = [\"proxy.golang.org\"]\n", []string{`6:2: unknown key "network" (removed: the Sandbox has no egress allowlist)`}},
		{"tool section is gone", minimalConfig + "[tool.pg]\nprompt = \"p\"\n", []string{`6:2: unknown key "tool.pg" (removed: Tools are services of compose.yml and compose.afk.yml)`}},
		{"image section is gone", minimalConfig + "[image]\ntemplate = \"debian13\"\n", []string{`unknown key "image"`}},
		{"repo of dots", "[forge]\nkind = \"github\"\nrepo = \"../..\"\n[gate]\ncmd = \"x\"\n", []string{`3:1: forge.repo: want owner/name, got "../.."`}},
		{"repo dot segment", "[forge]\nkind = \"github\"\nrepo = \"o/.\"\n[gate]\ncmd = \"x\"\n", []string{`forge.repo: want owner/name`}},
		{"protect empty", minimalConfig + "[protect]\npaths = [\"\"]\n", []string{"7:10: protect.paths[0]: must not be empty"}},
		{"protect dotdot", minimalConfig + "[protect]\npaths = [\"a/../b\"]\n", []string{`7:10: protect.paths[0]: must not contain "..", got "a/../b"`}},
		{"protect absolute", minimalConfig + "[protect]\npaths = [\"/etc\"]\n", []string{`7:10: protect.paths[0]: must be relative, got "/etc"`}},
		{"expose bad port", minimalConfig + "[expose]\nweb = \"8080:0\"\n", []string{`7:1: expose.web: expose "8080:0": port "0" is not 1-65535`}},
		{"expose bad address", minimalConfig + "[expose]\nweb = \"localhost:8080:80\"\n", []string{`7:1: expose.web: expose "localhost:8080:80": address`}},
		{"expose wrong type", minimalConfig + "[expose]\nweb = 8080\n", []string{"7:7: expose.web: "}},
		{"expose same host port", minimalConfig + "[expose]\napi = \"8080:80\"\nweb = \"0.0.0.0:8080:81\"\n", []string{"8:1: expose.web: host port 8080 same as expose.api"}},
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

func TestParseNamedPrefixesEveryLine(t *testing.T) {
	_, err := ParseNamed("f.toml", []byte("[forge]\nkind = \"github\"\nrepo = \"x\"\n"))
	want := "f.toml: 3:1: forge.repo: want owner/name, got \"x\"\nf.toml: gate.cmd: required"
	if err == nil || err.Error() != want {
		t.Errorf("err = %v, want %q", err, want)
	}
}
