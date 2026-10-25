package run

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/graf0/afk/config"
	"github.com/graf0/afk/image"
)

// buildFunc is image.Build; tests inject another.
type buildFunc func(ctx context.Context, opts image.Options) (dir string, err error)

// ensureSandboxImage makes sure the Project has a Base image of the Sandbox VM
// that is current for base.Config and not expired (image.Ensure). A build
// is announced on out in one line with its reason; its full output goes to
// logs/image-sandbox-<version>.log in the Project's state directory.
func ensureSandboxImage(ctx context.Context, base Base, paths image.Paths, out io.Writer, build buildFunc) (err error) {
	cfg := base.Config
	project, err := image.ProjectName(cfg.Forge.Repo)
	if err != nil {
		return
	}
	hash, err := image.ConfigHash(cfg, base.Settings)
	if err != nil {
		return
	}
	maxAge, err := time.ParseDuration(cfg.Machine.Sandbox.MaxAge)
	if err != nil {
		err = fmt.Errorf("machine.%s.max_age: %w", config.SandboxMachine, err)
		return
	}
	logPath := func(version string) (path string) {
		name := fmt.Sprintf("image-%s-%s.log", config.SandboxMachine, version)
		path = filepath.Join(paths.StateDir, project, "logs", name)
		return
	}
	_, err = image.Ensure(ctx, image.EnsureOptions{
		Paths: paths, Project: project, Machine: config.SandboxMachine, Hash: hash, MaxAge: maxAge,
		Say: func(msg, version string) {
			line := fmt.Sprintf("= image %s: %s", config.SandboxMachine, msg)
			if version != "" {
				line += ", log: " + logPath(version)
			}
			_, _ = fmt.Fprintln(out, line)
		},
		Build: func(ctx context.Context, version string) (dir string, err error) {
			path := logPath(version)
			if err = os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				err = fmt.Errorf("creating %s: %w", filepath.Dir(path), err)
				return
			}
			log, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
			if err != nil {
				err = fmt.Errorf("creating the build log: %w", err)
				return
			}
			defer func() { _ = log.Close() }()
			dir, err = build(ctx, image.Options{Paths: paths, Config: cfg, Out: log, ProjectSettings: base.Settings, Version: version})
			if err != nil {
				err = fmt.Errorf("building the image (log: %s): %w", path, err)
			}
			return
		},
	})
	return
}
