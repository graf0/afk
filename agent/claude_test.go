package agent

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestSessionCommand runs the remote command in a local shell, with a fake
// claude that prints what it got.
func TestSessionCommand(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "fake claude")
	script := `#!/bin/bash
printf 'token=%s\n' "$CLAUDE_CODE_OAUTH_TOKEN"
printf 'proxy=%s\n' "$https_proxy"
printf 'traffic=%s\n' "$CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC"
printf 'pwd=%s\n' "$PWD"
printf 'dburl=%s\n' "$DATABASE_URL"
printf 'arg=%s\n' "$@"
printf 'stdin=%s\n' "$(cat)"
`
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	s := Session{Token: "tok'en", Prompt: "do it\nwell", Dir: dir, Model: "opus", Proxy: "http://127.0.0.1:3128", Binary: fake, Env: map[string]string{"DATABASE_URL": "postgres:///it's $x"}}

	cmd := exec.Command("bash", "-c", s.command())
	cmd.Stdin = strings.NewReader(s.Token + "\n" + s.Prompt)
	cmd.Env = []string{"PATH=/usr/bin:/bin"}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	for _, want := range []string{
		"token=tok'en\n",
		"proxy=http://127.0.0.1:3128\n",
		"traffic=1\n",
		"dburl=postgres:///it's $x\n",
		"pwd=" + dir + "\n",
		"arg=-p\narg=--dangerously-skip-permissions\n",
		"arg=--output-format\narg=stream-json\n",
		"arg=" + Schema + "\n",
		"arg=--model\narg=opus\n",
		"arg=--disallowedTools\narg=WebFetch\narg=WebSearch\nstdin=",
		"stdin=do it\nwell\n",
	} {
		if !strings.Contains(string(out), want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(s.command(), "tok'en") {
		t.Error("token on the command line")
	}
}

// TestSandboxCommand runs the Sandbox command in a local shell: the prompt
// comes from the file, the token only from the environment, stderr joins stdout.
func TestSandboxCommand(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "fake claude")
	script := `#!/bin/bash
printf 'token=%s\n' "$CLAUDE_CODE_OAUTH_TOKEN"
printf 'traffic=%s\n' "$CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC"
printf 'arg=%s\n' "$@"
printf 'stdin=%s\n' "$(cat)"
echo oops >&2
`
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	prompt := filepath.Join(dir, "it's prompt.md")
	if err := os.WriteFile(prompt, []byte("do it\nwell"), 0o644); err != nil {
		t.Fatal(err)
	}
	command := SandboxCommand(ShellQuote(fake), prompt, "opus")
	cmd := exec.Command("bash", "-c", command)
	cmd.Env = []string{"PATH=/usr/bin:/bin", TokenEnv + "=sekret"}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	for _, want := range []string{
		"token=sekret\n", "traffic=1\n", "arg=-p\n", "arg=--model\narg=opus\n",
		"arg=--disallowedTools\narg=WebFetch\narg=WebSearch\nstdin=", "stdin=do it\nwell\n", "oops\n",
	} {
		if !strings.Contains(string(out), want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(command, "sekret") {
		t.Error("token on the command line")
	}
}

func TestExportEnv(t *testing.T) {
	if got := ExportEnv(nil); got != "" {
		t.Errorf("ExportEnv(nil) = %q", got)
	}
	out, err := exec.Command("bash", "-c", ExportEnv(map[string]string{"B": "x y", "A": "it's"})+`printf '%s|%s' "$A" "$B"`).Output()
	if err != nil || string(out) != "it's|x y" {
		t.Errorf("got %q, %v", out, err)
	}
}

func TestShellQuote(t *testing.T) {
	out, err := exec.Command("bash", "-c", "printf %s "+ShellQuote(`a'b "c" $d`)).Output()
	if err != nil || string(out) != `a'b "c" $d` {
		t.Errorf("got %q, %v", out, err)
	}
}
