package egress

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

const (
	publicV4 = "93.184.216.34"
	publicV6 = "2606:2800:220:1::1"
)

type fakeResolver struct {
	mu    sync.Mutex
	table map[string][]string
	calls []string
}

func (r *fakeResolver) LookupIPAddr(ctx context.Context, host string) (addrs []net.IPAddr, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, host)
	ips, ok := r.table[host]
	if !ok {
		err = fmt.Errorf("no such host %s", host)
		return
	}
	for _, ip := range ips {
		addrs = append(addrs, net.IPAddr{IP: net.ParseIP(ip)})
	}
	return
}

func (r *fakeResolver) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

// fakeDialer records requested addresses and connects to target instead.
type fakeDialer struct {
	mu     sync.Mutex
	target string
	addrs  []string
}

func (d *fakeDialer) DialContext(ctx context.Context, network, addr string) (conn net.Conn, err error) {
	d.mu.Lock()
	d.addrs = append(d.addrs, addr)
	d.mu.Unlock()
	if d.target == "" {
		err = errors.New("connection refused")
		return
	}
	return (&net.Dialer{}).DialContext(ctx, network, d.target)
}

func (d *fakeDialer) dialed() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.addrs...)
}

type fixture struct {
	proxy    *Proxy
	resolver *fakeResolver
	dialer   *fakeDialer
	addr     string
}

func newFixture(t *testing.T, policy Policy, table map[string][]string, target string) *fixture {
	t.Helper()
	f := &fixture{
		resolver: &fakeResolver{table: table},
		dialer:   &fakeDialer{target: target},
	}
	f.proxy = New(policy)
	f.proxy.Resolver = f.resolver
	f.proxy.Dialer = f.dialer
	f.proxy.HostAddrs = func() ([]net.Addr, error) {
		return []net.Addr{&net.IPNet{IP: net.ParseIP("203.0.113.7"), Mask: net.CIDRMask(24, 32)}}, nil
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- f.proxy.Serve(ctx, ln) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Serve: %v", err)
		}
	})
	f.addr = ln.Addr().String()
	return f
}

func mustAllowlist(t *testing.T, entries ...string) Policy {
	t.Helper()
	policy, err := Allowlist(entries)
	if err != nil {
		t.Fatal(err)
	}
	return policy
}

func dialProxy(t *testing.T, addr string) net.Conn {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	return conn
}

// send writes raw to the proxy and returns the response status code.
func send(t *testing.T, addr, raw string) (status int) {
	t.Helper()
	conn := dialProxy(t, addr)
	if _, err := io.WriteString(conn, raw); err != nil {
		t.Fatal(err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode
}

func connectRequest(target string) string {
	return fmt.Sprintf("CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", target, target)
}

func getRequest(target string) string {
	return fmt.Sprintf("GET http://%s/path?q=1 HTTP/1.1\r\nHost: %s\r\nProxy-Connection: keep-alive\r\n\r\n", target, target)
}

func startEcho(t *testing.T) (addr string) {
	t.Helper()
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
				defer func() { _ = conn.Close() }()
				_, _ = io.Copy(conn, conn)
			}()
		}
	}()
	return ln.Addr().String()
}

func TestDeniedNameNeverReachesResolver(t *testing.T) {
	f := newFixture(t, mustAllowlist(t, "allowed.example"), map[string][]string{"evil.example": {publicV4}}, "")
	for name, raw := range map[string]string{
		"connect": connectRequest("evil.example:443"),
		"http":    getRequest("evil.example"),
	} {
		if got := send(t, f.addr, raw); got != http.StatusForbidden {
			t.Errorf("%s: status = %d, want 403", name, got)
		}
	}
	if n := f.resolver.callCount(); n != 0 {
		t.Errorf("resolver called %d times for a denied name", n)
	}
	if got := f.dialer.dialed(); len(got) != 0 {
		t.Errorf("dialed %v", got)
	}
}

