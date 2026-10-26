package run

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/graf0/afk/config"
	"github.com/graf0/afk/image"
)

func imageTestBase() (base Base) {
	base.Config.Forge.Repo = "a/b"
	base.Config.Machine.Sandbox = config.Machine{Template: "debian13", MaxAge: "168h"}
	return
}

func TestEnsureSandboxImageBuildsWithLogAndOneLine(t *testing.T) {
	paths := image.Paths{BaseDir: t.TempDir(), StateDir: t.TempDir()}
	var out bytes.Buffer
	build := func(ctx context.Context, opts image.Options) (dir string, err error) {
		_, _ = opts.Out.Write([]byte("packer output\n"))
		dir = filepath.Join(paths.BaseDir, "a-b", "sandbox", opts.Version)
		err = os.MkdirAll(dir, 0o755)
		return
	}

	if err := ensureSandboxImage(t.Context(), imageTestBase(), paths, &out, build); err != nil {
		t.Fatal(err)
	}
	logs, _ := filepath.Glob(filepath.Join(paths.StateDir, "a-b", "logs", "image-sandbox-*.log"))
	if len(logs) != 1 {
		t.Fatalf("logs = %q, want one image-sandbox-<version>.log", logs)
	}
	if data, _ := os.ReadFile(logs[0]); string(data) != "packer output\n" {
		t.Errorf("log = %q, want the build output", data)
	}
	want := "= image sandbox: building (no image), log: " + logs[0] + "\n"
	if out.String() != want {
		t.Errorf("out = %q, want %q", out.String(), want)
	}
}

func TestEnsureSandboxImageBuildFailureNamesLog(t *testing.T) {
	paths := image.Paths{BaseDir: t.TempDir(), StateDir: t.TempDir()}
	boom := errors.New("boom")
	build := func(ctx context.Context, opts image.Options) (dir string, err error) { return "", boom }

	err := ensureSandboxImage(t.Context(), imageTestBase(), paths, &bytes.Buffer{}, build)
	if !errors.Is(err, boom) || !strings.Contains(err.Error(), "image-sandbox-") {
		t.Errorf("err = %v, want the build error with the log path", err)
	}
}
