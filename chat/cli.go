package chat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"slices"
	"strings"
	"syscall"
	"time"
)

const (
	// CLIBinary is the name `afk` runs as in the Sandbox, and in $PATH there:
	// the host's binary, mounted read-only (ADR-0012).
	CLIBinary = "afk-ai"

	// protectedNote ends the notes of the commands that push the clone.
	protectedNote = "a change to a protected path ([protect] of .afk.toml, .afk.toml, compose files, the Stack's Dockerfiles, .afk/, .github/, .gitea/, .claude/, .mcp.json) refuses the whole push, listing the commits and paths"

	// cliFlagsNote ends the help: what the text and label flags take.
	cliFlagsNote = "--body-file - reads the text from stdin; --label, --add-label and --remove-label may repeat or list labels with commas. Output is plain text.\n"
)

// errCLIUsage answers arguments afk-ai does not take; the help of the Chat follows it.
var errCLIUsage = errors.New("wrong arguments; the commands of this Chat")

// helpCall asks the channel for the help of its Chat.
var helpCall = cliCall{method: http.MethodGet, path: "/"}

// cliCommands are the commands of afk-ai: what the help shows, to a Chat
// that has what they need, and how their arguments become a call. The one
// source of both, so a command never lacks its description.
var cliCommands = []cliCommand{
	{name: "issue view", args: "<n>", note: "the ticket with its comments, as the TICKET.md of a Run", when: hasForgeAPI, parse: parseIssueView},
	{name: "issue list", args: "[--label <label>] [--state open|closed|all]",
		note: "default --state open; prints #N  [labels]  title", when: hasForgeAPI, parse: parseIssueList},
	{name: "issue create", args: "--title <title> [--body <text> | --body-file -] [--label <a,b>]...",
		note: "ready-for-agent becomes needs-triage: a human makes a ticket ready for the Factory, so tell them which you think are; afk adds the footer naming this Chat, do not write it", when: hasForgeAPI, parse: parseIssueCreate},
	{name: "issue comment", args: "<n> (--body <text> | --body-file -)",
		note: "any issue; afk adds the footer; on a ready-for-agent one the comment goes to the Agent of the Factory's next Run on it", when: hasForgeAPI, parse: parseIssueComment},
	{name: "issue edit", args: "<n> [--add-label <a,b>]... [--remove-label <a,b>]... [--title <title>] [--body <text> | --body-file -]",
		note: "labels on any issue (ready-for-agent becomes needs-triage); --title and --body only on issues created by this Chat that are not ready-for-agent", when: hasForgeAPI, parse: parseIssueEdit},
	{name: "pr create", args: "--title <title> [--body <text> | --body-file -]",
		note: "commit first (uncommitted changes stay out; no commits against the base branch is an error): pushes the commits of this clone to the branch of this Chat and opens a pull request, or, when one is open, only pushes and prints its URL (title and body stay); " + protectedNote, when: hasForgeAPI, parse: parsePullCreate},
	{name: "push", note: "commit first (uncommitted changes stay out; no commits against the base branch is an error): pushes the commits of this clone to the branch of this Chat on origin, without a pull request: a human merges it; " + protectedNote,
		when: hasLocalTickets, parse: parsePush},
	{name: "tickets publish", args: "-m <message>",
		note: "commit first: puts the changes this branch made under the tickets directory, and nothing else, as one commit with the message on top of the base branch as it is on origin now and pushes it there; the whole branch is validated as by push; a conflict, a ticket given the status only a human gives, or a base branch that moved meanwhile is an error and nothing is pushed (call again); prints the commit",
		when: hasLocalTickets, parse: parseTicketsPublish},
	{name: "stack logs", args: "<service> [--tail <n>]",
		note: fmt.Sprintf("the last n lines (default %d, at most %d) of a service of the Stack, afk (this Sandbox) too", defaultLogTail, maxLogTail), parse: parseStackLogs},
	{name: "stack restart", args: "<service>",
		note: "any service of the Stack but afk (this Sandbox)", parse: parseStackRestart},
	{name: "stack reseed", note: "runs afk-setup again and prints its output: resets what that script resets, volumes stay; the only way to run afk-setup, which stack restart does not take",
		when: hasReseed, parse: parseStackReseed},
	{name: "vm list", note: "the test VMs of [[vm]] with state, os, user, owner and address; every vm command but list and screenshot claims the VM for this Chat until vm release or the end of afk chat (another Chat gets 409); ssh: ssh -o HostName=<address> <vm> <command> (key and config in ~/.ssh; the human puts the public key, which afk config prints, on the VM)",
		when: hasVMs, parse: parseVMList},
	{name: "vm start", args: "<vm>", when: hasVMs, parse: vmAction("start")},
	{name: "vm shutdown", args: "<vm>", note: "ACPI", when: hasVMs, parse: vmAction("shutdown")},
	{name: "vm release", args: "<vm>", note: "ends the claim of this Chat", when: hasVMs, parse: vmAction("release")},
	{name: "vm screenshot", args: "<vm> [--file <path>]",
		note: "saves a PNG (by default in the temporary directory) and prints its path and size", when: hasVMs, parse: parseVMScreenshot},
	{name: "vm key", args: "<vm> <key>[+<key>]...", note: fmt.Sprintf("a combination such as ctrl+alt+delete, at most %d keys", maxKeyCombo), when: hasVMs, parse: parseVMKey},
	{name: "vm type", args: "<vm> (<text> | -)", note: fmt.Sprintf("US-ASCII, at most %d characters; - reads stdin", maxTypeChars), when: hasVMs, parse: parseVMType},
	{name: "vm click", args: "<vm> [--button left|right|middle] <x> <y>", note: "pixels of the last screenshot of the VM, which must come first", when: hasVMs, parse: parseVMPoint("click")},
	{name: "vm move", args: "<vm> <x> <y>", note: "pixels of the last screenshot of the VM, which must come first", when: hasVMs, parse: parseVMPoint("move")},
	{name: "vm snapshot", args: "<vm> <name>", note: "name: " + strings.Trim(snapshotPattern.String(), "^$"), when: hasVMs, parse: parseVMSnapshot("snapshot")},
	{name: "vm revert", args: "<vm> <name>", when: hasVMs, parse: parseVMSnapshot("revert")},
}