func TestDeniedResolvedAddresses(t *testing.T) {
	tests := map[string][]string{
		"mixed public and private": {publicV4, "10.0.0.5"},
		"private first":            {"192.168.1.1", publicV4},
		"loopback":                 {"127.0.0.1"},
		"mapped loopback":          {"::ffff:127.0.0.1"},
		"ipv6 loopback":            {"::1"},
		"link-local":               {"169.254.169.254"},
		"ipv6 link-local":          {"fe80::1"},
		"unspecified":              {"0.0.0.0"},
		"ipv6 unspecified":         {"::"},
		"multicast":                {"224.0.0.1"},
		"link-local multicast":     {"ff02::1"},
		"ipv6 ula":                 {"fd00::1"},
		"cgnat":                    {"100.64.0.1"},
		"this network":             {"0.1.2.3"},
		"host interface address":   {"203.0.113.7"},
		"host interface mapped":    {"::ffff:203.0.113.7"},
		"nat64":                    {"64:ff9b::7f00:1"},
		"ipv4 compatible":          {"::7f00:1"},
		"6to4":                     {"2002:7f00:1::1"},
		"teredo":                   {"2001:0:7f00:1::1"},
		"site-local":               {"fec0::1"},
		"protocol assignments":     {"192.0.0.8"},
		"benchmarking":             {"198.18.0.1"},
	}
	for name, ips := range tests {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, mustAllowlist(t, "app.example"), map[string][]string{"app.example": ips}, "")
			for kind, raw := range map[string]string{
				"connect": connectRequest("app.example:443"),
				"http":    getRequest("app.example"),
			} {
				if got := send(t, f.addr, raw); got != http.StatusForbidden {
					t.Errorf("%s: status = %d, want 403", kind, got)
				}
			}
			if got := f.dialer.dialed(); len(got) != 0 {
				t.Errorf("dialed %v", got)
			}
		})
	}
}

func TestIPLiteralTargets(t *testing.T) {
	t.Run("allowlist without the literal", func(t *testing.T) {
		f := newFixture(t, mustAllowlist(t, "app.example"), nil, "")
		if got := send(t, f.addr, connectRequest(publicV4+":443")); got != http.StatusForbidden {
			t.Errorf("status = %d, want 403", got)
		}
		if f.resolver.callCount() != 0 || len(f.dialer.dialed()) != 0 {
			t.Error("resolver or dialer used")
		}
	})
	t.Run("allow all denies loopback without DNS", func(t *testing.T) {
		f := newFixture(t, AllowAll(), nil, "")
		for _, target := range []string{"127.0.0.1:80", "[::1]:80", "10.1.2.3:80", "203.0.113.7:80", "[fe80::1%25eth0]:80"} {
			if got := send(t, f.addr, connectRequest(target)); got != http.StatusForbidden {
				t.Errorf("%s: status = %d, want 403", target, got)
			}
		}
		if f.resolver.callCount() != 0 || len(f.dialer.dialed()) != 0 {
			t.Error("resolver or dialer used")
		}
	})
	t.Run("listed private literal is still denied", func(t *testing.T) {
		f := newFixture(t, mustAllowlist(t, "10.0.0.1"), nil, "")
		if got := send(t, f.addr, connectRequest("10.0.0.1:80")); got != http.StatusForbidden {
			t.Errorf("status = %d, want 403", got)
		}
	})
	t.Run("listed public literal is dialed without DNS", func(t *testing.T) {
		echo := startEcho(t)
		f := newFixture(t, mustAllowlist(t, publicV4), nil, echo)
		if got := send(t, f.addr, connectRequest(publicV4+":443")); got != http.StatusOK {
			t.Errorf("status = %d, want 200", got)
		}
		if f.resolver.callCount() != 0 {
			t.Error("resolver called for IP literal")
		}
		if got := f.dialer.dialed(); len(got) != 1 || got[0] != publicV4+":443" {
			t.Errorf("dialed %v", got)
		}
	})
}

func TestConnectTunnelsBothWaysIncludingBufferedBytes(t *testing.T) {
	echo := startEcho(t)
	f := newFixture(t, mustAllowlist(t, "app.example"), map[string][]string{"app.example": {publicV4}}, echo)

	conn := dialProxy(t, f.addr)
	// Payload sent in the same write as the request lands in the proxy's buffer.
	if _, err := io.WriteString(conn, connectRequest("app.example:443")+"early"); err != nil {
		t.Fatal(err)
	}
	br := bufio.NewReader(conn)
	line, err := br.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if line != "HTTP/1.1 200 Connection Established\r\n" {
		t.Fatalf("status line = %q", line)
	}
	if blank, _ := br.ReadString('\n'); blank != "\r\n" {
		t.Fatalf("expected blank line, got %q", blank)
	}

	buf := make([]byte, len("early"))
	if _, err := io.ReadFull(br, buf); err != nil || string(buf) != "early" {
		t.Fatalf("buffered bytes echo = %q, %v", buf, err)
	}
	if _, err := io.WriteString(conn, "later"); err != nil {
		t.Fatal(err)
	}
	buf = make([]byte, len("later"))
	if _, err := io.ReadFull(br, buf); err != nil || string(buf) != "later" {
		t.Fatalf("echo = %q, %v", buf, err)
	}
	if got := f.dialer.dialed(); len(got) != 1 || got[0] != publicV4+":443" {
		t.Errorf("dialed %v, want resolved address", got)
	}
}

