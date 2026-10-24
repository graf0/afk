package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOAuthToken(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, tokenFile)

	t.Setenv(TokenEnv, " from-env\n")
	if token, err := OAuthToken(dir); err != nil || token != "from-env" {
		t.Errorf("from env: %q, %v", token, err)
	}

	t.Setenv(TokenEnv, "")
	if _, err := OAuthToken(dir); err == nil || !strings.Contains(err.Error(), "claude setup-token") {
		t.Errorf("missing: err = %v, want setup-token hint", err)
	}

	if err := os.WriteFile(path, []byte("from-file\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := OAuthToken(dir); err == nil || !strings.Contains(err.Error(), "0600") {
		t.Errorf("mode 0644: err = %v, want refusal", err)
	}

	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if token, err := OAuthToken(dir); err != nil || token != "from-file" {
		t.Errorf("from file: %q, %v", token, err)
	}

	if err := os.WriteFile(path, []byte("a\nb\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OAuthToken(dir); err == nil {
		t.Error("multi-line token: want error")
	}
}
