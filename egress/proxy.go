package egress

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
)

const dialTimeout = 10 * time.Second

// ErrDenied is returned (wrapped) when policy or the address check refuses a target.
var ErrDenied = errors.New("egress denied")

// Addresses that are never reachable through the proxy, beyond what the
// net/netip predicates cover.
var deniedPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),      // "this network"
	netip.MustParsePrefix("100.64.0.0/10"),  // carrier-grade NAT
	netip.MustParsePrefix("240.0.0.0/4"),    // reserved, includes broadcast
	netip.MustParsePrefix("64:ff9b::/96"),   // NAT64, can embed private IPv4
	netip.MustParsePrefix("64:ff9b:1::/48"), // local-use NAT64
	netip.MustParsePrefix("::/96"),          // deprecated IPv4-compatible IPv6
	netip.MustParsePrefix("2002::/16"),      // 6to4, embeds IPv4
	netip.MustParsePrefix("2001::/32"),      // Teredo, embeds IPv4
	netip.MustParsePrefix("fec0::/10"),      // deprecated site-local
	netip.MustParsePrefix("192.0.0.0/24"),   // IETF protocol assignments
	netip.MustParsePrefix("198.18.0.0/15"),  // benchmarking
}

// errBadTarget marks a target that is not a valid "host:port".
var errBadTarget = errors.New("bad target")

// --- proxy ---

// Resolver resolves a host name to IP addresses.
type Resolver interface {
	LookupIPAddr(ctx context.Context, host string) (addrs []net.IPAddr, err error)
}

// Dialer opens connections to resolved addresses.
type Dialer interface {
	DialContext(ctx context.Context, network, address string) (conn net.Conn, err error)
}

// Proxy is an HTTP proxy (CONNECT and plain HTTP) enforcing a Policy.
// Resolver, Dialer and HostAddrs are seams; New sets production defaults.
type Proxy struct {
	Policy    Policy
	Resolver  Resolver
	Dialer    Dialer
	HostAddrs func() (addrs []net.Addr, err error) // addresses of the host's interfaces
	// Denials gets one line per denied target, "host:port: error"; nil
	// discards them. Connections write concurrently, so it must be safe for that.
	Denials io.Writer
}

// New returns a Proxy enforcing policy, using the system resolver and dialer.
func New(policy Policy) (proxy *Proxy) {
	return &Proxy{
		Policy:    policy,
		Resolver:  net.DefaultResolver,
		Dialer:    &net.Dialer{Timeout: dialTimeout},
		HostAddrs: net.InterfaceAddrs,
	}
}

// --- serving ---

// Serve accepts connections from ln until ctx is cancelled or ln is closed.
// Each connection carries one request. Serve closes ln and waits for the
// connection handlers before returning.
func (p *Proxy) Serve(ctx context.Context, ln net.Listener) (err error) {
	defer context.AfterFunc(ctx, func() { _ = ln.Close() })()

	var wg sync.WaitGroup
	defer wg.Wait()
	for {
		conn, acceptErr := ln.Accept()
		if acceptErr != nil {
			if ctx.Err() != nil || errors.Is(acceptErr, net.ErrClosed) {
				return nil
			}
			err = fmt.Errorf("accepting connection: %w", acceptErr)
			return
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			p.handle(ctx, conn)
		}()
	}
}

// handle serves one request. It deliberately avoids http.Server: Hijack there
// calls SetReadDeadline, which ssh channels do not support (docs/egress-proxy.md),
// so no deadlines are used here either; ctx cancellation closes the connection.
func (p *Proxy) handle(ctx context.Context, conn net.Conn) {
	defer func() { _ = conn.Close() }()
	defer context.AfterFunc(ctx, func() { _ = conn.Close() })()

	br := bufio.NewReader(conn)
	req, err := http.ReadRequest(br)
	if err != nil {
		writeStatus(conn, http.StatusBadRequest)
		return
	}
	req = req.WithContext(ctx)
	if req.Method == http.MethodConnect {
		p.handleConnect(ctx, conn, br, req)
		return
	}
	p.handlePlain(conn, req)
}

