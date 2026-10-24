package main

import (
	"bytes"
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
