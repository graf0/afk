package stack

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// gitRepo makes a repository with the files committed on main and returns
// its path and the commit.
func gitRepo(t *testing.T, files map[string]string) (repo, commit string) {
	t.Helper()
	repo = t.TempDir()
	for name, content := range files {
		path := filepath.Join(repo, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gitIn(t, repo, "init", "-q", "-b", "main")
	gitIn(t, repo, "add", "-A")
	gitIn(t, repo, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", "x")
	commit = gitIn(t, repo, "rev-parse", "HEAD")
	return
}

func gitIn(t *testing.T, dir string, args ...string) (out string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	data, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, data)
	}
	return strings.TrimSpace(string(data))
}

func TestExport(t *testing.T) {
	repo, commit := gitRepo(t, map[string]string{
		composeFile:         "services: {}\n",
		composeAfkFile:      "services: {}\n",
		"docker/Dockerfile": "FROM scratch\n",
	})
	// The working tree differs from the commit: only the commit counts.
	if err := os.WriteFile(filepath.Join(repo, composeFile), []byte("evil\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "export")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "stale"), nil, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := export(t.Context(), repo, commit, dir); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dir, composeFile))
	if err != nil || string(got) != "services: {}\n" {
		t.Errorf("exported %s = %q, %v, want the committed one", composeFile, got, err)
	}
	if _, err = os.Stat(filepath.Join(dir, "docker", "Dockerfile")); err != nil {
		t.Errorf("Dockerfile not exported: %v", err)
	}
	if _, err = os.Stat(filepath.Join(dir, "stale")); err == nil {
		t.Error("stale file of an earlier export survived")
	}
}

func TestExportMissingComposeFile(t *testing.T) {
	for name, missing := range map[string][]string{
		composeFile:    {composeFile},
		composeAfkFile: {composeAfkFile},
		"both":         {composeFile, composeAfkFile},
	} {
		t.Run(name, func(t *testing.T) {
			files := map[string]string{composeFile: "x", composeAfkFile: "x", "other": "x"}
			for _, m := range missing {
				delete(files, m)
			}
			repo, commit := gitRepo(t, files)
			err := export(t.Context(), repo, commit, filepath.Join(t.TempDir(), "export"))
			if err == nil || !strings.Contains(err.Error(), "base branch") {
				t.Fatalf("err = %v, want one naming the base branch", err)
			}
			for _, m := range missing {
				if !strings.Contains(err.Error(), m) {
					t.Errorf("err = %v, does not name %s", err, m)
				}
			}
			if got := errors.Is(err, ErrNoComposeAfk); got != slices.Contains(missing, composeAfkFile) {
				t.Errorf("errors.Is(err, ErrNoComposeAfk) = %v for missing %v", got, missing)
			}
		})
	}
}