func TestPlainHTTPReturnsUpstreamResponse(t *testing.T) {
	var gotReq *http.Request
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotReq = r
		w.Header().Set("X-Upstream", "yes")
		w.WriteHeader(http.StatusTeapot)
		_, _ = io.WriteString(w, "body from upstream")
	}))
	t.Cleanup(upstream.Close)

	f := newFixture(t, mustAllowlist(t, "app.example"), map[string][]string{"app.example": {publicV4}}, upstream.Listener.Addr().String())
	conn := dialProxy(t, f.addr)
	if _, err := io.WriteString(conn, getRequest("app.example:8080")); err != nil {
		t.Fatal(err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusTeapot || string(body) != "body from upstream" || resp.Header.Get("X-Upstream") != "yes" {
		t.Errorf("response = %d %q %v", resp.StatusCode, body, resp.Header)
	}
	if gotReq.URL.RequestURI() != "/path?q=1" || gotReq.Host != "app.example:8080" {
		t.Errorf("upstream saw %q host %q", gotReq.URL.RequestURI(), gotReq.Host)
	}
	if gotReq.Header.Get("Proxy-Connection") != "" {
		t.Error("Proxy-Connection forwarded upstream")
	}
	if got := f.dialer.dialed(); len(got) != 1 || got[0] != publicV4+":8080" {
		t.Errorf("dialed %v, want resolved address", got)
	}
}

func TestPlainHTTPIgnoresUserinfoAndHostHeader(t *testing.T) {
	var gotReq *http.Request
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotReq = r
	}))
	t.Cleanup(upstream.Close)

	f := newFixture(t, mustAllowlist(t, "app.example"), map[string][]string{"app.example": {publicV4}, "evil.example": {publicV4}}, upstream.Listener.Addr().String())
	raw := "GET http://user:pass@app.example/ HTTP/1.1\r\nHost: evil.example\r\n\r\n"
	if got := send(t, f.addr, raw); got != http.StatusOK {
		t.Fatalf("status = %d", got)
	}
	if gotReq.Host != "app.example" {
		t.Errorf("upstream Host = %q, want app.example", gotReq.Host)
	}
	if gotReq.Header.Get("Authorization") != "" {
		t.Error("URL userinfo forwarded as Authorization")
	}
	if calls := f.resolver.callCount(); calls != 1 {
		t.Errorf("resolver calls = %d, want 1 (app.example only)", calls)
	}
}

func TestResolverEdgeCases(t *testing.T) {
	f := newFixture(t, AllowAll(), map[string][]string{"empty.example": {}}, "")
	if got := send(t, f.addr, connectRequest("empty.example:443")); got != http.StatusBadGateway {
		t.Errorf("no addresses: status = %d, want 502", got)
	}

	zoned := &zonedResolver{}
	f.proxy.Resolver = zoned
	if got := send(t, f.addr, connectRequest("zoned.example:443")); got != http.StatusForbidden {
		t.Errorf("zoned address: status = %d, want 403", got)
	}
	if got := f.dialer.dialed(); len(got) != 0 {
		t.Errorf("dialed %v", got)
	}
}

type zonedResolver struct{}

func (zonedResolver) LookupIPAddr(ctx context.Context, host string) (addrs []net.IPAddr, err error) {
	return []net.IPAddr{{IP: net.ParseIP(publicV6), Zone: "eth0"}}, nil
}

func TestIPv4DialedFirst(t *testing.T) {
	echo := startEcho(t)
	f := newFixture(t, mustAllowlist(t, "app.example"), map[string][]string{"app.example": {publicV6, publicV4}}, echo)
	if got := send(t, f.addr, connectRequest("app.example:443")); got != http.StatusOK {
		t.Fatalf("status = %d", got)
	}
	if got := f.dialer.dialed(); len(got) == 0 || got[0] != publicV4+":443" {
		t.Errorf("dialed %v, want IPv4 first", got)
	}
}

