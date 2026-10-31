package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/graf0/afk/config"
)

func TestParseRemote(t *testing.T) {
	tests := []struct {
		remote string
		want   config.Forge
		ok     bool
	}{
		{"https://github.com/o/r.git", config.Forge{Kind: "github", Repo: "o/r"}, true},
		{"https://github.com/o/r", config.Forge{Kind: "github", Repo: "o/r"}, true},
		{"git@github.com:o/r.git", config.Forge{Kind: "github", Repo: "o/r"}, true},
		{"ssh://git@github.com/o/r.git", config.Forge{Kind: "github", Repo: "o/r"}, true},
		{"ssh://git@git.example.com:2222/o/r.git", config.Forge{Kind: "gitea", Repo: "o/r", URL: "https://git.example.com"}, true},
		{"https://git.example.com/o/r.git", config.Forge{Kind: "gitea", Repo: "o/r", URL: "https://git.example.com"}, true},
		{"git@git.example.com:o/r.git", config.Forge{Kind: "gitea", Repo: "o/r", URL: "https://git.example.com"}, true},
		{"", config.Forge{}, false},
		{"/srv/repo.git", config.Forge{}, false},
		{"https://git.example.com/a/b/c.git", config.Forge{}, false},
	}
	for _, tt := range tests {
		got, ok := parseRemote(tt.remote)
		if got != tt.want || ok != tt.ok {
			t.Errorf("parseRemote(%q) = %+v, %v; want %+v, %v", tt.remote, got, ok, tt.want, tt.ok)
		}
	}
}

func TestRenderConfig(t *testing.T) {
	tests := []struct {
		name  string
		forge config.Forge
		cmd   string
	}{
		{"github", config.Forge{Kind: "github", Repo: "o/r"}, "go test ./..."},
		{"gitea", config.Forge{Kind: "gitea", Repo: "o/r", URL: "https://g.example"}, `sh -c "make test" && echo 'ok'`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := renderConfig(tt.forge, tt.cmd)
			if err != nil {
				t.Fatal(err)
			}
			cfg, err := config.Parse(data)
			if err != nil {
				t.Fatalf("%v\n%s", err, data)
			}
			if cfg.Forge != tt.forge || cfg.Gate.Cmd != tt.cmd {
				t.Errorf("forge = %+v, cmd = %q", cfg.Forge, cfg.Gate.Cmd)
			}
		})
	}
}

func TestRunInitKeepsExistingFiles(t *testing.T) {
	root := workTree(t)
	if err := os.WriteFile(filepath.Join(root, config.FileName), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := runInit(t.Context(), root, &out); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(filepath.Join(root, config.FileName)); string(data) != "x" {
		t.Errorf(".afk.toml overwritten: %q", data)
	}
	if !strings.Contains(out.String(), "not overwritten") || !strings.Contains(out.String(), "created compose.afk.yml, .afk") {
		t.Errorf("out = %q", out.String())
	}
}
