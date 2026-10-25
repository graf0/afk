package image

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/graf0/afk/config"
)

var ensureNow = time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)

// ensureEnv is Ensure on a temp dir with a build that installs a version
// with the hash it is given and counts its calls.
type ensureEnv struct {
	opts   EnsureOptions
	builds atomic.Int32
	reason string
}

func newEnsureEnv(t *testing.T, hash string, maxAge time.Duration) (env *ensureEnv) {
	t.Helper()
	paths := Paths{BaseDir: t.TempDir(), StateDir: t.TempDir()}
	env = &ensureEnv{}
	env.opts = EnsureOptions{
		Paths: paths, Project: "p", Machine: "sandbox", Hash: hash, MaxAge: maxAge,
		Now: func() time.Time { return ensureNow },
		Say: func(msg, version string) {
			if version != "" {
				env.reason = strings.TrimSuffix(strings.TrimPrefix(msg, "building ("), ")")
			}
		},
		Build: func(ctx context.Context, version string) (dir string, err error) {
			env.builds.Add(1)
			return env.install(t, version, hash), nil
		},
	}
	return
}

// install makes a version directory with the hash file (none when hash is "").
func (env *ensureEnv) install(t *testing.T, version, hash string) (dir string) {
	t.Helper()
	dir = filepath.Join(env.opts.Paths.machineDir("p", "sandbox"), version)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if hash != "" {
		if err := os.WriteFile(filepath.Join(dir, hashFile), []byte(hash+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return
}

func TestEnsure(t *testing.T) {
	week := 168 * time.Hour
	tests := []struct {
		name       string
		versions   map[string]string // version to hash
		maxAge     time.Duration
		hash       string
		wantBuilds int32
		wantReason string
		wantUsed   string // version, when no build
	}{
		{name: "no image", hash: "h1", maxAge: week, wantBuilds: 1, wantReason: "no image"},
		{name: "current image", versions: map[string]string{"20261025T120000Z": "h1"}, hash: "h1", maxAge: week, wantUsed: "20261025T120000Z"},
		{name: "expired", versions: map[string]string{"20261016T000000Z": "h1"}, hash: "h1", maxAge: week, wantBuilds: 1, wantReason: "expired 252h0m0s > 168h0m0s"},
		{name: "zero never expires", versions: map[string]string{"20200101T000000Z": "h1"}, hash: "h1", wantUsed: "20200101T000000Z"},
		{name: "other hash", versions: map[string]string{"20261025T120000Z": "h1"}, hash: "h2", maxAge: week, wantBuilds: 1, wantReason: "configuration changed"},
		{name: "no hash file", versions: map[string]string{"20261025T120000Z": ""}, hash: "h1", maxAge: week, wantBuilds: 1, wantReason: "configuration changed"},
		{
			name:     "older version with the hash is not searched",
			versions: map[string]string{"20261024T120000Z": "h2", "20261025T120000Z": "h1"},
			hash:     "h2", maxAge: week, wantBuilds: 1, wantReason: "configuration changed",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newEnsureEnv(t, tt.hash, tt.maxAge)
			for version, hash := range tt.versions {
				env.install(t, version, hash)
			}
			env.opts.Build = func(ctx context.Context, version string) (dir string, err error) {
				env.builds.Add(1)
				if version != "20261026T120000Z" {
					t.Errorf("version = %q, want the one of Now", version)
				}
				return env.install(t, version, tt.hash), nil
			}

			dir, err := Ensure(t.Context(), env.opts)
			if err != nil {
				t.Fatal(err)
			}
			if got := env.builds.Load(); got != tt.wantBuilds || env.reason != tt.wantReason {
				t.Errorf("builds = %d, reason = %q; want %d, %q", got, env.reason, tt.wantBuilds, tt.wantReason)
			}
			want := tt.wantUsed
			if tt.wantBuilds > 0 {
				want = "20261026T120000Z"
			}
			if filepath.Base(dir) != want {
				t.Errorf("dir = %q, want version %q", dir, want)
			}
		})
	}
}

func TestEnsureBuildFailure(t *testing.T) {
	env := newEnsureEnv(t, "h", 0)
	boom := errors.New("boom")
	env.opts.Build = func(ctx context.Context, version string) (dir string, err error) { return "", boom }

	if _, err := Ensure(t.Context(), env.opts); !errors.Is(err, boom) {
		t.Errorf("err = %v, want the build error", err)
	}
}

func TestEnsureSecondCallerWaitsForLockAndUsesItsBuild(t *testing.T) {
	env := newEnsureEnv(t, "h", 0)
	started, release := make(chan struct{}), make(chan struct{})
	env.opts.Build = func(ctx context.Context, version string) (dir string, err error) {
		env.builds.Add(1)
		close(started)
		<-release
		return env.install(t, version, "h"), nil
	}
	waiting := make(chan struct{})
	second := env.opts
	second.Say = func(msg, version string) {
		if version == "" {
			close(waiting)
		}
	}

	var wg sync.WaitGroup
	dirs := make([]string, 2)
	errs := make([]error, 2)
	wg.Go(func() { dirs[0], errs[0] = Ensure(t.Context(), env.opts) })
	<-started
	wg.Go(func() { dirs[1], errs[1] = Ensure(t.Context(), second) })
	<-waiting
	if got := env.builds.Load(); got != 1 {
		t.Errorf("builds while the lock is held = %d, want 1", got)
	}
	close(release)
	wg.Wait()

	for i := range errs {
		if errs[i] != nil {
			t.Fatalf("Ensure %d: %v", i, errs[i])
		}
	}
	if got := env.builds.Load(); got != 1 || dirs[0] != dirs[1] {
		t.Errorf("builds = %d, dirs = %q; want one build, one dir", got, dirs)
	}
}

func TestEnsureWaitStopsWithContext(t *testing.T) {
	env := newEnsureEnv(t, "h", 0)
	unlock, err := lockProject(t.Context(), filepath.Join(env.opts.Paths.StateDir, "p", "image.lock"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()

	if _, err = Ensure(ctx, env.opts); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want deadline exceeded", err)
	}
	if env.builds.Load() != 0 {
		t.Error("built without the lock")
	}
}

func TestConfigHash(t *testing.T) {
	base := func() config.Config {
		cfg := config.Config{Machine: config.Machines{Sandbox: config.Machine{Template: "debian13", MaxAge: "168h", MemoryMiB: 2048, CPUs: 2}}}
		cfg.Tool = map[string]config.Tool{"pg": {On: "sandbox", Build: []string{"apt install postgresql"}, Setup: []string{"migrate"}, Env: map[string]string{"A": "1"}, Prompt: "pg"}}
		return cfg
	}
	settings := []byte(`{"enabledPlugins": {"a@market": true}}`)
	tests := []struct {
		name         string
		change       func(cfg *config.Config, settings *[]byte)
		wantSameHash bool
	}{
		{name: "env", change: func(cfg *config.Config, _ *[]byte) {
			cfg.Tool["pg"] = config.Tool{On: "sandbox", Build: cfg.Tool["pg"].Build, Env: map[string]string{"A": "2"}}
		}, wantSameHash: true},
		{name: "prompt and setup", change: func(cfg *config.Config, _ *[]byte) {
			cfg.Tool["pg"] = config.Tool{On: "sandbox", Build: cfg.Tool["pg"].Build, Setup: []string{"x"}, Prompt: "other"}
		}, wantSameHash: true},
		{name: "memory and cpus", change: func(cfg *config.Config, _ *[]byte) {
			cfg.Machine.Sandbox.MemoryMiB, cfg.Machine.Sandbox.CPUs = 8192, 8
		}, wantSameHash: true},
		{name: "max_age", change: func(cfg *config.Config, _ *[]byte) { cfg.Machine.Sandbox.MaxAge = "0" }, wantSameHash: true},
		{name: "tool without build", change: func(cfg *config.Config, _ *[]byte) {
			cfg.Tool["env-only"] = config.Tool{On: "sandbox", Env: map[string]string{"B": "1"}}
		}, wantSameHash: true},
		{name: "tool build", change: func(cfg *config.Config, _ *[]byte) {
			cfg.Tool["pg"] = config.Tool{On: "sandbox", Build: []string{"apt install postgresql-17"}}
		}},
		{name: "machine build", change: func(cfg *config.Config, _ *[]byte) { cfg.Machine.Sandbox.Build = []string{"apt install go"} }},
		{name: "agent plugin", change: func(cfg *config.Config, _ *[]byte) { cfg.Agent.Plugins = []string{"b@market"} }},
		{name: "agent marketplace", change: func(cfg *config.Config, _ *[]byte) { cfg.Agent.Marketplaces = []string{"org/market"} }},
		{name: "settings plugin", change: func(_ *config.Config, settings *[]byte) {
			*settings = []byte(`{"enabledPlugins": {"c@market": true}}`)
		}},
	}
	want, err := ConfigHash(base(), settings)
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, changed := base(), settings
			tt.change(&cfg, &changed)
			got, err := ConfigHash(cfg, changed)
			if err != nil {
				t.Fatal(err)
			}
			if (got == want) != tt.wantSameHash {
				t.Errorf("same hash = %v, want %v", got == want, tt.wantSameHash)
			}
		})
	}
}

