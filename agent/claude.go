// Package agent runs the Agent (Claude Code) in a Sandbox: prompt, session,
// report.
package agent

import "strings"

// disallowedTools are off: a fetched page is a prompt-injection channel
// (docs/workflow.md).
var disallowedTools = []string{"WebFetch", "WebSearch"}

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

// ShellQuote quotes s for a POSIX shell.
func ShellQuote(s string) (quoted string) {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
