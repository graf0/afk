package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/graf0/afk/stack"
)

var errMissing = fmt.Errorf("exporting: %w", stack.ErrNoComposeAfk)

func workTree(t *testing.T) (dir string) {
	t.Helper()
	dir = t.TempDir()
	if out, err := exec.Command("git", "-C", dir, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	return
}

func templateFiles(t *testing.T, dir string) (names []string) {
	t.Helper()
	for _, name := range []string{"compose.afk.yml", ".afk/Dockerfile"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			names = append(names, name)
		}
	}
	return
}

func TestOfferTemplateAccepted(t *testing.T) {
	dir := workTree(t)
	var out strings.Builder
	created, err := offerTemplate(t.Context(), errMissing, dir, strings.NewReader("y\n"), &out, true)
	if err != nil || !created {
		t.Fatalf("offerTemplate = %v, %v; want created", created, err)
	}
	if got := templateFiles(t, dir); len(got) != 2 {
		t.Errorf("files = %v, want both", got)
	}
	for _, want := range []string{"compose.afk.yml", ".afk/Dockerfile", "git add", "git commit", "git push"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
}

func TestOfferTemplateFromSubdirectory(t *testing.T) {
	dir := workTree(t)
	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := offerTemplate(t.Context(), errMissing, sub, strings.NewReader("yes\n"), &strings.Builder{}, true); err != nil {
		t.Fatal(err)
	}
	if got := templateFiles(t, dir); len(got) != 2 {
		t.Errorf("files at the work tree root = %v, want both", got)
	}
}

func TestOfferTemplateDeclined(t *testing.T) {
	for _, answer := range []string{"n\n", "\n", "", "maybe\n"} {
		t.Run(fmt.Sprintf("%q", answer), func(t *testing.T) {
			dir := workTree(t)
			created, err := offerTemplate(t.Context(), errMissing, dir, strings.NewReader(answer), &strings.Builder{}, true)
			if created || !errors.Is(err, stack.ErrNoComposeAfk) {
				t.Errorf("offerTemplate = %v, %v; want the original error", created, err)
			}
			if got := templateFiles(t, dir); len(got) != 0 {
				t.Errorf("files = %v, want none", got)
			}
		})
	}
}

func TestOfferTemplateWithoutTTY(t *testing.T) {
	dir := workTree(t)
	var out strings.Builder
	created, err := offerTemplate(t.Context(), errMissing, dir, strings.NewReader("y\n"), &out, false)
	if created || err == nil {
		t.Fatalf("offerTemplate = %v, %v; want an error", created, err)
	}
	for _, want := range []string{"compose.afk.yml", "terminal", "commit", "push"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q: %v", want, err)
		}
	}
	if out.Len() != 0 || len(templateFiles(t, dir)) != 0 {
		t.Errorf("asked or wrote without a TTY: output %q", out.String())
	}
}

func TestOfferTemplateKeepsExistingFiles(t *testing.T) {
	dir := workTree(t)
	path := filepath.Join(dir, ".afk", "Dockerfile")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	created, err := offerTemplate(t.Context(), errMissing, dir, strings.NewReader("y\n"), &out, true)
	if created || err == nil || !strings.Contains(err.Error(), "not overwritten") {
		t.Fatalf("offerTemplate = %v, %v; want a not-overwritten error", created, err)
	}
	if out.Len() != 0 {
		t.Errorf("asked despite an existing file: %q", out.String())
	}
	if got, _ := os.ReadFile(path); string(got) != "mine\n" {
		t.Errorf(".afk/Dockerfile = %q, was overwritten", got)
	}
	if got := templateFiles(t, dir); len(got) != 1 {
		t.Errorf("files = %v, want only the existing one", got)
	}
}

func TestOfferTemplateFilesNotOnBaseBranch(t *testing.T) {
	dir := workTree(t)
	if err := stack.WriteTemplate(dir); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	created, err := offerTemplate(t.Context(), errMissing, dir, strings.NewReader("y\n"), &out, true)
	if created || err == nil || !strings.Contains(err.Error(), "commit and push") {
		t.Fatalf("offerTemplate = %v, %v; want an instruction to commit and push", created, err)
	}
	if out.Len() != 0 {
		t.Errorf("asked although the files are there: %q", out.String())
	}
}

func TestOfferTemplateOtherError(t *testing.T) {
	boom := errors.New("boom")
	created, err := offerTemplate(t.Context(), boom, workTree(t), strings.NewReader("y\n"), &strings.Builder{}, true)
	if created || err != boom {
		t.Errorf("offerTemplate = %v, %v; want the error untouched", created, err)
	}
}

func TestIsTerminal(t *testing.T) {
	file, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	if isTerminal(file) {
		t.Error("/dev/null is not a terminal")
	}
}
