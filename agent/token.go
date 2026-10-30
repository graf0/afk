package agent

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const (
	// TokenEnv carries the Claude Code OAuth token, on the host and in the Sandbox.
	TokenEnv = "CLAUDE_CODE_OAUTH_TOKEN"

	// tokenFile holds the token in the afk state directory, mode 0600.
	tokenFile = "claude-oauth-token"
)

// OAuthToken returns the Claude Code token: TokenEnv, else the file
// stateDir/claude-oauth-token, which must not be readable by others.
func OAuthToken(stateDir string) (token string, err error) {
	if token = strings.TrimSpace(os.Getenv(TokenEnv)); token != "" {
		err = checkToken(token)
		return
	}
	path := filepath.Join(stateDir, tokenFile)
	hint := fmt.Sprintf("run `claude setup-token` and set %s or save the token to %s (mode 0600)", TokenEnv, path)
	info, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		err = fmt.Errorf("no Claude token: %s", hint)
		return
	}
	if err != nil {
		err = fmt.Errorf("reading Claude token: %w", err)
		return
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		err = fmt.Errorf("refusing Claude token file %s with mode %v: want a regular file with mode 0600", path, info.Mode())
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		err = fmt.Errorf("reading Claude token: %w", err)
		return
	}
	if token = strings.TrimSpace(string(data)); token == "" {
		err = fmt.Errorf("token file %s is empty: %s", path, hint)
		return
	}
	err = checkToken(token)
	return
}

// checkToken rejects a token that would break its passing to the Sandbox (one
// line on stdin).
func checkToken(token string) (err error) {
	if strings.ContainsAny(token, "\r\n") {
		err = errors.New("the Claude token contains a line break")
	}
	return
}
