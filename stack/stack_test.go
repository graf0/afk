package stack

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testOptions(t *testing.T) (opts Options) {
	t.Helper()
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	return Options{Project: "owner-name", Kind: Chat, Name: "plan", StateDir: t.TempDir(), Commit: strings.Repeat("a", 40)}
}

func TestNewNames(t *testing.T) {
	chat, err := New(testOptions(t))
	if err != nil {
		t.Fatal(err)
	}
	opts := testOptions(t)
	opts.Kind = Run
	run, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	if chat.ProjectName() != "afk_owner-name_chat_plan" || run.ProjectName() != "afk_owner-name_run_plan" {
		t.Errorf("project names = %q, %q", chat.ProjectName(), run.ProjectName())
	}
	if chat.cacheScope() != chat.ProjectName() || run.cacheScope() != "afk_owner-name_factory" {
		t.Errorf("cache scopes = %q, %q", chat.cacheScope(), run.cacheScope())
	}
	other := testOptions(t)
	other.Name = "plan2"
	second, err := New(other)
	if err != nil {
		t.Fatal(err)
	}
	if second.ProjectName() == chat.ProjectName() || second.Tree() == chat.Tree() {
		t.Error("two Chats of a Project share a compose project or a tree")
	}
	if !strings.HasPrefix(chat.Tree(), filepath.Join(chat.opts.StateDir, "owner-name", "chats", "plan")) {
		t.Errorf("tree %s outside the state directory", chat.Tree())
	}
}

func TestNewRejects(t *testing.T) {
	tests := map[string]func(o *Options){
		"project":  func(o *Options) { o.Project = "Owner/Name" },
		"name":     func(o *Options) { o.Name = "../x" },
		"kind":     func(o *Options) { o.Kind = "main" },
		"env":      func(o *Options) { o.Env = map[string]string{"A": "x\nB=y"} },
		"env key":  func(o *Options) { o.Env = map[string]string{"A=B": "x"} },
		"trailing": func(o *Options) { o.Name = "plan-" },
		"dollar":   func(o *Options) { o.Env = map[string]string{"A": "a$b"} },
		"commit":   func(o *Options) { o.Commit = "main" },
		"option":   func(o *Options) { o.Commit = "--output=x" },
		"relative": func(o *Options) { o.StateDir = "state" },
		"socket":   func(o *Options) { o.Mounts = []Mount{{Source: os.Getenv("XDG_RUNTIME_DIR"), Target: "/x"}} },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			opts := testOptions(t)
			mutate(&opts)
			if _, err := New(opts); err == nil {
				t.Error("New accepted it")
			}
		})
	}
}

func TestUpWithoutComposeFile(t *testing.T) {
	repo, commit := gitRepo(t, map[string]string{composeFile: "services: {}\n"})
	opts := testOptions(t)
	opts.Repo, opts.Commit = repo, commit
	s, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	err = s.Up(t.Context())
	if err == nil || !strings.Contains(err.Error(), composeAfkFile) {
		t.Errorf("err = %v, want one naming %s", err, composeAfkFile)
	}
}

func TestProjectNamesAreUnambiguous(t *testing.T) {
	a := testOptions(t)
	a.Project, a.Kind, a.Name = "a-chat", Run, "x"
	b := testOptions(t)
	b.Project, b.Kind, b.Name = "a", Chat, "run-x"
	first, err := New(a)
	if err != nil {
		t.Fatal(err)
	}
	second, err := New(b)
	if err != nil {
		t.Fatal(err)
	}
	if first.ProjectName() == second.ProjectName() {
		t.Errorf("both are %q", first.ProjectName())
	}
}

func TestWriteEnv(t *testing.T) {
	opts := testOptions(t)
	opts.Env = map[string]string{"TOKEN": "secret"}
	s, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(s.dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err = s.writeEnv(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(s.envPath())
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("env file: %v, %v, want mode 0600", info, err)
	}
	if err = s.removeEnv(); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(s.envPath()); err == nil {
		t.Error("env file survived removeEnv")
	}
	if err = s.removeEnv(); err != nil {
		t.Errorf("removing a missing env file: %v", err)
	}
}
