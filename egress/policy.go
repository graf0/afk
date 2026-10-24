// Package egress implements the Egress proxy: the only way out of a machine.
package egress

import (
	"fmt"
	"net/netip"
	"slices"
	"strings"
)

// Policy decides which host names the proxy may connect to.
// The zero value denies everything.
type Policy struct {
	all      bool
	exact    map[string]struct{}
	suffixes []string // ".example.com" for the entry "*.example.com"
}

// AllowAll returns a Policy allowing every host name (address checks still apply).
func AllowAll() (policy Policy) {
	policy.all = true
	return
}

// Allowlist returns a Policy allowing only the given entries.
// An entry is an exact host name (or IP literal); "*.example.com" matches
// subdomains of any depth, but not "example.com" itself. Matching ignores case.
func Allowlist(entries []string) (policy Policy, err error) {
	policy.exact = make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		wildcard := strings.HasPrefix(entry, "*.")
		host := normalizeHost(strings.TrimPrefix(entry, "*."))
		if !validHost(host) {
			err = fmt.Errorf("invalid allowlist entry %q", entry)
			return
		}
		if !wildcard {
			policy.exact[host] = struct{}{}
			continue
		}
		if isIPLiteral(host) {
			err = fmt.Errorf("invalid allowlist entry %q: wildcard on IP address", entry)
			return
		}
		policy.suffixes = append(policy.suffixes, "."+host)
	}
	return
}

// Allows reports whether host (without port) is permitted by the policy.
func (p Policy) Allows(host string) (ok bool) {
	host = normalizeHost(host)
	if !validHost(host) {
		return false
	}
	if p.all {
		return true
	}
	if _, found := p.exact[host]; found {
		return true
	}
	// IP literals match only exactly, never by suffix ("*.0.0.1" vs 127.0.0.1).
	if isIPLiteral(host) {
		return false
	}
	for _, suffix := range p.suffixes {
		if strings.HasSuffix(host, suffix) {
			return true
		}
	}
	return false
}

// AllowsPort reports whether the policy permits connecting to port.
// An allowlist permits only the web ports 80 and 443; AllowAll permits any.
func (p Policy) AllowsPort(port int) (ok bool) {
	return p.all || port == 80 || port == 443
}

// normalizeHost lowercases host, drops one trailing dot and canonicalizes IP literals.
func normalizeHost(host string) (normalized string) {
	normalized = strings.TrimSuffix(strings.ToLower(host), ".")
	if addr, err := netip.ParseAddr(normalized); err == nil && addr.Zone() == "" {
		normalized = addr.String()
	}
	return
}

// validHost reports whether a normalized host is a plausible name or IP literal.
func validHost(host string) (ok bool) {
	if host == "" || strings.ContainsAny(host, " \t\r\n*/@%") {
		return false
	}
	if isIPLiteral(host) {
		return true
	}
	if strings.Contains(host, ":") {
		return false
	}
	return !slices.Contains(strings.Split(host, "."), "")
}

func isIPLiteral(host string) (ok bool) {
	_, err := netip.ParseAddr(host)
	return err == nil
}
