package stack

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestDockerfilePath(t *testing.T) {
	tests := []struct {
		name       string
		build      map[string]any
		want       string
		wantInline bool
		wantErr    bool
	}{
		{name: "default", build: map[string]any{"context": "/e/.afk"}, want: ".afk/Dockerfile"},
		{name: "named", build: map[string]any{"context": "/e", "dockerfile": "docker/Dockerfile.dev"}, want: "docker/Dockerfile.dev"},
		{name: "outside the context", build: map[string]any{"context": "/e/app", "dockerfile": "../ci/Dockerfile"}, want: "ci/Dockerfile"},
		{name: "absolute", build: map[string]any{"context": "/e/app", "dockerfile": "/e/x/Dockerfile"}, want: "x/Dockerfile"},
		{name: "inline", build: map[string]any{"context": "/e", "dockerfile_inline": "FROM scratch"}, wantInline: true},
		{name: "outside the export", build: map[string]any{"context": "/e", "dockerfile": "../x/Dockerfile"}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, inline, err := dockerfilePath("/e", tt.build)
			if got != tt.want || inline != tt.wantInline || (err != nil) != tt.wantErr {
				t.Errorf("dockerfilePath = %q, %v, %v; want %q, %v, error=%v", got, inline, err, tt.want, tt.wantInline, tt.wantErr)
			}
		})
	}
}

func TestProtectedDockerfiles(t *testing.T) {
	services := map[string]composeService{
		"web":    {Build: map[string]any{"context": "/e", "dockerfile": "docker/Dockerfile.web"}},
		"same":   {Build: map[string]any{"context": "/e", "dockerfile": "docker/Dockerfile.web"}},
		"tool":   {Build: map[string]any{"context": "/e/tools"}},
		"inline": {Build: map[string]any{"context": "/e", "dockerfile_inline": "FROM scratch"}},
		"image":  {},
	}
	got, err := protectedDockerfiles("/e", services)
	if want := []string{"docker/Dockerfile.web", "tools/Dockerfile"}; err != nil || !slices.Equal(got, want) {
		t.Errorf("protectedDockerfiles = %v, %v; want %v", got, err, want)
	}
	services["bad"] = composeService{Build: map[string]any{"context": "/e", "dockerfile": "../x/Dockerfile"}}
	if _, err = protectedDockerfiles("/e", services); err == nil || !strings.Contains(err.Error(), "bad") {
		t.Errorf("Dockerfile outside the export: err = %v, want an error naming the service", err)
	}
}

func TestRejectExternalFiles(t *testing.T) {
	tests := []struct {
		name, compose, afk, want string // want: substring of the error, "" for none
	}{
		{"plain", "services:\n  a:\n    image: x\n    extends:\n      service: b\n", "services: {}\n", ""},
		{"include", "include:\n  - other.yml\nservices: {}\n", "services: {}\n", "include"},
		{"quoted include", "\"include\": [x.yml]\n", "services: {}\n", "include"},
		{"include in compose.afk.yml", "services: {}\n", "include: [x.yml]\n", "compose.afk.yml: include"},
		{"extends with file", "services:\n  a:\n    extends:\n      file: common.yml\n      service: b\n", "services: {}\n", "extends"},
		{"flow extends with file", "services:\n  a: {extends: {file: c.yml, service: b}}\n", "services: {}\n", "extends"},
		{"file without extends", "services:\n  a:\n    configs: [{source: c, file: x}]\n", "services: {}\n", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			for name, content := range map[string]string{composeFile: tt.compose, composeAfkFile: tt.afk} {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			err := rejectExternalFiles(dir)
			if tt.want == "" && err != nil || tt.want != "" && (err == nil || !strings.Contains(err.Error(), tt.want)) {
				t.Errorf("err = %v, want %q", err, tt.want)
			}
		})
	}
}