func TestAllowAllResolvesAnyPublicName(t *testing.T) {
	echo := startEcho(t)
	f := newFixture(t, AllowAll(), map[string][]string{"anything.example": {publicV4}}, echo)
	if got := send(t, f.addr, connectRequest("anything.example:443")); got != http.StatusOK {
		t.Errorf("status = %d, want 200", got)
	}
}

func TestUpstreamFailureIs502(t *testing.T) {
	f := newFixture(t, mustAllowlist(t, "app.example", "nodns.example"), map[string][]string{"app.example": {publicV4}}, "")
	for name, raw := range map[string]string{
		"connect dial failure": connectRequest("app.example:443"),
		"http dial failure":    getRequest("app.example"),
		"connect dns failure":  connectRequest("nodns.example:443"),
	} {
		if got := send(t, f.addr, raw); got != http.StatusBadGateway {
			t.Errorf("%s: status = %d, want 502", name, got)
		}
	}
}

func TestHostAddrsFailureDenies(t *testing.T) {
	f := newFixture(t, AllowAll(), map[string][]string{"app.example": {publicV4}}, "")
	f.proxy.HostAddrs = func() ([]net.Addr, error) { return nil, errors.New("boom") }
	if got := send(t, f.addr, connectRequest("app.example:443")); got != http.StatusForbidden {
		t.Errorf("status = %d, want 403 when host addresses are unknown", got)
	}
	if got := f.dialer.dialed(); len(got) != 0 {
		t.Errorf("dialed %v", got)
	}
}

func TestMalformedRequests(t *testing.T) {
	f := newFixture(t, AllowAll(), map[string][]string{"app.example": {publicV4}}, "")
	tests := map[string]string{
		"garbage":                "not http\r\n\r\n",
		"origin-form":            "GET /path HTTP/1.1\r\nHost: app.example\r\n\r\n",
		"https absolute-form":    "GET https://app.example/ HTTP/1.1\r\nHost: app.example\r\n\r\n",
		"connect without port":   connectRequest("app.example"),
		"connect invalid port":   connectRequest("app.example:http"),
		"connect port zero":      connectRequest("app.example:0"),
		"connect port too large": connectRequest("app.example:70000"),
	}
	for name, raw := range tests {
		if got := send(t, f.addr, raw); got != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", name, got)
		}
	}
	if f.resolver.callCount() != 0 {
		t.Error("resolver called for malformed request")
	}
}

func TestServeStopsOnContextCancel(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- New(AllowAll()).Serve(ctx, ln) }()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Serve = %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not stop")
	}
}

func TestServeStopsWhenListenerClosed(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- New(AllowAll()).Serve(context.Background(), ln) }()
	_ = ln.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Serve = %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not stop")
	}
}

func TestServeClosesActiveConnectionsOnCancel(t *testing.T) {
	echo := startEcho(t)
	proxy := New(AllowAll())
	proxy.Resolver = &fakeResolver{table: map[string][]string{"app.example": {publicV4}}}
	proxy.Dialer = &fakeDialer{target: echo}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- proxy.Serve(ctx, ln) }()

	conn := dialProxy(t, ln.Addr().String())
	_, _ = io.WriteString(conn, connectRequest("app.example:443"))
	br := bufio.NewReader(conn)
	_, _ = br.ReadString('\n')
	_, _ = br.ReadString('\n')

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Serve = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not stop with an open tunnel")
	}
}

func TestIsDeniedIP(t *testing.T) {
	denied := []string{"127.0.0.1", "127.9.9.9", "::1", "10.0.0.1", "172.16.0.1", "192.168.0.1", "169.254.0.1", "fe80::1", "0.0.0.0", "::", "224.0.0.1", "ff02::1", "fc00::1", "100.64.0.1", "100.127.255.255", "255.255.255.255", "240.0.0.1", "::ffff:10.0.0.1"}
	allowed := []string{publicV4, publicV6, "1.1.1.1", "100.128.0.1", "172.32.0.1", "8.8.8.8"}
	for _, s := range denied {
		if !isDeniedIP(net.ParseIP(s)) {
			t.Errorf("%s not denied", s)
		}
	}
	for _, s := range allowed {
		if isDeniedIP(net.ParseIP(s)) {
			t.Errorf("%s denied", s)
		}
	}
}
