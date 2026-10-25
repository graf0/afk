package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestRunNeedsBaseBranch: vm list and image build read the base branch first,
// vm kill does not.
func TestRunNeedsBaseBranch(t *testing.T) {
	root := t.TempDir()
	if out, err := exec.Command("git", "init", "-q", root).CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, args := range [][]string{{"vm", "list"}, {"image", "build"}} {
		err := runCommand(t.Context(), nil, args, root, &bytes.Buffer{})
		if err == nil || !strings.Contains(err.Error(), "base branch") {
			t.Errorf("%v: err = %v, want a base branch error", args, err)
		}
	}
	err := runCommand(t.Context(), nil, []string{"vm", "kill", "x"}, root, &bytes.Buffer{})
	if err != nil && strings.Contains(err.Error(), "base branch") {
		t.Errorf("vm kill: err = %v, want no base branch read", err)
	}
}

func TestRun(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	t.Run("factory ignores the working tree's .afk.toml", func(t *testing.T) {
		err := runCommand(t.Context(), nil, []string{"factory", "#x"}, root, &bytes.Buffer{})
		if err == nil || !strings.Contains(err.Error(), "afk factory") {
			t.Errorf("err = %v, want usage (no config load)", err)
		}
	})

	t.Run("config without .afk.toml fails with path", func(t *testing.T) {
		var out bytes.Buffer
		err := runCommand(t.Context(), nil, []string{"config"}, root, &out)
		if err == nil || !strings.Contains(err.Error(), filepath.Join(root, ".afk.toml")) {
			t.Errorf("err = %v", err)
		}
	})

	if err := os.WriteFile(filepath.Join(root, ".afk.toml"), []byte("[forge]\nkind = \"github\"\nrepo = \"o/r\"\n[gate]\ncmd = \"true\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Run("config prints effective config", func(t *testing.T) {
		var out bytes.Buffer
		if err := runCommand(t.Context(), nil, []string{"config"}, root, &out); err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"max_fixes = 3", "ready-for-agent", ".claude/"} {
			if !strings.Contains(out.String(), want) {
				t.Errorf("output lacks %q:\n%s", want, out.String())
			}
		}
	})

	t.Run("config lists every error on its own line", func(t *testing.T) {
		bad := t.TempDir()
		_ = os.Mkdir(filepath.Join(bad, ".git"), 0o755)
		toml := "[forge]\nkind = \"github\"\nrepo = \"x\"\n[labels]\nwontfix = \"\"\n"
		if err := os.WriteFile(filepath.Join(bad, ".afk.toml"), []byte(toml), 0o644); err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		err := runCommand(t.Context(), nil, []string{"config"}, bad, &out)
		if err == nil {
			t.Fatal("want error")
		}
		lines := strings.Split(err.Error(), "\n")
		if len(lines) != 3 || !strings.Contains(lines[0], ".afk.toml: 3:1: forge.repo") || !strings.Contains(lines[1], ".afk.toml: 5:1: labels.wontfix") || !strings.Contains(lines[2], "gate.cmd: required") {
			t.Errorf("err = %q, want 3 positioned lines", err)
		}
		if out.Len() != 0 {
			t.Errorf("printed %q on failure", out.String())
		}
	})

	t.Run("unknown command", func(t *testing.T) {
		err := runCommand(t.Context(), nil, []string{"nope"}, root, &bytes.Buffer{})
		if err == nil || !strings.Contains(err.Error(), "nope") {
			t.Errorf("err = %v", err)
		}
	})

	t.Run("image needs a subcommand", func(t *testing.T) {
		err := runCommand(t.Context(), nil, []string{"image"}, root, &bytes.Buffer{})
		if err == nil || !strings.Contains(err.Error(), "afk image build") {
			t.Errorf("err = %v", err)
		}
	})

	t.Run("no command requires project", func(t *testing.T) {
		if err := runCommand(t.Context(), nil, nil, root, &bytes.Buffer{}); err != nil {
			t.Errorf("err = %v", err)
		}
		other := t.TempDir()
		_ = os.Mkdir(filepath.Join(other, ".git"), 0o755)
		if err := runCommand(t.Context(), nil, nil, other, &bytes.Buffer{}); err == nil {
			t.Error("want error without .afk.toml")
		}
	})
}

func TestHandleSignals(t *testing.T) {
	ctx, abort := handleSignals(t.Context())
	self := os.Getpid()

	if err := syscall.Kill(self, syscall.SIGINT); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("first signal did not cancel the context")
	}
	select {
	case <-abort:
		t.Fatal("abort closed by the first signal")
	default:
	}

	if err := syscall.Kill(self, syscall.SIGINT); err != nil {
		t.Fatal(err)
	}
	select {
	case <-abort:
	case <-time.After(5 * time.Second):
		t.Fatal("second signal did not close abort")
	}
}
