package stack

import (
	"strings"
	"testing"
)

func TestParseExpose(t *testing.T) {
	tests := []struct {
		spec    string
		want    string
		wantErr string
	}{
		{spec: "8080:5173", want: "127.0.0.1:8080:5173"},
		{spec: "192.168.122.1:8081:8080", want: "192.168.122.1:8081:8080"},
		{spec: "0.0.0.0:2222:22", want: "0.0.0.0:2222:22"},
		{spec: "08080:+80", want: "127.0.0.1:8080:80"},
		{spec: "::1:8080:80", wantErr: "want [address:]host_port:container_port"}, // IPv6: not supported
		{spec: "8080", wantErr: "want [address:]host_port:container_port"},
		{spec: "x:5173", wantErr: "port"},
		{spec: "0:5173", wantErr: "port"},
		{spec: "8080:70000", wantErr: "port"},
		{spec: "nope:8080:80", wantErr: "address"},
		{spec: "1:2:3:4", wantErr: "want [address:]host_port:container_port"},
	}
	for _, tt := range tests {
		t.Run(tt.spec, func(t *testing.T) {
			got, err := ParseExpose(tt.spec)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Errorf("ParseExpose(%q) = %q, %v, want %q", tt.spec, got, err, tt.want)
			}
		})
	}
}

func TestImageTag(t *testing.T) {
	build := map[string]any{"context": "/export/ctx", "dockerfile": "Dockerfile.afk", "args": map[string]any{"A": "b"}}

	tag, err := imageTag("proj", "Afk", "/export", build, "tree1", "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(tag, "localhost/afk/proj/afk:") {
		t.Errorf("tag = %q, want localhost/afk/proj/afk:<hash>", tag)
	}

	same, _ := imageTag("proj", "Afk", "/export", build, "tree1", "")
	if same != tag {
		t.Errorf("same definition gave %q and %q", tag, same)
	}
	moved, _ := imageTag("proj", "Afk", "/other", map[string]any{"context": "/other/ctx", "dockerfile": "Dockerfile.afk", "args": map[string]any{"A": "b"}}, "tree1", "")
	if moved != tag {
		t.Errorf("export directory leaks into the tag: %q vs %q", moved, tag)
	}
	if changed, _ := imageTag("proj", "Afk", "/export", build, "tree2", ""); changed == tag {
		t.Error("changed context tree kept the tag")
	}
	if changed, _ := imageTag("proj", "Afk", "/export", build, "tree1", "blob"); changed == tag {
		t.Error("Dockerfile outside the context kept the tag")
	}
	args, _ := imageTag("proj", "Afk", "/export", map[string]any{"context": "/export/ctx", "dockerfile": "Dockerfile.afk", "args": map[string]any{"A": "c"}}, "tree1", "")
	if args == tag {
		t.Error("changed build arg kept the tag")
	}
}

func TestImageTagContextOutsideExport(t *testing.T) {
	_, err := imageTag("proj", "afk", "/export", map[string]any{"context": "/etc"}, "", "")
	if err == nil || !strings.Contains(err.Error(), "outside") {
		t.Errorf("err = %v, want context outside the export", err)
	}
}

func TestOverride(t *testing.T) {
	data, err := overrideFile(overrideInput{
		Tree:     "/state/p/chats/c/repo",
		Home:     "/state/p/chats/c/home",
		HostHome: "/home/u",
		EnvFile:  "/state/p/chats/c/agent.env",
		Mounts:   []Mount{{Source: "/usr/bin/claude", Target: "/home/u/bin/claude", ReadOnly: true}},
		Ports:    map[string][]string{"web": {"127.0.0.1:8080:5173"}, "db": nil},
		Images:   map[string]string{"afk": "localhost/afk/p/afk:abc", "web": "localhost/afk/p/web:def"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"afk":`, `image: "localhost/afk/p/afk:abc"`, "userns_mode: keep-id", "init: true",
		`command: ["sleep", "infinity"]`, `working_dir: "/state/p/chats/c/repo"`,
		`env_file: [{"path":"/state/p/chats/c/agent.env","required":false}]`,
		`"target":"/home/u/bin/claude"`, `"read_only":true`,
		`ports: !override ["127.0.0.1:8080:5173"]`, `ports: !override []`,
	} {
		if !strings.Contains(string(data), want) {
			t.Errorf("override lacks %q:\n%s", want, data)
		}
	}
	if got := strings.Count(string(data), "- {"); got != 3 {
		t.Errorf("%d volumes, want 3 (tree, home, claude)", got)
	}
	if got := strings.Count(string(data), "userns_mode"); got != 1 {
		t.Errorf("userns_mode on %d services, want only the Sandbox", got)
	}
}

func TestOverrideEscapesInterpolation(t *testing.T) {
	data, err := overrideFile(overrideInput{Tree: "/state/$HOME/repo", Home: "/h", HostHome: "/home/u", EnvFile: "/e"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "/state/$$HOME/repo") {
		t.Errorf("override does not escape $: %s", data)
	}
}
