// Package image builds Base images from Templates with the Packer CLI (ADR-0006).
package image

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/graf0/afk/config"
	"github.com/graf0/afk/egress"
)

const (
	debian13Name = "debian13"

	debian13Image    = "https://cloud.debian.org/images/cloud/trixie/latest/debian-13-genericcloud-amd64.qcow2"
	debian13Checksum = "file:https://cloud.debian.org/images/cloud/trixie/latest/SHA512SUMS"

	templateFile = "debian13.pkr.hcl"
	varFile      = "vars.json"

	imageGroup = "kvm"
)

var projectNamePattern = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]*$`)

// Options configures Build.
type Options struct {
	Paths  Paths
	Config config.Config
	Out    io.Writer // build progress, as it happens
}

// --- naming ---

// ProjectName derives the Base image directory name from [forge].repo
// ("graf0/dfmx" becomes "graf0-dfmx").
func ProjectName(repo string) (name string, err error) {
	name = strings.ReplaceAll(repo, "/", "-")
	if !projectNamePattern.MatchString(name) || strings.Contains(name, "..") {
		err = fmt.Errorf("cannot derive a project name from forge.repo %q", repo)
		name = ""
	}
	return
}

// --- build ---

// Build builds the Base image of the Project described by opts.Config and
// installs it in Paths.BaseDir/<project>, replacing the previous one. It
// returns that directory. Cancelling ctx interrupts Packer, which stops QEMU.
//
// Internet access during the build goes only through an Egress proxy
// (all public domains) on a random loopback port, tunneled into the guest.
func Build(ctx context.Context, opts Options) (imageDir string, err error) {
	cfg := opts.Config
	if opts.Out == nil {
		opts.Out = io.Discard
	}
	if cfg.Image.Template != debian13Name {
		err = fmt.Errorf("unsupported image.template %q: only %q is implemented", cfg.Image.Template, debian13Name)
		return
	}
	project, err := ProjectName(cfg.Forge.Repo)
	if err != nil {
		return
	}
	gid, err := lookupGroupID(imageGroup)
	if err != nil {
		return
	}
	paths := opts.Paths
	packerBin, err := ensurePacker(ctx, http.DefaultClient, paths.BinDir(), pinnedPacker)
	if err != nil {
		err = fmt.Errorf("preparing packer: %w", err)
		return
	}
	keyPath, publicKey, err := ensureSSHKey(paths.SSHDir())
	if err != nil {
		return
	}

	for _, dir := range []string{paths.PluginDir(), paths.PackerConfigDir(), paths.CacheDir} {
		if err = os.MkdirAll(dir, 0o755); err != nil {
			err = fmt.Errorf("creating %s: %w", dir, err)
			return
		}
	}
	if err = os.MkdirAll(paths.BaseDir, 0o2775); err != nil {
		err = fmt.Errorf("creating %s: %w", paths.BaseDir, err)
		return
	}
	// Work files live in the cache; the disk is built next to its final place
	// (same filesystem for an atomic move, and not on tmpfs like /tmp).
	workDir, err := os.MkdirTemp(paths.CacheDir, "build-*")
	if err != nil {
		err = fmt.Errorf("creating work dir: %w", err)
		return
	}
	defer func() { _ = os.RemoveAll(workDir) }()
	buildDir, err := os.MkdirTemp(paths.BaseDir, ".build-*")
	if err != nil {
		err = fmt.Errorf("creating build dir: %w", err)
		return
	}
	defer func() { _ = os.RemoveAll(buildDir) }()

	proxyPort, stopProxy, err := startProxy(ctx)
	if err != nil {
		return
	}
	defer stopProxy()
	sshPort, err := freePort()
	if err != nil {
		return
	}

	seedFiles, err := writeSeed(workDir, publicKey)
	if err != nil {
		return
	}
	outputDir := filepath.Join(buildDir, "out")
	vars := Vars{
		BaseImage:    debian13Image,
		BaseChecksum: debian13Checksum,
		OutputDir:    outputDir,
		SSHKey:       keyPath,
		SSHPort:      sshPort,
		ProxyPort:    proxyPort,
		SeedFiles:    seedFiles,
		Provision:    provisionSteps(cfg.Image.Provision),
	}
	varPath := filepath.Join(workDir, varFile)
	if err = writeVarFile(varPath, vars); err != nil {
		return
	}
	if err = os.WriteFile(filepath.Join(workDir, templateFile), []byte(debian13Template), 0o600); err != nil {
		err = fmt.Errorf("writing template: %w", err)
		return
	}

	_, _ = fmt.Fprintf(opts.Out, "==> afk: installing packer plugins\n")
	initCmd := packerCommand(ctx, packerBin, paths, workDir, "init", templateFile)
	if err = runPacker(initCmd, opts.Out, false); err != nil {
		err = fmt.Errorf("packer init: %w", err)
		return
	}
	_, _ = fmt.Fprintf(opts.Out, "==> afk: building %s image for %s\n", debian13Name, project)
	buildCmd := packerCommand(ctx, packerBin, paths, workDir, "build", "-machine-readable", "-var-file="+varFile, templateFile)
	if err = runPacker(buildCmd, opts.Out, true); err != nil {
		err = fmt.Errorf("packer build: %w", err)
		return
	}

	imageDir = paths.ImageDir(project)
	if err = installArtifact(outputDir, imageDir, gid); err != nil {
		imageDir = ""
		return
	}
	_, _ = fmt.Fprintf(opts.Out, "==> afk: image ready in %s\n", imageDir)
	return
}

// --- build helpers ---

// writeSeed writes the cloud-init NoCloud files into workDir and returns their paths.
func writeSeed(workDir, publicKey string) (paths []string, err error) {
	files, err := renderSeed(publicKey)
	if err != nil {
		return
	}
	seedDir := filepath.Join(workDir, "seed")
	if err = os.Mkdir(seedDir, 0o700); err != nil {
		err = fmt.Errorf("creating %s: %w", seedDir, err)
		return
	}
	for _, name := range []string{"user-data", "meta-data"} {
		path := filepath.Join(seedDir, name)
		if err = os.WriteFile(path, []byte(files[name]), 0o600); err != nil {
			err = fmt.Errorf("writing %s: %w", path, err)
			return
		}
		paths = append(paths, path)
	}
	return
}

// startProxy serves an Egress proxy allowing all public domains on a random
// loopback port. stop shuts it down and waits for it.
func startProxy(ctx context.Context) (port int, stop func(), err error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		err = fmt.Errorf("listening for egress proxy: %w", err)
		return
	}
	port = ln.Addr().(*net.TCPAddr).Port
	proxyCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = egress.New(egress.AllowAll()).Serve(proxyCtx, ln)
	}()
	stop = func() {
		cancel()
		<-done
	}
	return
}

// freePort returns a currently unused loopback TCP port. It is released before
// return, so another process could grab it before QEMU does; Packer then fails
// loudly and the build can be retried.
func freePort() (port int, err error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		err = fmt.Errorf("finding free port: %w", err)
		return
	}
	port = ln.Addr().(*net.TCPAddr).Port
	err = ln.Close()
	return
}

func lookupGroupID(name string) (gid int, err error) {
	group, err := user.LookupGroup(name)
	if err != nil {
		err = fmt.Errorf("looking up group %q: %w", name, err)
		return
	}
	gid, err = strconv.Atoi(group.Gid)
	if err != nil {
		err = fmt.Errorf("parsing gid of group %q: %w", name, err)
	}
	return
}

// --- artifact ---

// installArtifact gives src's tree to group gid (skipped when negative) with
// read access for the group, then moves it to dest, replacing an existing
// directory. If the move fails the old directory is restored.
func installArtifact(src, dest string, gid int) (err error) {
	err = filepath.WalkDir(src, func(path string, entry fs.DirEntry, walkErr error) (err error) {
		if walkErr != nil {
			return walkErr
		}
		mode := os.FileMode(0o640)
		if entry.IsDir() {
			mode = os.ModeSetgid | 0o775
		}
		if gid >= 0 {
			if err = os.Chown(path, -1, gid); err != nil {
				return
			}
		}
		return os.Chmod(path, mode)
	})
	if err != nil {
		err = fmt.Errorf("setting permissions of %s: %w", src, err)
		return
	}

	backup := ""
	if _, statErr := os.Stat(dest); statErr == nil {
		backup = fmt.Sprintf("%s.old-%d", dest, os.Getpid())
		if err = os.Rename(dest, backup); err != nil {
			err = fmt.Errorf("moving old image aside: %w", err)
			return
		}
	}
	if err = os.Rename(src, dest); err != nil {
		if backup != "" {
			_ = os.Rename(backup, dest)
		}
		err = fmt.Errorf("installing image to %s: %w", dest, err)
		return
	}
	if backup != "" {
		_ = os.RemoveAll(backup)
	}
	return
}
