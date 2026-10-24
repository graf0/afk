package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/graf0/afk/config"
)

func TestRunVMUsage(t *testing.T) {
	for _, args := range [][]string{nil, {"up", "x"}, {"shell"}, {"kill"}, {"kill", "a", "b"}, {"start"}} {
		err := runVM(t.Context(), args, config.Config{}, &bytes.Buffer{})
		if err == nil || !strings.Contains(err.Error(), "afk vm up") {
			t.Errorf("runVM(%q): err = %v, want usage", args, err)
		}
	}
}

func TestTerminalHelpersOnNonTerminal(t *testing.T) {
	file, err := os.Create(filepath.Join(t.TempDir(), "f"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	if _, err = makeRaw(int(file.Fd())); err == nil {
		t.Error("makeRaw on a regular file succeeded")
	}
	if rows, cols := terminalSize(int(file.Fd())); rows != 24 || cols != 80 {
		t.Errorf("terminalSize = %dx%d, want the 24x80 fallback", rows, cols)
	}
}
