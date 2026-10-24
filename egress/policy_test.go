package egress

import "testing"

func TestAllowlistMatching(t *testing.T) {
	policy, err := Allowlist([]string{"example.com", "*.Wild.org", "1.2.3.4", "2001:db8::1"})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		host string
		want bool
	}{
		{"example.com", true},
		{"EXAMPLE.com", true},
		{"example.com.", true},
		{"a.example.com", false},
		{"badexample.com", false},
		{"a.wild.org", true},
		{"A.B.WILD.org", true},
		{"wild.org", false},
		{"badwild.org", false},
		{".wild.org", false},
		{"a..wild.org", false},
		{"", false},
		{"1.2.3.4", true},
		{"1.2.3.5", false},
		{"2001:DB8::1", true},
		{"2001:db8::2", false},
		{"fe80::1%eth0", false},
	}
	for _, tt := range tests {
		if got := policy.Allows(tt.host); got != tt.want {
			t.Errorf("Allows(%q) = %v, want %v", tt.host, got, tt.want)
		}
	}
}

// A wildcard entry must never match an IP literal that merely ends with the suffix.
func TestAllowlistWildcardDoesNotMatchIPLiteral(t *testing.T) {
	policy, err := Allowlist([]string{"*.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	if policy.Allows("127.0.0.1") {
		t.Error("wildcard matched IP literal 127.0.0.1")
	}
}

func TestAllowlistRejectsInvalidEntries(t *testing.T) {
	for _, entry := range []string{"", "*", "*.", "a.*.com", "*example.com", "exa*mple.com", ".example.com", "a..com", "ex ample.com"} {
		if _, err := Allowlist([]string{entry}); err == nil {
			t.Errorf("Allowlist(%q) succeeded, want error", entry)
		}
	}
}

func TestZeroPolicyDeniesEverything(t *testing.T) {
	var policy Policy
	if policy.Allows("example.com") || policy.Allows("1.2.3.4") {
		t.Error("zero Policy allowed a host")
	}
}

func TestAllowAll(t *testing.T) {
	policy := AllowAll()
	for _, host := range []string{"example.com", "1.2.3.4"} {
		if !policy.Allows(host) {
			t.Errorf("AllowAll denied %q", host)
		}
	}
	if policy.Allows("") || policy.Allows("a..b") {
		t.Error("AllowAll allowed a malformed host")
	}
}
