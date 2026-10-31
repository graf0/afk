package chat

import (
	"bytes"
	"io"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunCLINoChannel(t *testing.T) {
	err := RunCLI(t.Context(), filepath.Join(t.TempDir(), "none.sock"), []string{"issue", "list"}, nil, &bytes.Buffer{}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "no channel to afk") {
		t.Errorf("err = %v", err)
	}
}