func TestConfigHashIgnoresOrderOfPluginsAndMarketplaces(t *testing.T) {
	cfg := config.Config{Machine: config.Machines{Sandbox: config.Machine{Template: "debian13"}}}
	a := []byte(`{"enabledPlugins": {"a@m": true, "b@m": true, "c@m": true},
		"extraKnownMarketplaces": {"m": {"source": {"source": "github", "repo": "o/m"}}, "n": {"source": {"source": "git", "url": "https://x/n.git"}}}}`)
	b := []byte(`{"extraKnownMarketplaces": {"n": {"source": {"source": "git", "url": "https://x/n.git"}}, "m": {"source": {"source": "github", "repo": "o/m"}}},
		"enabledPlugins": {"c@m": true, "a@m": true, "b@m": true}}`)
	for range 20 { // map iteration order is random
		hashA, errA := ConfigHash(cfg, a)
		hashB, errB := ConfigHash(cfg, b)
		if errA != nil || errB != nil || hashA != hashB {
			t.Fatalf("hashes %q, %q (errors %v, %v), want the same", hashA, hashB, errA, errB)
		}
	}
}

func TestConfigHashCoversTemplateAndFinalSteps(t *testing.T) {
	cfg := config.Config{Machine: config.Machines{Sandbox: config.Machine{Template: "debian13"}}}
	want, err := ConfigHash(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func(saved []string) { finalSteps = saved }(finalSteps)
	finalSteps = append(append([]string(nil), finalSteps...), "echo new")
	if got, _ := ConfigHash(cfg, nil); got == want {
		t.Error("hash ignores afk's final steps")
	}
	finalSteps = append([]string(nil), finalSteps[:len(finalSteps)-1]...)

	defer func(saved string) { metaData = saved }(metaData)
	metaData += "x"
	if got, _ := ConfigHash(cfg, nil); got == want {
		t.Error("hash ignores the Template's seed")
	}
}

func TestConfigHashRejectsUnknownTemplate(t *testing.T) {
	cfg := config.Config{Machine: config.Machines{Sandbox: config.Machine{Template: "ws2025"}}}
	if _, err := ConfigHash(cfg, nil); err == nil || !strings.Contains(err.Error(), "ws2025") {
		t.Errorf("err = %v, want unsupported template", err)
	}
}

func TestEnsureCleansOldVersionsAfterBuild(t *testing.T) {
	env := newEnsureEnv(t, "h", 0)
	for _, version := range []string{"20200101T000000Z", "20200102T000000Z", "20200103T000000Z"} {
		env.install(t, version, "old")
	}
	machineDir := env.opts.Paths.machineDir("p", "sandbox")
	overlay := writeOverlay(t, t.TempDir(), filepath.Join(machineDir, "20200101T000000Z", "disk.qcow2"))
	env.opts.Overlays = func() ([]string, error) { return []string{overlay}, nil }
	if err := os.MkdirAll(filepath.Join(machineDir, ".build-1"), 0o755); err != nil {
		t.Fatal(err)
	}

	if _, err := Ensure(t.Context(), env.opts); err != nil {
		t.Fatal(err)
	}
	// 0102 goes; 0101 has an overlay, 0103 is the previous newest.
	want := []string{".build-1", "20200101T000000Z", "20200103T000000Z", "20261026T120000Z"}
	if got := listDir(t, machineDir); !slices.Equal(got, want) {
		t.Errorf("left = %v, want %v", got, want)
	}
}

func TestEnsureCleanupErrorIsAWarning(t *testing.T) {
	env := newEnsureEnv(t, "h", 0) // no Overlays: cleanup fails
	var warnings []string
	env.opts.Warn = func(msg string) { warnings = append(warnings, msg) }

	dir, err := Ensure(t.Context(), env.opts)
	if err != nil || dir == "" {
		t.Fatalf("Ensure = %q, %v; want the build to succeed", dir, err)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "cleaning old image versions") || strings.Contains(warnings[0], "\n") {
		t.Errorf("warnings = %q", warnings)
	}
}

func TestBuildExclusiveHoldsLockAndCleans(t *testing.T) {
	paths := Paths{BaseDir: t.TempDir(), StateDir: t.TempDir()}
	var cfg config.Config
	cfg.Forge.Repo = "a/p"
	machineDir := paths.machineDir("a-p", "sandbox")
	for _, version := range []string{"20200101T000000Z", "20200102T000000Z", "20200103T000000Z"} {
		if err := os.MkdirAll(filepath.Join(machineDir, version), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	build := func(ctx context.Context, opts Options) (dir string, err error) {
		// The Project's lock is held: another locker has to wait.
		waitCtx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
		defer cancel()
		if _, lockErr := lockBuild(waitCtx, paths, "a-p", nil); lockErr == nil {
			t.Error("the build lock was free during the build")
		}
		dir = filepath.Join(machineDir, "20261026T120000Z")
		err = os.MkdirAll(dir, 0o755)
		return
	}
	overlays := func() ([]string, error) { return []string{}, nil }

	if _, err := buildExclusive(t.Context(), Options{Paths: paths, Config: cfg}, overlays, build); err != nil {
		t.Fatal(err)
	}
	want := []string{"20200103T000000Z", "20261026T120000Z"}
	if got := listDir(t, machineDir); !slices.Equal(got, want) {
		t.Errorf("left = %v, want %v", got, want)
	}

	var out strings.Builder
	if _, err := buildExclusive(t.Context(), Options{Paths: paths, Config: cfg, Out: &out}, nil, build); err != nil {
		t.Fatalf("a failed cleanup failed the build: %v", err)
	}
	if got := out.String(); strings.Count(got, "warning: cleaning old image versions") != 1 || strings.Count(got, "\n") != 1 {
		t.Errorf("out = %q, want one warning line", got)
	}
}
