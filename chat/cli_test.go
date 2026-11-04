package chat

import (
	"bytes"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"
)

// Without the channel there is no help either: only the Chat knows its commands.
func TestRunCLINoChannel(t *testing.T) {
	for _, args := range [][]string{{"issue", "list"}, nil} {
		err := RunCLI(t.Context(), filepath.Join(t.TempDir(), "none.sock"), args, nil, &bytes.Buffer{}, io.Discard)
		if err == nil || !strings.Contains(err.Error(), "no channel to afk") {
			t.Errorf("%q: err = %v", args, err)
		}
	}
}

// No arguments print the help of this Chat from the channel, wrong ones are
// an error with it: only what the Chat has.
func TestRunCLIHelp(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "afk.sock")
	stop, err := serveChannel(t.Context(), socket, io.Discard, helpRoutes(capabilities{Reseed: true}))
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	check := func(args []string, help string) {
		t.Helper()
		for _, want := range []string{"afk-ai issue view <n>", "afk-ai stack reseed", "--body-file - reads"} {
			if !strings.Contains(help, want) {
				t.Errorf("%q: help lacks %q:\n%s", args, want, help)
			}
		}
		if strings.Contains(help, "afk-ai vm") {
			t.Errorf("%q: help of a Chat without VMs has the vm commands:\n%s", args, help)
		}
	}

	var out bytes.Buffer
	if err := RunCLI(t.Context(), socket, nil, nil, &out, io.Discard); err != nil {
		t.Fatalf("no arguments: err = %v", err)
	}
	check(nil, out.String())

	for _, args := range [][]string{{"issue"}, {"nope", "x"}, {"issue", "view"}} {
		err := RunCLI(t.Context(), socket, args, nil, io.Discard, io.Discard)
		if !errors.Is(err, errCLIUsage) {
			t.Fatalf("%q: err = %v, want the usage", args, err)
		}
		check(args, err.Error())
	}
}

// A Chat on a local forge has push and tickets publish instead of the issue
// and pr commands.
func TestCLIHelpLocal(t *testing.T) {
	help := cliHelp(capabilities{TicketsDir: ".scratch"})
	for _, want := range []string{"afk-ai push\n", "afk-ai tickets publish -m <message>", "afk-ai stack logs"} {
		if !strings.Contains(help, want) {
			t.Errorf("help lacks %q:\n%s", want, help)
		}
	}
	for _, gone := range []string{"afk-ai issue", "afk-ai pr"} {
		if strings.Contains(help, gone) {
			t.Errorf("help of a local forge has %q:\n%s", gone, help)
		}
	}
	if help = cliHelp(capabilities{}); strings.Contains(help, "afk-ai push") || strings.Contains(help, "tickets publish") {
		t.Errorf("help of a Forge with an API has the commands of a local one:\n%s", help)
	}
}
