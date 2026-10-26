package machine

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"golang.org/x/crypto/ssh"
	"golang.org/x/term"
)

// Interactive runs command in the guest over client on a pty of the local
// terminal, in raw mode (Ctrl-C goes to the command, not to this process),
// following changes of the terminal's size. secret, when not empty, is sent
// as the first line of the command's stdin, before the terminal's input; the
// pty then starts with echo off, so the line never reaches the screen, and the
// command must read it and turn echo back on (`stty echo`). Without a local
// terminal there is no pty: stdin is passed as it is. Cancelling ctx closes
// the session.
func Interactive(ctx context.Context, client *ssh.Client, command, secret string) (err error) {
	session, err := client.NewSession()
	if err != nil {
		err = fmt.Errorf("opening ssh session: %w", err)
		return
	}
	defer func() { _ = session.Close() }()
	defer context.AfterFunc(ctx, func() { _ = session.Close() })()

	// ponytail: ssh copies os.Stdin in a goroutine that outlives Wait and eats
	// one more keystroke; fine for one session per process (CLI, a tmux window).
	session.Stdin, session.Stdout, session.Stderr = os.Stdin, os.Stdout, os.Stderr
	if secret != "" {
		session.Stdin = io.MultiReader(strings.NewReader(secret+"\n"), os.Stdin)
	}
	if fd := int(os.Stdin.Fd()); term.IsTerminal(fd) {
		var state *term.State
		if state, err = term.MakeRaw(fd); err != nil {
			err = fmt.Errorf("setting terminal raw mode: %w", err)
			return
		}
		defer func() { _ = term.Restore(fd, state) }()
		cols, rows := terminalSize()
		echo := uint32(1)
		if secret != "" {
			echo = 0
		}
		termName := os.Getenv("TERM")
		if termName == "" {
			termName = "xterm"
		}
		if err = session.RequestPty(termName, rows, cols, ssh.TerminalModes{ssh.ECHO: echo}); err != nil {
			err = fmt.Errorf("requesting pty: %w", err)
			return
		}
		winch := make(chan os.Signal, 1)
		signal.Notify(winch, syscall.SIGWINCH)
		defer func() { signal.Stop(winch); close(winch) }()
		go func() {
			for range winch {
				cols, rows := terminalSize()
				_ = session.WindowChange(rows, cols)
			}
		}()
	}
	if err = session.Start(command); err != nil {
		err = fmt.Errorf("starting command: %w", err)
		return
	}
	err = session.Wait()
	return
}

// terminalSize is the size of the local terminal, 80x24 when unknown.
func terminalSize() (cols, rows int) {
	cols, rows, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil || rows == 0 {
		return 80, 24
	}
	return
}