// --- CONNECT ---

func (p *Proxy) handleConnect(ctx context.Context, conn net.Conn, br *bufio.Reader, req *http.Request) {
	upstream, err := p.dial(ctx, "tcp", req.Host)
	if err != nil {
		writeStatus(conn, statusFor(err))
		return
	}
	defer func() { _ = upstream.Close() }()
	if _, err = io.WriteString(conn, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		return
	}

	// br first drains bytes the client sent before our 200.
	done := make(chan struct{}, 2)
	go func() { pipe(upstream, br); done <- struct{}{} }()
	go func() { pipe(conn, upstream); done <- struct{}{} }()
	<-done
	<-done
}

// pipe copies src to dst, then half-closes dst (or closes it when it cannot
// half-close). A copy error closes both directions through dst's Close.
func pipe(dst net.Conn, src io.Reader) {
	_, err := io.Copy(dst, src)
	if closer, ok := dst.(interface{ CloseWrite() error }); ok && err == nil {
		_ = closer.CloseWrite()
		return
	}
	_ = dst.Close()
}

// --- plain HTTP ---

func (p *Proxy) handlePlain(conn net.Conn, req *http.Request) {
	if req.URL.Scheme != "http" || req.URL.Host == "" {
		writeStatus(conn, http.StatusBadRequest)
		return
	}
	req.RequestURI = ""
	req.Close = true
	req.Header.Del("Proxy-Connection")
	req.Header.Del("Proxy-Authorization")
	req.URL.User = nil // the checked URL host is the only target; no credentials from the URL
	req.Host = req.URL.Host

	// No keep-alives: the transport keeps no state, so one per request is cheap.
	transport := &http.Transport{DialContext: p.dial, DisableKeepAlives: true}
	resp, err := transport.RoundTrip(req)
	if err != nil {
		writeStatus(conn, statusFor(err))
		return
	}
	defer func() { _ = resp.Body.Close() }()
	resp.Close = true
	_ = resp.Write(conn)
}

// --- responses ---

// statusFor maps a dial or round trip error to the response status.
func statusFor(err error) (status int) {
	switch {
	case errors.Is(err, ErrDenied):
		return http.StatusForbidden
	case errors.Is(err, errBadTarget):
		return http.StatusBadRequest
	}
	return http.StatusBadGateway
}

func writeStatus(w io.Writer, status int) {
	_, _ = fmt.Fprintf(w, "HTTP/1.1 %d %s\r\nContent-Length: 0\r\nConnection: close\r\n\r\n", status, http.StatusText(status))
}

// --- dialing and address checks ---

// dial applies the policy and address checks to address ("host:port") and
// connects to a resolved IP, never to the name.
func (p *Proxy) dial(ctx context.Context, network, address string) (conn net.Conn, err error) {
	host, port, err := splitHostPort(address)
	if err != nil {
		err = fmt.Errorf("parsing target %q: %w", address, err)
		return
	}
	defer func() { p.logDenial(host, port, err) }()
	if !p.Policy.AllowsPort(port) {
		err = fmt.Errorf("%w: port %d not allowed", ErrDenied, port)
		return
	}
	if !p.Policy.Allows(host) {
		err = fmt.Errorf("%w: host %q not allowed", ErrDenied, host)
		return
	}
	ips, err := p.resolve(ctx, host)
	if err != nil {
		return
	}
	if err = p.checkAddrs(host, ips); err != nil {
		return
	}

	var lastErr error
	for _, ip := range ips {
		conn, lastErr = p.Dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), strconv.Itoa(port)))
		if lastErr == nil {
			return
		}
	}
	err = fmt.Errorf("dialing %q: %w", host, lastErr)
	return
}

