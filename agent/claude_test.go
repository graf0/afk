package agent

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

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

func TestShellQuote(t *testing.T) {
	out, err := exec.Command("bash", "-c", "printf %s "+ShellQuote(`a'b "c" $d`)).Output()
	if err != nil || string(out) != `a'b "c" $d` {
		t.Errorf("got %q, %v", out, err)
	}
}
