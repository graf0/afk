package stack

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteTemplate(t *testing.T) {
	dir := t.TempDir()
	if err := WriteTemplate(dir); err != nil {
		t.Fatal(err)
	}
	compose, err := os.ReadFile(filepath.Join(dir, composeAfkFile))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"context: .afk", "${AFK_TREE}", "${HOME}/go/pkg/mod", "${AFK_CACHE}", "# afk-setup:"} {
		if !strings.Contains(string(compose), want) {
			t.Errorf("%s lacks %q", composeAfkFile, want)
		}
	}
	dockerfile, err := os.ReadFile(filepath.Join(dir, dockerfileAfk))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"debian:13", "git", "ca-certificates", "curl"} {
		if !strings.Contains(string(dockerfile), want) {
			t.Errorf("%s lacks %q", dockerfileAfk, want)
		}
	}
}

func TestWriteTemplateKeepsExistingFiles(t *testing.T) {
	for _, existing := range []string{composeAfkFile, dockerfileAfk} {
		t.Run(existing, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.MkdirAll(filepath.Join(dir, filepath.Dir(existing)), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, existing), []byte("mine\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			err := WriteTemplate(dir)
			if err == nil || !strings.Contains(err.Error(), existing) || !strings.Contains(err.Error(), "not overwritten") {
				t.Fatalf("err = %v, want one naming %s and saying it is not overwritten", err, existing)
			}
			if got := ExistingTemplateFiles(dir); len(got) != 1 || got[0] != existing {
				t.Errorf("files = %v, want only %s", got, existing)
			}
			if got, _ := os.ReadFile(filepath.Join(dir, existing)); string(got) != "mine\n" {
				t.Errorf("%s = %q, was overwritten", existing, got)
			}
		})
	}
}