// logDenial writes a line to p.Denials when err is a denial. The host comes
// from the guest: control characters become "?", so that it cannot forge
// lines or drive the terminal of whoever reads the file.
func (p *Proxy) logDenial(host string, port int, err error) {
	if p.Denials == nil || !errors.Is(err, ErrDenied) {
		return
	}
	line := fmt.Sprintf("%s: %v", net.JoinHostPort(host, strconv.Itoa(port)), err)
	line = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return '?'
		}
		return r
	}, line)
	_, _ = fmt.Fprintln(p.Denials, line)
}

// resolve returns the addresses of host, IPv4 first. IP literals skip DNS.
func (p *Proxy) resolve(ctx context.Context, host string) (ips []net.IP, err error) {
	host = normalizeHost(host)
	if addr, parseErr := netip.ParseAddr(host); parseErr == nil {
		ips = []net.IP{addr.AsSlice()}
		return
	}
	addrs, err := p.Resolver.LookupIPAddr(ctx, host)
	if err != nil {
		err = fmt.Errorf("resolving %q: %w", host, err)
		return
	}
	var v4s, v6s []net.IP
	for _, addr := range addrs {
		switch {
		case addr.Zone != "":
			err = fmt.Errorf("%w: %q resolved to a zoned address", ErrDenied, host)
			return
		case addr.IP.To4() != nil:
			v4s = append(v4s, addr.IP)
		default:
			v6s = append(v6s, addr.IP)
		}
	}
	ips = append(v4s, v6s...)
	if len(ips) == 0 {
		err = fmt.Errorf("resolving %q: no addresses", host)
	}
	return
}

// checkAddrs denies the target when any address is non-public or belongs to the host.
func (p *Proxy) checkAddrs(host string, ips []net.IP) (err error) {
	hostAddrs, err := p.HostAddrs()
	if err != nil {
		// Fail closed: without host addresses the check cannot be done.
		err = fmt.Errorf("%w: listing host addresses: %w", ErrDenied, err)
		return
	}
	for _, ip := range ips {
		if isDeniedIP(ip) || isHostAddr(ip, hostAddrs) {
			err = fmt.Errorf("%w: %q resolves to %s", ErrDenied, host, ip)
			return
		}
	}
	return
}

func isDeniedIP(ip net.IP) (denied bool) {
	addr, ok := netip.AddrFromSlice(ip)
	if !ok {
		return true
	}
	addr = addr.Unmap()
	if addr.IsLoopback() || addr.IsPrivate() || addr.IsLinkLocalUnicast() ||
		addr.IsLinkLocalMulticast() || addr.IsInterfaceLocalMulticast() ||
		addr.IsUnspecified() || addr.IsMulticast() {
		return true
	}
	return slices.ContainsFunc(deniedPrefixes, func(prefix netip.Prefix) bool { return prefix.Contains(addr) })
}

func isHostAddr(ip net.IP, hostAddrs []net.Addr) (found bool) {
	for _, hostAddr := range hostAddrs {
		var hostIP net.IP
		switch a := hostAddr.(type) {
		case *net.IPNet:
			hostIP = a.IP
		case *net.IPAddr:
			hostIP = a.IP
		}
		if hostIP.Equal(ip) {
			return true
		}
	}
	return false
}

// splitHostPort splits a mandatory-port "host:port" and validates the port.
func splitHostPort(address string) (host string, port int, err error) {
	host, portStr, err := net.SplitHostPort(address)
	if err != nil {
		err = fmt.Errorf("%w: %w", errBadTarget, err)
		return
	}
	port, convErr := strconv.Atoi(portStr)
	if convErr != nil || port < 1 || port > 65535 {
		err = fmt.Errorf("%w: invalid port %q", errBadTarget, portStr)
		return
	}
	if host == "" {
		err = fmt.Errorf("%w: empty host", errBadTarget)
	}
	return
}
