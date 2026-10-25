package image

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/graf0/afk/config"
)

// lockPollInterval is how often a waiting Ensure tries the build lock again.
var lockPollInterval = 200 * time.Millisecond

// EnsureOptions configures Ensure.
type EnsureOptions struct {
	Paths   Paths
	Project string
	Machine string
	Hash    string           // ConfigHash of the current configuration
	MaxAge  time.Duration    // Expiry; 0: none
	Now     func() time.Time // nil: time.Now

	// Build builds the version named version and returns its directory.
	Build func(ctx context.Context, version string) (dir string, err error)
	// Say reports what Ensure is doing, in one line: "building (<reason>)"
	// with the version about to be built, or the wait for another Factory's
	// build with no version (nil: silent).
	Say func(msg string, version string)
}

// --- hash ---

// ConfigHash identifies what a Base image of cfg's Sandbox VM is made of:
// the Template (its source image, Packer template, cloud-init seed) and the
// whole guest script in order: afk's initial steps, plugins and marketplaces
// (cfg.Agent and the Project's .claude/settings.json, nil: none),
// [machine.sandbox].build, the Tools' build and afk's final steps. Memory,
// cpus, max_age and the Tools' env, prompt and setup do not change the image,
// so they are not in it.
func ConfigHash(cfg config.Config, settings []byte) (hash string, err error) {
	tmpl, provision, final, err := guestScript(cfg, settings)
	if err != nil {
		return
	}
	hash = configHash(tmpl, provision, final)
	return
}

// guestScript is the Template of cfg's Sandbox VM and the steps that Build
// runs in it (provision, then final) and ConfigHash covers: the one place
// where they are put together.
func guestScript(cfg config.Config, settings []byte) (tmpl imageTemplate, provision, final []string, err error) {
	machineCfg := cfg.Machine.Sandbox
	tmpl, ok := templates[machineCfg.Template]
	if !ok {
		err = fmt.Errorf("unsupported machine.%s.template %q: only %q are implemented", config.SandboxMachine, machineCfg.Template, slices.Sorted(maps.Keys(templates)))
		return
	}
	pluginSteps, err := pluginInstallSteps(cfg.Agent, settings)
	if err != nil {
		return
	}
	provision, final = provisionSteps(pluginSteps, machineCfg.Build, cfg.Tool)
	return
}

// configHash is the SHA-256 of everything an image is built from.
func configHash(tmpl imageTemplate, provision, final []string) (hash string) {
	// Marshal cannot fail on strings and string slices.
	data, _ := json.Marshal(struct {
		Template  imageTemplate
		UserData  string
		MetaData  string
		Provision []string
		Final     []string
	}{tmpl, userDataTemplate, metaData, provision, final})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// --- ensure ---

// Ensure returns the directory of the Base image of the machine to use,
// building a new version first when the newest one is missing, older than
// MaxAge or made from another configuration (older versions are not
// searched for a matching hash). It builds under a lock per Project and
// decides again once it has it, as another Factory may have built meanwhile;
// a second caller waits for the lock instead of building in parallel.
func Ensure(ctx context.Context, opts EnsureOptions) (dir string, err error) {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	dir, reason, err := decide(opts)
	if err != nil || reason == "" {
		return
	}
	say := func(msg, version string) {
		if opts.Say != nil {
			opts.Say(msg, version)
		}
	}
	unlock, err := lockProject(ctx, filepath.Join(opts.Paths.StateDir, opts.Project, "image.lock"), func() {
		say("another Factory is building the image, waiting", "")
	})
	if err != nil {
		return
	}
	defer unlock()
	if dir, reason, err = decide(opts); err != nil || reason == "" {
		return
	}
	version := newVersion(opts.Now())
	say("building ("+reason+")", version)
	dir, err = opts.Build(ctx, version)
	return
}

// decide returns the newest image to use, or the reason to build instead.
func decide(opts EnsureOptions) (dir, reason string, err error) {
	dir, err = Latest(opts.Paths, opts.Project, opts.Machine)
	if errors.Is(err, ErrNoImage) {
		dir, reason, err = "", "no image", nil
		return
	}
	if err != nil {
		return
	}
	built, err := time.Parse(versionFormat, filepath.Base(dir))
	if err != nil {
		err = fmt.Errorf("parsing the version of %s: %w", dir, err)
		return
	}
	if age := opts.Now().Sub(built); opts.MaxAge > 0 && age > opts.MaxAge {
		reason = fmt.Sprintf("expired %s > %s", age.Round(time.Minute), opts.MaxAge)
		return
	}
	data, readErr := os.ReadFile(filepath.Join(dir, hashFile))
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		err = fmt.Errorf("reading %s: %w", hashFile, readErr)
		return
	}
	if strings.TrimSpace(string(data)) != opts.Hash {
		reason = "configuration changed"
	}
	return
}

// --- lock ---

// lockProject takes an exclusive flock on path (created with its directory),
// waiting for another holder until ctx ends. The kernel drops the lock with
// the process, so it never outlives a crashed Factory.
func lockProject(ctx context.Context, path string, waiting func()) (unlock func(), err error) {
	if err = os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		err = fmt.Errorf("creating %s: %w", filepath.Dir(path), err)
		return
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		err = fmt.Errorf("opening the build lock: %w", err)
		return
	}
	for told := false; ; {
		lockErr := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if lockErr == nil {
			break
		}
		if !errors.Is(lockErr, syscall.EWOULDBLOCK) {
			_ = file.Close()
			err = fmt.Errorf("locking %s: %w", path, lockErr)
			return
		}
		if !told && waiting != nil {
			told = true
			waiting()
		}
		select {
		case <-ctx.Done():
			_ = file.Close()
			err = fmt.Errorf("waiting for the image build lock: %w", ctx.Err())
			return
		case <-time.After(lockPollInterval):
		}
	}
	unlock = func() { _ = file.Close() } // closing releases the lock
	return
}
