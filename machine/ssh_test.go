package machine

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func newSigner(t *testing.T) (signer ssh.Signer) {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if signer, err = ssh.NewSignerFromKey(private); err != nil {
		t.Fatal(err)
	}
	return
}

func TestNewHostKeyCredential(t *testing.T) {
	public, credential, err := newHostKey()
	if err != nil {
		t.Fatal(err)
	}
	tmpfiles, err := base64.StdEncoding.DecodeString(credential)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(tmpfiles)), "\n")
	if len(lines) != 2 {
		t.Fatalf("tmpfiles config:\n%s", tmpfiles)
	}

	// f+~: create or truncate, argument base64-encoded (tmpfiles.d(5)).
	fields := strings.Fields(lines[0])
	if len(fields) != 7 || strings.Join(fields[:6], " ") != "f+~ /etc/ssh/ssh_host_ed25519_key 0600 root root -" {
		t.Fatalf("private key line = %q", lines[0])
	}
	pemData, err := base64.StdEncoding.DecodeString(fields[6])
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.ParsePrivateKey(pemData)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(signer.PublicKey().Marshal(), public.Marshal()) || public.Type() != ssh.KeyAlgoED25519 {
		t.Error("private key in the credential does not match the returned public key")
	}

	fields = strings.Fields(lines[1])
	if len(fields) != 7 || strings.Join(fields[:6], " ") != "f+~ /etc/ssh/ssh_host_ed25519_key.pub 0644 root root -" {
		t.Fatalf("public key line = %q", lines[1])
	}
	if pubData, _ := base64.StdEncoding.DecodeString(fields[6]); !bytes.Equal(pubData, ssh.MarshalAuthorizedKey(public)) {
		t.Errorf("public key file = %q", pubData)
	}

	if other, _, _ := newHostKey(); bytes.Equal(other.Marshal(), public.Marshal()) {
		t.Error("two machines got the same host key")
	}
}

// serveSSH accepts ssh connections on loopback with hostKey, authorizing
// clientKey, and returns the address.
func serveSSH(t *testing.T, hostKey ssh.Signer, clientKey ssh.PublicKey) (addr string) {
	t.Helper()
	config := &ssh.ServerConfig{
		PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (perms *ssh.Permissions, err error) {
			if !bytes.Equal(key.Marshal(), clientKey.Marshal()) {
				err = errors.New("unknown key")
			}
			return
		},
	}
	config.AddHostKey(hostKey)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				_, chans, reqs, err := ssh.NewServerConn(conn, config)
				if err != nil {
					return
				}
				go ssh.DiscardRequests(reqs)
				for ch := range chans {
					_ = ch.Reject(ssh.Prohibited, "test")
				}
			}()
		}
	}()
	return ln.Addr().String()
}

func TestSSHPinsHostKey(t *testing.T) {
	client, guest, impostor := newSigner(t), newSigner(t), newSigner(t)
	addr := serveSSH(t, guest, client.PublicKey())

	m := Machine{Name: "afk-run-x", HostKey: guest.PublicKey()}
	config, err := m.sshConfig(client)
	if err != nil {
		t.Fatal(err)
	}
	if config.User != "agent" || len(config.HostKeyAlgorithms) != 1 || config.HostKeyAlgorithms[0] != ssh.KeyAlgoED25519 {
		t.Errorf("config: user %q, host key algorithms %v", config.User, config.HostKeyAlgorithms)
	}
	conn, err := dialSSH(t.Context(), addr, config)
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()

	m.HostKey = impostor.PublicKey()
	if config, err = m.sshConfig(client); err != nil {
		t.Fatal(err)
	}
	if conn, err = dialSSH(t.Context(), addr, config); !errors.Is(err, ErrHostKey) {
		if conn != nil {
			_ = conn.Close()
		}
		t.Errorf("err = %v, want ErrHostKey", err)
	}

	if _, err = (Machine{Name: "afk-run-x"}).sshConfig(client); err == nil {
		t.Error("machine without host key accepted")
	}
}

func TestSSHGivesUpWhenContextEnds(t *testing.T) {
	// TEST-NET-1 never answers: SSH keeps retrying until the deadline.
	m := Machine{Name: "afk-run-x", Addr: "192.0.2.1", HostKey: newSigner(t).PublicKey()}
	ctx, cancel := context.WithTimeout(t.Context(), 1200*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := m.SSH(ctx, newSigner(t))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want deadline exceeded", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("SSH returned %s after the deadline", elapsed)
	}
}
