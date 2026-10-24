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

// pipeCloseDelay is how long output pipes may stay open after the process
// group has been killed (a daemonized escapee could hold them forever).
const pipeCloseDelay = 5 * time.Second

// interruptGrace is how long Packer gets after SIGINT to stop QEMU before its
// whole process group is killed. A variable so tests do not wait two minutes.
var interruptGrace = 2 * time.Minute

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

// packerCommand builds the Packer invocation: the given binary (never one from
// PATH), run in dir with afk's own plugin and config directories and a minimal
// environment (nothing of the host's secrets). The process gets its own
// process group so the terminal's Ctrl-C does not reach it directly, and
// Pdeathsig so it dies with afk. Cancelling ctx sends Packer exactly one
// SIGINT, after which it stops QEMU and exits; if it has not exited when
// interruptGrace passes, or when abort is closed, the whole group (Packer, its
// plugins, QEMU) is killed.
func packerCommand(ctx context.Context, abort <-chan struct{}, bin string, paths Paths, dir string, args ...string) (cmd *exec.Cmd) {
	cmd = exec.CommandContext(ctx, bin, args...)
	cmd.Dir = dir
	cmd.Env = packerEnv(paths)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
	cmd.Cancel = func() (err error) {
		err = cmd.Process.Signal(syscall.SIGINT)
		go killGroupAfter(cmd.Process.Pid, interruptGrace, abort)
		return
	}
	cmd.WaitDelay = interruptGrace + pipeCloseDelay
	return
}

// packerEnv is Packer's whole environment: PATH (qemu, xorriso), HOME, TMPDIR
// and afk's own settings.
func packerEnv(paths Paths) (env []string) {
	env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + os.Getenv("HOME"),
		"PACKER_PLUGIN_PATH=" + paths.pluginDir(),
		"PACKER_CONFIG_DIR=" + paths.packerConfigDir(),
		"CHECKPOINT_DISABLE=1",
	}
	if tmp, ok := os.LookupEnv("TMPDIR"); ok {
		env = append(env, "TMPDIR="+tmp)
	}
	return
}

// killGroupAfter SIGKILLs process group pgid after grace or when abort is
// closed, whichever is first. A group that is already gone is left alone.
func killGroupAfter(pgid int, grace time.Duration, abort <-chan struct{}) {
	timer := time.NewTimer(grace)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-abort:
	}
	if syscall.Kill(-pgid, 0) == nil {
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
	}
}

// runPacker runs cmd, writing its output to out as it arrives.
func runPacker(cmd *exec.Cmd, out io.Writer) (err error) {
	shared := &syncWriter{w: out}
	cmd.Stdout, cmd.Stderr = shared, shared
	if err = cmd.Run(); err != nil {
		err = fmt.Errorf("running %s: %w", cmd.Args[0], err)
	}
	return
}

// runPackerMachineReadable runs cmd, parsing its stdout as `-machine-readable`
// events and writing them readably to out as they arrive. The error includes
// Packer's error messages.
func runPackerMachineReadable(cmd *exec.Cmd, out io.Writer) (err error) {
	shared := &syncWriter{w: out}
	cmd.Stderr = shared
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
