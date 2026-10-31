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
	"net"
	"net/http"
	"strings"
	"syscall"
	"time"
)

const (
	// CLIBinary is the name `afk` runs as in the Sandbox, and in $PATH there:
	// the host's binary, mounted read-only (ADR-0012).
	CLIBinary = "afk-ai"

	cliUsage = `usage:
  afk-ai issue view <n>
  afk-ai issue list [--label <label>] [--state open|closed|all]
  afk-ai issue create --title <title> [--body <text> | --body-file -] [--label <a,b>]...
  afk-ai issue comment <n> (--body <text> | --body-file -)
  afk-ai issue edit <n> [--add-label <a,b>]... [--remove-label <a,b>]... [--title <title>] [--body <text> | --body-file -]
  afk-ai pr create --title <title> [--body <text> | --body-file -]
  afk-ai stack logs <service> [--tail <n>]
  afk-ai stack restart <service>
  afk-ai stack reseed
  afk-ai vm list
  afk-ai vm (start|shutdown|release) <vm>
  afk-ai vm screenshot <vm> [--file <path>]
  afk-ai vm key <vm> <key>[+<key>]...
  afk-ai vm type <vm> (<text> | -)
  afk-ai vm click <vm> [--button left|right|middle] <x> <y>
  afk-ai vm move <vm> <x> <y>
  afk-ai vm (snapshot|revert) <vm> <name>
--body-file - reads the text from stdin; --label, --add-label and --remove-label may repeat or list labels with commas;
--title and --body only on issues created by this Chat that are not ready-for-agent;
pr create pushes the commits of this clone (uncommitted changes stay out) to the branch of this Chat and opens a pull request, or, when one is open, only pushes and prints its URL;
stack logs prints the last n lines (default 100, at most 1000) of a service of the Stack; stack restart works on any service but afk and afk-setup; stack reseed runs afk-setup again and prints its output;
vm works only on the test VMs of [[vm]]: list shows state, os, owner and address; start, shutdown (ACPI), key, type, click, move, snapshot and revert claim the VM for this Chat until release or the end of afk chat (another Chat gets 409); screenshot saves a PNG (by default in the temporary directory) and prints its path and size, click and move take pixels of the last screenshot; key is a combination such as ctrl+alt+delete; type takes US-ASCII, at most 500 characters, "-" reads stdin`
)

// errCLIUsage answers arguments afk-ai does not take.
var errCLIUsage = errors.New(cliUsage)

// cliCommands parse the arguments after `<group> <subcommand>` into the call.
var cliCommands = map[string]func(args []string, stdin io.Reader) (call cliCall, err error){
	"issue view":    parseIssueView,
	"issue list":    parseIssueList,
	"issue create":  parseIssueCreate,
	"issue comment": parseIssueComment,
	"issue edit":    parseIssueEdit,
	"pr create":     parsePullCreate,
	"stack logs":    parseStackLogs,
	"stack restart": parseStackRestart,
	"stack reseed":  parseStackReseed,
	"vm list":       parseVMList,
	"vm start":      vmAction("start"),
	"vm shutdown":   vmAction("shutdown"),
	"vm release":    vmAction("release"),
	"vm screenshot": parseVMScreenshot,
	"vm key":        parseVMKey,
	"vm type":       parseVMType,
	"vm click":      parseVMPoint("click"),
	"vm move":       parseVMPoint("move"),
	"vm snapshot":   parseVMSnapshot("snapshot"),
	"vm revert":     parseVMSnapshot("revert"),
}

// cliCall is a request to the channel: what a subcommand of afk-ai asks for.
type cliCall struct {
	method, path string
	body         any
	// saveTo is the file the reply's Data is written to (a screenshot), when set.
	saveTo string
}

// RunCLI executes `afk-ai args` against the channel at socket and writes
// the result to stdout and the channel's warning to stderr; stdin is the text
// of --body-file -.
func RunCLI(ctx context.Context, socket string, args []string, stdin io.Reader, stdout, stderr io.Writer) (err error) {
	var parse func(args []string, stdin io.Reader) (call cliCall, err error)
	if len(args) >= 2 {
		parse = cliCommands[args[0]+" "+args[1]]
	}
	if parse == nil {
		err = errCLIUsage
		return
	}
	call, err := parse(args[2:], stdin)
	if err != nil {
		return
	}
	err = send(ctx, socket, call, stdout, stderr)
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