// capabilities are what a Chat has beyond its Stack's logs and restarts:
// fixed for the life of `afk chat`.
type capabilities struct {
	VMs    bool // [[vm]] in the Project config
	Reseed bool // the service afk-setup in the Stack
	// TicketsDir is forge.dir of a local forge: tickets are files there, with
	// no issue or pull request API. Empty: the Forge has that API.
	TicketsDir string
}

// cliCommand is one command of afk-ai.
type cliCommand struct {
	name, args, note string
	// when tells whether a Chat with caps has what the command needs; nil: every Chat.
	when func(caps capabilities) (ok bool)
	// parse turns the arguments after the name into the call.
	parse func(args []string, stdin io.Reader) (call cliCall, err error)
}

// cliCall is a request to the channel: what a subcommand of afk-ai asks for.
type cliCall struct {
	method, path string
	body         any
	// saveTo is the file the reply's Data is written to (a screenshot), when set.
	saveTo string
}

// --- the help of afk-ai ---

// hasVMs, hasReseed, hasForgeAPI and hasLocalTickets are the conditions of
// the vm commands, of stack reseed, of the issue and pr commands and of the
// commands of a local forge.
func hasVMs(caps capabilities) (ok bool)          { return caps.VMs }
func hasReseed(caps capabilities) (ok bool)       { return caps.Reseed }
func hasForgeAPI(caps capabilities) (ok bool)     { return caps.TicketsDir == "" }
func hasLocalTickets(caps capabilities) (ok bool) { return caps.TicketsDir != "" }

// cliHelp is the help of afk-ai for a Chat with caps: its commands, each
// with its note. The prompt of the Chat and `GET /` of its channel carry it.
func cliHelp(caps capabilities) (help string) {
	var b strings.Builder
	b.WriteString("usage:\n")
	for _, c := range cliCommands {
		if c.when != nil && !c.when(caps) {
			continue
		}
		line := strings.TrimSpace(CLIBinary + " " + c.name + " " + c.args)
		fmt.Fprintf(&b, "  %s\n", line)
		if c.note != "" {
			fmt.Fprintf(&b, "      %s\n", c.note)
		}
	}
	b.WriteString(cliFlagsNote)
	help = b.String()
	return
}

// --- host: the help route of the channel ---

