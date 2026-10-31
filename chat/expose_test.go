package chat

import (
	"net"
	"strconv"
	"strings"
	"testing"
)

func TestExposeSpecs(t *testing.T) {
	if _, err := exposeSpecs(nil); err == nil || !strings.Contains(err.Error(), "[expose]") {
		t.Errorf("empty [expose]: err = %v", err)
	}
	specs, err := exposeSpecs(map[string]string{"web": "8080:80"})
	if err != nil || len(specs["web"]) != 1 || specs["web"][0] != "8080:80" {
		t.Errorf("specs = %v, %v", specs, err)
	}
}

func TestClaimPorts(t *testing.T) {
	free, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := strconv.Itoa(free.Addr().(*net.TCPAddr).Port)
	_ = free.Close()
	expose := map[string]string{"web": port + ":80"}

	dir := t.TempDir()
	alpha, beta := newClaims(dir, claimPort, "p/alpha"), newClaims(dir, claimPort, "p/beta")
	if err := claimPorts(alpha, expose); err != nil {
		t.Fatalf("free ports: %v", err)
	}

	// Another Chat holds them: the error names it.
	err = claimPorts(beta, expose)
	if err == nil || !strings.Contains(err.Error(), "port "+port+" is held by Chat p/alpha") {
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
