// Package image builds Base images from Templates with the Packer CLI (ADR-0006).
package image

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"net"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/graf0/afk/config"
	"github.com/graf0/afk/egress"
)

const (
	varFile    = "vars.json"
	imageGroup = "kvm"
)

var projectNamePattern = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]*$`)

// Options configures Build.
type Options struct {
	Paths  Paths
	Config config.Config
	Out    io.Writer // build progress, as it happens

	// Abort, once closed, kills Packer and everything it started right away
	// instead of waiting for it to stop QEMU after ctx is cancelled (nil: never).
	Abort <-chan struct{}
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
// installs it in a new version directory Paths.BaseDir/<project>/<version>
// (older versions stay). It returns that directory. Cancelling ctx interrupts
// Packer, which stops QEMU.
//
// Internet access during the build goes only through an Egress proxy
// (all public domains) on a random loopback port, tunneled into the guest.
func Build(ctx context.Context, opts Options) (imageDir string, err error) {
	cfg := opts.Config
	if opts.Out == nil {
		opts.Out = io.Discard
	}
	tmpl, ok := templates[cfg.Image.Template]
	if !ok {
		err = fmt.Errorf("unsupported image.template %q: only %q are implemented", cfg.Image.Template, slices.Sorted(maps.Keys(templates)))
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
	packerBin, err := ensurePacker(ctx, http.DefaultClient, paths.binDir(), pinnedPacker)
	if err != nil {
		err = fmt.Errorf("preparing packer: %w", err)
		return
	}
	keyPath, publicKey, err := ensureSSHKey(paths.sshDir())
	if err != nil {
		return
	}

	for _, dir := range []string{paths.pluginDir(), paths.packerConfigDir()} {
		if err = os.MkdirAll(dir, 0o755); err != nil {
			err = fmt.Errorf("creating %s: %w", dir, err)
			return
		}
	}
	projectDir := paths.projectDir(project)
	for _, dir := range []string{paths.BaseDir, projectDir} {
		if err = EnsureSharedDir(dir, gid); err != nil {
			return
		}
	}
	// Work files live in the cache; the disk is built next to its final place
	// (same filesystem for an atomic move, and not on tmpfs like /tmp).
	workDir, cleanWork, err := tempDir(paths.CacheDir, "build-*")
	if err != nil {
		return
	}
	defer cleanWork()
	buildDir, cleanBuild, err := tempDir(projectDir, ".build-*")
	if err != nil {
		return
	}
	defer cleanBuild()

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
	provision, final := provisionSteps(cfg.Image.Provision)
	vars := packerVars{
		BaseImage:      tmpl.ImageURL,
		BaseChecksum:   tmpl.ChecksumURL,
		DiskName:       tmpl.DiskName,
		OutputDir:      outputDir,
		SSHKey:         keyPath,
		SSHPort:        sshPort,
		ProxyPort:      proxyPort,
		SeedFiles:      seedFiles,
		Provision:      provision,
		FinalProvision: final,
	}
	if err = writeVarFile(filepath.Join(workDir, varFile), vars); err != nil {
		return
	}
	if err = os.WriteFile(filepath.Join(workDir, tmpl.hclFile()), []byte(tmpl.HCL), 0o600); err != nil {
		err = fmt.Errorf("writing template: %w", err)
		return
	}

	_, _ = fmt.Fprintf(opts.Out, "==> afk: installing packer plugins\n")
	initCmd := packerCommand(ctx, opts.Abort, packerBin, paths, workDir, "init", tmpl.hclFile())
	if err = runPacker(initCmd, opts.Out); err != nil {
		err = fmt.Errorf("packer init: %w", err)
		return
	}
	_, _ = fmt.Fprintf(opts.Out, "==> afk: building %s image for %s\n", tmpl.Name, project)
	buildCmd := packerCommand(ctx, opts.Abort, packerBin, paths, workDir, "build", "-machine-readable", "-var-file="+varFile, tmpl.hclFile())
	if err = runPackerMachineReadable(buildCmd, opts.Out); err != nil {
		err = fmt.Errorf("packer build: %w", err)
		return
	}

	imageDir = filepath.Join(projectDir, newVersion(time.Now()))
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

// listenLoopback listens on a random loopback TCP port.
func listenLoopback() (ln net.Listener, port int, err error) {
	ln, err = net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		err = fmt.Errorf("listening on loopback: %w", err)
		return
	}
	port = ln.Addr().(*net.TCPAddr).Port
	return
}

// startProxy serves an Egress proxy allowing all public domains on a random
// loopback port. stop shuts it down and waits for it.
func startProxy(ctx context.Context) (port int, stop func(), err error) {
	ln, port, err := listenLoopback()
	if err != nil {
		return
	}
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
	ln, port, err := listenLoopback()
	if err != nil {
		return
	}
	err = ln.Close()
	return
}

// tempDir creates a temp directory in parent; cleanup removes it.
func tempDir(parent, pattern string) (dir string, cleanup func(), err error) {
	if err = os.MkdirAll(parent, 0o755); err != nil {
		err = fmt.Errorf("creating %s: %w", parent, err)
		return
	}
	dir, err = os.MkdirTemp(parent, pattern)
	if err != nil {
		err = fmt.Errorf("creating temp dir in %s: %w", parent, err)
		return
	}
	cleanup = func() { _ = os.RemoveAll(dir) }
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

// installArtifact gives src's tree to group gid with read access for the
// group, then moves it to dest, a version directory that must not exist yet.
func installArtifact(src, dest string, gid int) (err error) {
	err = filepath.WalkDir(src, func(path string, entry fs.DirEntry, walkErr error) (err error) {
		if walkErr != nil {
			return walkErr
		}
		mode := os.FileMode(0o640)
		if entry.IsDir() {
			mode = sharedDirMode
		}
		if err = os.Chown(path, -1, gid); err != nil {
			return
		}
		return os.Chmod(path, mode)
	})
	if err != nil {
		err = fmt.Errorf("setting permissions of %s: %w", src, err)
		return
	}
	if _, statErr := os.Lstat(dest); statErr == nil {
		err = fmt.Errorf("installing image to %s: version already exists", dest)
		return
	}
	if err = os.Rename(src, dest); err != nil {
		err = fmt.Errorf("installing image to %s: %w", dest, err)
	}
	return
}
