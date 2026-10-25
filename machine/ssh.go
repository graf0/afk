package machine

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/graf0/afk/egress"
)

const (
	sshUser = "agent" // the user created in every Base image

	// ProxyAddr is where the Egress proxy appears inside the guest.
	ProxyAddr = "127.0.0.1:3128"

	// SSHTimeout bounds waiting for a fresh machine's sshd.
	SSHTimeout = 2 * time.Minute

	dialRetry        = 500 * time.Millisecond
	handshakeTimeout = 10 * time.Second

	hostKeyPath = "/etc/ssh/ssh_host_ed25519_key"
)

// ErrHostKey is returned (wrapped) when the guest presents a host key other
// than the one generated for the machine; SSH does not retry then.
var ErrHostKey = errors.New("ssh host key mismatch")

// --- host key ---

// newHostKey generates the machine's sshd host key. Base image clones share
// their host keys, so each machine gets its own, installed before sshd starts
// by systemd-tmpfiles from the tmpfiles.extra credential (SMBIOS, see the
// domain XML). credential is that tmpfiles config, base64-encoded; `f+~`
// overwrites the existing key with base64-decoded content.
func newHostKey() (public ssh.PublicKey, credential string, err error) {
	publicRaw, privateRaw, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		err = fmt.Errorf("generating host key: %w", err)
		return
	}
	block, err := ssh.MarshalPrivateKey(privateRaw, "")
	if err != nil {
		err = fmt.Errorf("encoding host key: %w", err)
		return
	}
	if public, err = ssh.NewPublicKey(publicRaw); err != nil {
		err = fmt.Errorf("encoding host public key: %w", err)
		return
	}
	b64 := base64.StdEncoding.EncodeToString
	tmpfiles := fmt.Sprintf("f+~ %s 0600 root root - %s\nf+~ %s.pub 0644 root root - %s\n",
		hostKeyPath, b64(pem.EncodeToMemory(block)),
		hostKeyPath, b64(ssh.MarshalAuthorizedKey(public)))
	credential = b64([]byte(tmpfiles))
	return
}

// --- ssh ---

// SSH connects to m as the Base image user with signer (the `afk` key),
// retrying until the guest's sshd answers or ctx is done. The host key must be
// m.HostKey (ed25519 only); a different one fails at once with ErrHostKey.
func (m Machine) SSH(ctx context.Context, signer ssh.Signer) (client *ssh.Client, err error) {
	config, err := m.sshConfig(signer)
	if err != nil {
		return
	}
	addr := net.JoinHostPort(m.Addr, "22")
	for {
		var dialErr error
		client, dialErr = dialSSH(ctx, addr, config)
		if dialErr == nil {
			return
		}
		if errors.Is(dialErr, ErrHostKey) {
			err = fmt.Errorf("ssh to %s: %w", m.Name, dialErr)
			return
		}
		select {
		case <-ctx.Done():
			err = fmt.Errorf("ssh to %s: %w (last error: %w)", m.Name, ctx.Err(), dialErr)
			return
		case <-time.After(dialRetry):
		}
	}
}

// sshConfig authenticates with signer and accepts only m.HostKey.
func (m Machine) sshConfig(signer ssh.Signer) (config *ssh.ClientConfig, err error) {
	if m.HostKey == nil {
		err = fmt.Errorf("machine %s has no host key", m.Name)
		return
	}
	fixed := ssh.FixedHostKey(m.HostKey)
	config = &ssh.ClientConfig{
		User: sshUser,
		Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: func(hostname string, remote net.Addr, key ssh.PublicKey) (err error) {
			if err = fixed(hostname, remote, key); err != nil {
				err = fmt.Errorf("%w: %w", ErrHostKey, err)
			}
			return
		},
		HostKeyAlgorithms: []string{ssh.KeyAlgoED25519},
	}
	return
}

// dialSSH makes one connection attempt.
func dialSSH(ctx context.Context, addr string, config *ssh.ClientConfig) (client *ssh.Client, err error) {
	dialer := net.Dialer{Timeout: handshakeTimeout}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	_ = conn.SetDeadline(time.Now().Add(handshakeTimeout))
	clientConn, chans, reqs, err := ssh.NewClientConn(conn, addr, config)
	if !stop() && err == nil { // ctx done: conn is being closed
		_ = clientConn.Close()
		err = ctx.Err()
	}
	if err != nil {
		_ = conn.Close()
		return
	}
	_ = conn.SetDeadline(time.Time{})
	client = ssh.NewClient(clientConn, chans, reqs)
	return
}

// --- egress ---

// ServeEgress serves an Egress proxy with policy on ProxyAddr inside the guest,
// through a reverse tunnel on client; no port is opened on the host. stop
// shuts the proxy down and waits for it; so do cancelling ctx and closing client.
// denials gets a line per denied target (nil discards them).
func ServeEgress(ctx context.Context, client *ssh.Client, policy egress.Policy, denials io.Writer) (stop func(), err error) {
	ln, err := client.Listen("tcp", ProxyAddr)
	if err != nil {
		err = fmt.Errorf("opening proxy tunnel on guest %s: %w", ProxyAddr, err)
		return
	}
	proxyCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		proxy := egress.New(policy)
		proxy.Denials = denials
		_ = proxy.Serve(proxyCtx, ln)
	}()
	stop = func() {
		cancel()
		<-done
	}
	return
}