// helpRoutes is the help part of the channel: GET / answers the help of a
// Chat with caps.
func helpRoutes(caps capabilities) (group routeGroup) {
	help := cliHelp(caps)
	return func(mux *http.ServeMux, logger *log.Logger) {
		s := responder{log: logger}
		mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, _ *http.Request) {
			s.reply(w, "help", help, "", 0, nil)
		})
	}
}

// --- Sandbox: afk-ai ---

// RunCLI executes `afk-ai args` against the channel at socket and writes
// the result to stdout and the channel's warning to stderr; stdin is the text
// of --body-file -. No arguments print the help of this Chat, which only the
// channel knows; wrong arguments are an error with it.
func RunCLI(ctx context.Context, socket string, args []string, stdin io.Reader, stdout, stderr io.Writer) (err error) {
	if len(args) == 0 {
		err = send(ctx, socket, helpCall, stdout, stderr)
		return
	}
	err = errCLIUsage
	var call cliCall
	for _, c := range cliCommands {
		// A name is one word (push) or two (issue view).
		if words := strings.Fields(c.name); len(args) >= len(words) && slices.Equal(args[:len(words)], words) {
			call, err = c.parse(args[len(words):], stdin)
			break
		}
	}
	if errors.Is(err, errCLIUsage) {
		err = usage(ctx, socket)
		return
	}
	if err != nil {
		return
	}
	err = send(ctx, socket, call, stdout, stderr)
	return
}

// usage is errCLIUsage with the help of this Chat, or with why there is none.
func usage(ctx context.Context, socket string) (err error) {
	var help strings.Builder
	if err = send(ctx, socket, helpCall, &help, io.Discard); err != nil {
		err = fmt.Errorf("%w: %w", errCLIUsage, err)
		return
	}
	err = fmt.Errorf("%w:\n%s", errCLIUsage, strings.TrimSuffix(help.String(), "\n"))
	return
}

// send sends call over the channel at socket and writes the reply: the
// output to stdout, the warning to stderr.
func send(ctx context.Context, socket string, call cliCall, stdout, stderr io.Writer) (err error) {
	// pr create bundles, validates and pushes the clone and asks the Forge
	// twice: minutes on a big repository; a ticket call answers in seconds.
	client := &http.Client{Timeout: 5 * time.Minute, Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (conn net.Conn, err error) {
			conn, err = (&net.Dialer{}).DialContext(ctx, "unix", socket)
			return
		},
	}}
	var payload io.Reader
	if call.body != nil {
		var data []byte
		if data, err = json.Marshal(call.body); err != nil {
			err = fmt.Errorf("encoding the request: %w", err)
			return
		}
		payload = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, call.method, "http://afk"+call.path, payload)
	if err != nil {
		err = fmt.Errorf("building the request: %w", err)
		return
	}
	resp, err := client.Do(req)
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ECONNREFUSED) {
		err = errors.New("no channel to afk (works only in a Sandbox of `afk chat`)")
		return
	}
	if err != nil {
		err = fmt.Errorf("calling afk: %w", err)
		return
	}
	defer func() { _ = resp.Body.Close() }()
	var reply channelReply
	if err = json.NewDecoder(resp.Body).Decode(&reply); err != nil {
		err = fmt.Errorf("reading the reply of afk: %w", err)
		return
	}
	if reply.Error != "" {
		err = errors.New(reply.Error)
		return
	}
	if reply.Warning != "" {
		_, _ = fmt.Fprintln(stderr, reply.Warning)
	}
	if call.saveTo != "" {
		err = saveScreenshot(call.saveTo, reply, stdout)
		return
	}
	_, err = io.WriteString(stdout, reply.Output)
	return
}

// textFlags is the flag set of a command that takes a text: --body, or
// --body-file - for stdin. text returns it once the set is parsed.
func textFlags(name string, body *string, stdin io.Reader) (flags *flag.FlagSet, text func() (text string, err error)) {
	flags = flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(body, "body", "", "")
	file := flags.String("body-file", "", "")
	text = func() (text string, err error) {
		switch {
		case *file == "":
			text = *body
		case *file != "-" || *body != "":
			err = errCLIUsage
		default:
			var data []byte
			data, err = io.ReadAll(stdin)
			text = string(data)
		}
		return
	}
	return
}

// labelsFlag defines flag name, which may repeat and list labels with commas.
func labelsFlag(flags *flag.FlagSet, name string, labels *[]string) {
	flags.Func(name, "", func(value string) (err error) {
		*labels = append(*labels, strings.Split(value, ",")...)
		return
	})
}
