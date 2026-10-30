// Package agent runs the Agent (Claude Code) in a Sandbox: prompt, session,
// report.
package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"

	"golang.org/x/crypto/ssh"
)

// disallowedTools are off in a Run: the network is an allowlist anyway, and a
// fetched page is a prompt-injection channel (docs/workflow.md).
var disallowedTools = []string{"WebFetch", "WebSearch"}

// Domains are the hosts Claude Code needs, allowed in every Run and in
// `afk vm shell` next to [network].allow. With nonessential traffic disabled
// (telemetry, error reporting, auto-update) and a long-lived OAuth token,
// Claude Code talks only to the API.
var Domains = []string{"api.anthropic.com"}

// Session is one Claude Code run in the guest.
type Session struct {
	Token  string // Claude Code OAuth token
	Prompt string
	Dir    string            // working directory in the guest
	Model  string            // empty: Claude Code's default
	Proxy  string            // Egress proxy URL in the guest
	Binary string            // "claude" unless set (tests)
	Env    map[string]string // [tool.<name>].env, set before afk's own variables
}

// Run runs the session on client. The token reaches the guest only on the
// session's stdin, read into the environment by the remote shell: never on a
// command line, never on disk (Debian's sshd does not accept SetEnv). The
// prompt follows on stdin. The Agent's lines go to out, the raw stream and
// stderr to log. Cancelling ctx closes the session.
func (s Session) Run(ctx context.Context, client *ssh.Client, out, log io.Writer) (report Report, err error) {
	session, err := client.NewSession()
	if err != nil {
		err = fmt.Errorf("opening agent session: %w", err)
		return
	}
	defer func() { _ = session.Close() }()
	defer context.AfterFunc(ctx, func() { _ = session.Close() })()

	if err = checkToken(s.Token); err != nil {
		return
	}
	session.Stdin = io.MultiReader(strings.NewReader(s.Token+"\n"), strings.NewReader(s.Prompt))
	session.Stderr = log
	stdout, err := session.StdoutPipe()
	if err != nil {
		err = fmt.Errorf("agent stdout: %w", err)
		return
	}
	if err = session.Start(s.command()); err != nil {
		err = fmt.Errorf("starting agent: %w", err)
		return
	}
	report, streamErr := Stream(stdout, out, log)
	if streamErr != nil {
		_ = session.Close() // stop the agent: its output is unusable or too large
	}
	if waitErr := session.Wait(); waitErr != nil {
		// The exit message comes from the guest.
		err = fmt.Errorf("agent exited: %s", Sanitize(waitErr.Error()))
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		err = fmt.Errorf("agent interrupted: %w", ctxErr)
	}
	err = errors.Join(err, streamErr)
	return
}

// command is the remote shell command of the session.
func (s Session) command() (command string) {
	binary := s.Binary
	if binary == "" {
		binary = "claude"
	}
	args := claudeArgs(binary, s.Model)

	env := []string{"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1"}
	for _, name := range []string{"http_proxy", "https_proxy", "HTTP_PROXY", "HTTPS_PROXY"} {
		env = append(env, name+"="+ShellQuote(s.Proxy))
	}
	quoted := make([]string, len(args))
	for i, arg := range args {
		quoted[i] = ShellQuote(arg)
	}
	return ExportEnv(s.Env) + "export " + strings.Join(env, " ") +
		" && IFS= read -r " + TokenEnv + " && export " + TokenEnv +
		" && cd " + ShellQuote(s.Dir) +
		" && exec " + strings.Join(quoted, " ")
}

// claudeArgs is the command line of a session: the prompt comes on stdin.
func claudeArgs(binary, model string) (args []string) {
	args = []string{binary, "-p", "--dangerously-skip-permissions", "--verbose",
		"--output-format", "stream-json", "--json-schema", Schema}
	if model != "" {
		args = append(args, "--model", model)
	}
	// Variadic: must stay last, the prompt comes on stdin.
	args = append(args, "--disallowedTools")
	args = append(args, disallowedTools...)
	return
}

// SandboxCommand is the shell command of one session in the Sandbox of a
// Stack: bash runs it in the clone. The token is in the Sandbox environment
// already (Stack's env file), so nothing secret is on the command line. The
// prompt is read from promptFile; stderr goes to the stream, for the log.
// binary is a shell word, quoted by the caller when it needs it.
func SandboxCommand(binary, promptFile, model string) (command string) {
	args := claudeArgs(binary, model)
	for i := 1; i < len(args); i++ {
		args[i] = ShellQuote(args[i])
	}
	return "export CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1 && exec " + strings.Join(args, " ") +
		" < " + ShellQuote(promptFile) + " 2>&1"
}

// ExportEnv is a shell prefix exporting env in name order, or empty without
// any. The names must be valid shell names (config checks that); the values
// are quoted.
func ExportEnv(env map[string]string) (prefix string) {
	for _, name := range slices.Sorted(maps.Keys(env)) {
		prefix += "export " + name + "=" + ShellQuote(env[name]) + " && "
	}
	return
}

// ShellQuote quotes s for a POSIX shell.
func ShellQuote(s string) (quoted string) {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
