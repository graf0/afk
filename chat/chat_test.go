package chat

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/graf0/afk/forge"
	"github.com/graf0/afk/machine"
	"github.com/graf0/afk/run"
)

func TestValidateName(t *testing.T) {
	for name, ok := range map[string]bool{
		"plan": true, "a": true, "x-1": true, "0": true, strings.Repeat("a", 32): true,
		"": false, "-x": false, "Plan": false, "a_b": false, "a b": false, "a/b": false, "ż": false,
		strings.Repeat("a", 33): false,
	} {
		if err := ValidateName(name); (err == nil) != ok {
			t.Errorf("ValidateName(%q) = %v, want ok=%t", name, err, ok)
		}
	}
}

func TestValidateEffort(t *testing.T) {
	for effort, ok := range map[string]bool{"": true, "low": true, "xhigh": true, "max": true, "extreme": false, "High": false} {
		if err := ValidateEffort(effort); (err == nil) != ok {
			t.Errorf("ValidateEffort(%q) = %v, want ok=%t", effort, err, ok)
		}
	}
}

func TestSessionCommand(t *testing.T) {
	s := session{Env: map[string]string{"DB": "pg://x y"}, Model: "opus", Effort: "high", Prompt: "it's a prompt"}
	got := s.command()
	for _, want := range []string{
		"export DB='pg://x y' && export CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1",
		"https_proxy='http://127.0.0.1:3128'",
		"IFS= read -r CLAUDE_CODE_OAUTH_TOKEN",
		"stty echo",
		"cd '/work/repo' && exec 'claude' '--dangerously-skip-permissions' '--model' 'opus' '--effort' 'high' '--append-system-prompt' 'it'\\''s a prompt'",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("command lacks %q:\n%s", want, got)
		}
	}
	// The token is read before anything of the Agent runs.
	if strings.Index(got, "read -r") > strings.Index(got, "exec ") {
		t.Errorf("token read after exec:\n%s", got)
	}
	// Model and effort are left out when not set; Command replaces the claude line.
	got = session{Command: []string{"claude", "--version"}}.command()
	if !strings.HasSuffix(got, "&& exec 'claude' '--version'") || strings.Contains(got, "--effort") {
		t.Errorf("command = %s", got)
	}
	got = session{Prompt: "p"}.command()
	if strings.Contains(got, "--model") || strings.Contains(got, "--effort") {
		t.Errorf("default command passes model or effort: %s", got)
	}
}

func TestSystemPrompt(t *testing.T) {
	got := systemPrompt("plan", "main")
	for _, want := range []string{`"plan"`, "origin/main", "chat-plan", "afk-forge", "afk chat export", "/work/repo"} {
		if !strings.Contains(got, want) {
			t.Errorf("prompt lacks %q:\n%s", want, got)
		}
	}
}

func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func TestCheckNew(t *testing.T) {
	root := t.TempDir()
	remoteDir := filepath.Join(root, "origin.git")
	work := filepath.Join(root, "work")
	for _, dir := range []string{remoteDir, work} {
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	gitIn(t, remoteDir, "init", "-q", "--bare", "-b", "main")
	gitIn(t, work, "init", "-q", "-b", "chat-taken")
	gitIn(t, work, "commit", "-q", "--allow-empty", "-m", "x")
	gitIn(t, work, "push", "-q", remoteDir, "chat-taken")
	remote := forge.Remote{Repo: "o/r", URL: remoteDir}

	quarantine, err := run.OpenQuarantine(t.Context(), filepath.Join(root, "quarantine.git"))
	if err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Join(root, "chats")
	if err = os.MkdirAll(filepath.Join(stateDir, "has-state"), 0o755); err != nil {
		t.Fatal(err)
	}
	machines := []machine.Machine{
		{Name: "afk-chat-has-vm", Kind: machine.KindChat, ID: "has-vm"},
		{Name: "afk-run-fresh", Kind: machine.KindRun, ID: "fresh"}, // a Run's id is no Chat's
	}
	for name, want := range map[string]string{
		"fresh":     "",
		"Bad Name":  "invalid Chat name",
		"has-vm":    "already exists (VM afk-chat-has-vm)",
		"has-state": "already exists",
		"taken":     "branch chat-taken already exists on o/r",
	} {
		err := checkNew(t.Context(), name, machines, filepath.Join(stateDir, name), quarantine, remote)
		if (want == "") != (err == nil) || (err != nil && !strings.Contains(err.Error(), want)) {
			t.Errorf("checkNew(%q) = %v, want %q", name, err, want)
		}
	}
}
