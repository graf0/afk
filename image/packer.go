package image

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)

// interruptGrace is how long Packer gets after SIGINT to stop QEMU before it is killed.
const interruptGrace = 2 * time.Minute

// packerCommand builds the Packer invocation: the given binary (never one from
// PATH), run in dir with afk's own plugin and config directories. The process
// gets its own process group so the terminal's Ctrl-C does not reach it
// directly; cancelling ctx sends it exactly one SIGINT, after which Packer
// stops QEMU and exits.
func packerCommand(ctx context.Context, bin string, paths Paths, dir string, args ...string) (cmd *exec.Cmd) {
	cmd = exec.CommandContext(ctx, bin, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"PACKER_PLUGIN_PATH="+paths.PluginDir(),
		"PACKER_CONFIG_DIR="+paths.PackerConfigDir(),
		"CHECKPOINT_DISABLE=1",
	)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGINT) }
	cmd.WaitDelay = interruptGrace
	return
}

// runPacker runs cmd, writing its output to out as it arrives. With
// machineReadable the stdout is parsed as `-machine-readable` events and the
// error includes Packer's error messages.
func runPacker(cmd *exec.Cmd, out io.Writer, machineReadable bool) (err error) {
	shared := &syncWriter{w: out}
	cmd.Stderr = shared
	if !machineReadable {
		cmd.Stdout = shared
		if err = cmd.Run(); err != nil {
			err = fmt.Errorf("running %s: %w", cmd.Args[0], err)
		}
		return
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		err = fmt.Errorf("opening packer stdout: %w", err)
		return
	}
	if err = cmd.Start(); err != nil {
		err = fmt.Errorf("starting %s: %w", cmd.Args[0], err)
		return
	}
	buildErrors, streamErr := streamEvents(stdout, shared)
	waitErr := cmd.Wait()
	switch {
	case waitErr != nil:
		err = fmt.Errorf("packer failed: %w", waitErr)
		if len(buildErrors) > 0 {
			err = fmt.Errorf("%w: %s", err, strings.Join(buildErrors, "; "))
		}
	case streamErr != nil:
		err = streamErr
	}
	return
}

// syncWriter serializes writes from Packer's stdout and stderr readers.
type syncWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (s *syncWriter) Write(p []byte) (n int, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}
