package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/graf0/afk/stack"
)

func TestParseSelector(t *testing.T) {
	for _, args := range [][]string{nil, {"*"}} {
		if selector, err := parseSelector(args); err != nil || selector != nil {
			t.Errorf("parseSelector(%q) = %v, %v; want nil (every ready ticket)", args, selector, err)
		}
	}
	for args, want := range map[string][]int{"#5": {5}, "12": {12}, "#12 #15": {12, 15}, "#15 3": {15, 3}} {
		if selector, err := parseSelector(strings.Fields(args)); err != nil || !slices.Equal(selector, want) {
			t.Errorf("parseSelector(%q) = %v, %v; want %v", args, selector, err, want)
		}
	}
	for _, args := range [][]string{{"*", "#1"}, {"#0"}, {"#-1"}, {"#x"}, {"#1", "*"}} {
		if _, err := parseSelector(args); err == nil || !strings.Contains(err.Error(), "afk factory") {
			t.Errorf("parseSelector(%q): err = %v, want usage", args, err)
		}
	}
}

// The error of a Run in a Project without compose.afk.yml gets the template
// offered by `afk factory`, wrapped as the Factory returns it.
func TestFactoryOffersTemplateOnMissingStack(t *testing.T) {
	missing := fmt.Errorf("preflight: %w", stack.ErrNoComposeAfk)
	t.Run("on a terminal, yes", func(t *testing.T) {
		dir := workTree(t)
		var out strings.Builder
		if err := offerTemplateOnFailure(t.Context(), missing, dir, strings.NewReader("y\n"), &out, true); err != nil {
			t.Fatal(err)
		}
		if got := templateFiles(t, dir); len(got) != 2 {
			t.Errorf("template files = %v", got)
		}
	})
	t.Run("on a terminal, no: the error stays", func(t *testing.T) {
		dir := workTree(t)
		if err := offerTemplateOnFailure(t.Context(), missing, dir, strings.NewReader("n\n"), &strings.Builder{}, true); !errors.Is(err, stack.ErrNoComposeAfk) {
			t.Errorf("err = %v", err)
		}
		if got := templateFiles(t, dir); len(got) != 0 {
			t.Errorf("template files = %v", got)
		}
	})
	t.Run("without a terminal: the error says what to do", func(t *testing.T) {
		err := offerTemplateOnFailure(t.Context(), missing, workTree(t), strings.NewReader("y\n"), &strings.Builder{}, false)
		if err == nil || !strings.Contains(err.Error(), "terminal") {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("other errors and success pass", func(t *testing.T) {
		boom := errors.New("boom")
		if err := offerTemplateOnFailure(t.Context(), boom, workTree(t), strings.NewReader(""), &strings.Builder{}, true); err != boom {
			t.Errorf("err = %v", err)
		}
		if err := offerTemplateOnFailure(t.Context(), nil, "", nil, nil, true); err != nil {
			t.Errorf("err = %v", err)
		}
	})
}

// A Project with a local forge has no Factory yet: refused before anything starts.
func TestFactoryRefusesLocalForge(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "")
	root := t.TempDir()
	origin, dir := filepath.Join(root, "origin.git"), filepath.Join(root, "work")
	cfg := "[forge]\nkind = \"local\"\nrepo = \"o/r\"\n[gate]\ncmd = \"true\"\n"
	for _, args := range [][]string{
		{"init", "-q", "--bare", "-b", "main", origin},
		{"init", "-q", "-b", "main", dir},
		{"-C", dir, "remote", "add", "origin", origin},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, ".afk.toml"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"-C", dir, "add", ".afk.toml"},
		{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", "config"},
		{"-C", dir, "push", "-q", "origin", "main"},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if err := runFactory(t.Context(), nil, dir, io.Discard); !errors.Is(err, errLocalFactory) {
		t.Errorf("err = %v, want %v", err, errLocalFactory)
	}
}
