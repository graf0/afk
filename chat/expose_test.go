package chat

import (
	"net"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/graf0/afk/config"
)

func TestExposeSpecs(t *testing.T) {
	if _, err := exposeSpecs(nil); err == nil || !strings.Contains(err.Error(), "[expose]") {
		t.Errorf("empty [expose]: err = %v", err)
	}
	specs, err := exposeSpecs(map[string]config.Ports{"web": {"8080:80"}, "api": {"8081:80", "2222:22"}})
	if err != nil || !slices.Equal(specs["web"], []string{"8080:80"}) || !slices.Equal(specs["api"], []string{"8081:80", "2222:22"}) {
		t.Errorf("specs = %v, %v", specs, err)
	}
}

func TestClaimPorts(t *testing.T) {
	free, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := strconv.Itoa(free.Addr().(*net.TCPAddr).Port)
	freeToo, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	second := strconv.Itoa(freeToo.Addr().(*net.TCPAddr).Port)
	_ = free.Close()
	_ = freeToo.Close()
	// Something else listens on the second spec of the service: every spec counts.
	expose := map[string]config.Ports{"web": {second + ":81", port + ":80"}}

	dir := t.TempDir()
	alpha, beta := newClaims(dir, claimPort, "p/alpha"), newClaims(dir, claimPort, "p/beta")
	if err := claimPorts(alpha, expose); err != nil {
		t.Fatalf("free ports: %v", err)
	}

	// Another Chat holds them: the error names it.
	err = claimPorts(beta, expose)
	if err == nil || !strings.Contains(err.Error(), "port "+second+" is held by Chat p/alpha") {
		t.Errorf("held by a Chat: err = %v", err)
	}

	// Something else listens: the error names the port.
	alpha.close()
	other, err := net.Listen("tcp", "127.0.0.1:"+port)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = other.Close() }()
	err = claimPorts(beta, expose)
	if err == nil || strings.Contains(err.Error(), "held by Chat") || !strings.Contains(err.Error(), port) {
		t.Errorf("held by something else: err = %v", err)
	}
}
