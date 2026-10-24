package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/term"

	"github.com/graf0/afk/agent"
	"github.com/graf0/afk/config"
	"github.com/graf0/afk/egress"
	"github.com/graf0/afk/image"
	"github.com/graf0/afk/machine"
)

// sshTimeout bounds waiting for a fresh machine's sshd.
const sshTimeout = 2 * time.Minute

const vmUsage = "usage: afk vm up | afk vm shell <vm> | afk vm kill <vm>"

// runVM executes the diagnostic `afk vm <subcommand>`.
func runVM(ctx context.Context, args []string, cfg config.Config, stdout io.Writer) (err error) {
	argCount := map[string]int{"up": 1, "shell": 2, "kill": 2} // including the subcommand
	if len(args) == 0 || argCount[args[0]] != len(args) {
		err = errors.New(vmUsage)
		return
	}
	conn, err := machine.Connect()
	if err != nil {
		return
	}
	defer func() { _ = conn.Close() }()
	switch args[0] {
	case "up":
		err = vmUp(ctx, conn, cfg, stdout)
	case "shell":
		err = vmShell(ctx, conn, cfg, args[1])
	case "kill":
		err = conn.Kill(args[1])
	}
	return
}

// vmUp starts a Run machine of the Project and waits for ssh; the machine
// keeps running.
func vmUp(ctx context.Context, conn *machine.Conn, cfg config.Config, stdout io.Writer) (err error) {
	project, err := image.ProjectName(cfg.Forge.Repo)
	if err != nil {
		return
	}
	paths, err := image.DefaultPaths()
	if err != nil {
		return
	}
	disk, err := image.LatestDisk(paths, project)
	if err != nil {
		return
	}
	signer, err := image.SSHSigner(paths)
	if err != nil {
		return
	}
	id := make([]byte, 4)
	_, _ = rand.Read(id)

	start := time.Now()
	m, err := conn.Up(ctx, machine.Spec{Project: project, Kind: machine.KindRun, ID: hex.EncodeToString(id), BaseDisk: disk})
	if err != nil {
		return
	}
	sshCtx, cancel := context.WithTimeout(ctx, sshTimeout)
	defer cancel()
	client, err := m.SSH(sshCtx, signer)
	if err != nil {
		if killErr := conn.Kill(m.Name); killErr != nil {
			err = errors.Join(err, killErr)
		}
		return
	}
	_ = client.Close()
	_, err = fmt.Fprintf(stdout, "%s: start → ssh %s, guest %s\n", m.Name, time.Since(start).Round(100*time.Millisecond), m.Addr)
	return
}

// vmShell opens an interactive shell in machine name, with the Egress proxy
// (allowlist [network].allow + agent.Domains) tunneled in for its duration.
func vmShell(ctx context.Context, conn *machine.Conn, cfg config.Config, name string) (err error) {
	policy, err := egress.Allowlist(slices.Concat(cfg.Network.Allow, agent.Domains))
	if err != nil {
		return
	}
	paths, err := image.DefaultPaths()
	if err != nil {
		return
	}
	signer, err := image.SSHSigner(paths)
	if err != nil {
		return
	}
	m, err := conn.Get(name)
	if err != nil {
		return
	}
	sshCtx, cancel := context.WithTimeout(ctx, sshTimeout)
	defer cancel()
	client, err := m.SSH(sshCtx, signer)
	if err != nil {
		return
	}
	defer func() { _ = client.Close() }()
	stop, err := machine.ServeEgress(ctx, client, policy)
	if err != nil {
		return
	}
	defer stop()

	session, err := client.NewSession()
	if err != nil {
		err = fmt.Errorf("opening ssh session: %w", err)
		return
	}
	defer func() { _ = session.Close() }()
	session.Stdin, session.Stdout, session.Stderr = os.Stdin, os.Stdout, os.Stderr
	if fd := int(os.Stdin.Fd()); term.IsTerminal(fd) {
		var state *term.State
		if state, err = term.MakeRaw(fd); err != nil {
			err = fmt.Errorf("setting terminal raw mode: %w", err)
			return
		}
		defer func() { _ = term.Restore(fd, state) }()
		cols, rows, sizeErr := term.GetSize(int(os.Stdout.Fd()))
		if sizeErr != nil || rows == 0 { // unknown size
			cols, rows = 80, 24
		}
		termName := os.Getenv("TERM")
		if termName == "" {
			termName = "xterm"
		}
		if err = session.RequestPty(termName, rows, cols, ssh.TerminalModes{ssh.ECHO: 1}); err != nil {
			err = fmt.Errorf("requesting pty: %w", err)
			return
		}
	}
	proxy := "http://" + machine.ProxyAddr
	if err = session.Start("export http_proxy=" + proxy + " https_proxy=" + proxy + "; exec bash -l"); err != nil {
		err = fmt.Errorf("starting shell: %w", err)
		return
	}
	if err = session.Wait(); err != nil {
		err = fmt.Errorf("shell: %w", err)
	}
	return
}
