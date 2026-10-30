package stack

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const (
	composeBinary = "docker-compose"
	// errorTail is how much of a command's output an error carries.
	errorTail = 4096
)

// composeService is the part of a service of `docker-compose config` we use.
type composeService struct {
	Build   map[string]any `json:"build"`
	Volumes []struct {
		Type   string `json:"type"`
		Source string `json:"source"`
	} `json:"volumes"`
}

// config resolves the Project's two compose files.
func (s *Stack) config(ctx context.Context) (services map[string]composeService, err error) {
	var out bytes.Buffer
	if err = s.compose(ctx, false, &out, "--profile", "*", "config", "--format", "json"); err != nil {
		return
	}
	var parsed struct {
		Services map[string]composeService `json:"services"`
	}
	if err = json.Unmarshal(out.Bytes(), &parsed); err != nil {
		err = fmt.Errorf("parsing the compose config: %w", err)
		return
	}
	services = parsed.Services
	return
}

// compose runs docker-compose against the rootless podman socket, for this
// Stack's project. It reads only files of the Stack's state: the export of
// the base branch and the generated override (ADR-0010). Without override the
// generated file is left out, for the commands that run before it exists.
// The socket is only in the environment of this process, never of the Sandbox.
func (s *Stack) compose(ctx context.Context, override bool, stdout io.Writer, args ...string) (err error) {
	cmd, err := s.command(ctx, override, args...)
	if err != nil {
		return
	}
	tail := &tailBuffer{max: errorTail}
	cmd.Stderr = tail
	if s.opts.Out != nil {
		cmd.Stderr = io.MultiWriter(tail, s.opts.Out)
	}
	cmd.Stdout = stdout
	if stdout == nil {
		cmd.Stdout = io.Discard
		if s.opts.Out != nil {
			cmd.Stdout = s.opts.Out
		}
	}
	if err = cmd.Run(); err != nil {
		err = fmt.Errorf("%s %s: %w: %s", composeBinary, args[0], err, strings.TrimSpace(tail.String()))
	}
	return
}

// command is the docker-compose command of compose, not started: streams are
// the caller's.
func (s *Stack) command(ctx context.Context, override bool, args ...string) (cmd *exec.Cmd, err error) {
	if _, err = os.Stat(s.socket); err != nil {
		err = fmt.Errorf("podman socket: %w (systemctl --user enable --now podman.socket)", err)
		return
	}
	global := []string{"--ansi", "never", "--progress", "plain", "--project-name", s.ProjectName()}
	if _, statErr := os.Stat(s.exportDir()); statErr == nil {
		global = append(global, "--project-directory", s.exportDir(),
			"--file", filepath.Join(s.exportDir(), composeFile), "--file", filepath.Join(s.exportDir(), composeAfkFile))
		if _, statErr = os.Stat(s.overridePath()); override && statErr == nil {
			global = append(global, "--file", s.overridePath())
		}
	} else if args[0] != "down" && args[0] != "rm" {
		err = errors.New("the Stack has no export of the base branch: Up first")
		return
	}
	cmd = exec.CommandContext(ctx, composeBinary, append(global, args...)...)
	cmd.Dir = s.dir // nothing outside the state is ever the project directory
	cmd.Env = append(hostEnv(os.Environ()), "DOCKER_HOST=unix://"+s.socket, "AFK_TREE="+s.Tree(), "AFK_CACHE="+s.cacheScope())
	return
}

// hostEnv is env without what steers compose or the container client: the
// environment of the host must not change where or how a Stack starts.
func hostEnv(env []string) (kept []string) {
	for _, entry := range env {
		if strings.HasPrefix(entry, "COMPOSE_") || strings.HasPrefix(entry, "DOCKER_") || strings.HasPrefix(entry, "CONTAINER_") {
			continue
		}
		kept = append(kept, entry)
	}
	return
}

// rejectSocketMounts fails when a bind mount source is the podman socket or
// a directory holding it: the socket must never reach the Sandbox.
func rejectSocketMounts(socket string, sources map[string][]string) (err error) {
	for service, list := range sources {
		for _, source := range list {
			if source == "" || !filepath.IsAbs(source) {
				continue
			}
			if rel, relErr := filepath.Rel(source, socket); relErr == nil && !strings.HasPrefix(rel, "..") {
				err = fmt.Errorf("service %s mounts %s, which holds the podman socket", service, source)
				return
			}
		}
	}
	return
}

// tailBuffer keeps the last max bytes written to it.
type tailBuffer struct {
	max int
	buf []byte
}

func (t *tailBuffer) Write(p []byte) (n int, err error) {
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.max {
		t.buf = t.buf[len(t.buf)-t.max:]
	}
	n = len(p)
	return
}

func (t *tailBuffer) String() (s string) { return string(t.buf) }
