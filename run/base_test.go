package run

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/graf0/afk/config"
)

func TestReadBase(t *testing.T) {
	root, user, base := userRepo(t)
	// The working tree and a newer, unpushed commit must not matter.
	sh(t, user, `printf '[forge]\nkind = "gitea"\nrepo = "a/b"\nurl = "https://g.example"\n[gate]\ncmd = "x"\n' > .afk.toml && git commit -qam local`)

	got, err := ReadBase(t.Context(), filepath.Join(user, "src"))
	if err != nil {
		t.Fatal(err)
	}
	if got.Repo != user || got.Branch != "main" || got.Commit != base {
		t.Errorf("ReadBase = %+v, want repo %s, main at %s", got, user, base)
	}
	if got.Config.Forge.Kind != "github" || !slices.Contains(got.Config.Protect.Paths, "secrets/key") {
		t.Errorf("config = %+v, want the one from origin/main", got.Config)
	}
	if got.Ident != (Ident{Name: "t", Email: "t@example.com"}) {
		t.Errorf("ident = %+v, want the user's git identity", got.Ident)
	}

	t.Run("fetches origin and follows origin/HEAD", func(t *testing.T) {
		sh(t, root, `
git clone -q origin.git other && cd other
git checkout -q -b trunk && echo t > t && git add t && git commit -qm trunk
git push -q origin trunk`)
		sh(t, user, "git remote set-head origin trunk 2>/dev/null || git symbolic-ref refs/remotes/origin/HEAD refs/remotes/origin/trunk")
		got, err := ReadBase(t.Context(), user)
		if err != nil {
			t.Fatal(err)
		}
		want := sh(t, filepath.Join(root, "other"), "git rev-parse HEAD")
		if got.Branch != "trunk" || got.Commit != want {
			t.Errorf("ReadBase = %+v, want trunk at %s", got, want)
		}
	})

	t.Run("plugins from settings.json on the base", func(t *testing.T) {
		if len(got.Plugins) != 0 {
			t.Errorf("plugins = %q, want none for empty settings", got.Plugins)
		}
		sh(t, root, `cd other && echo '{"enabledPlugins": {"a@m": true, "b@m": false}}' > settings.json && mkdir -p .claude && mv settings.json .claude/ && git add -A && git commit -qm plugins && git push -q origin trunk`)
		got, err := ReadBase(t.Context(), user)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(got.Plugins, []string{"a@m"}) {
			t.Errorf("plugins = %q, want [a@m]", got.Plugins)
		}
		sh(t, root, "cd other && git rm -q .claude/settings.json && git commit -qm rm && git push -q origin trunk")
		if got, err = ReadBase(t.Context(), user); err != nil || len(got.Plugins) != 0 {
			t.Errorf("without settings.json: plugins = %q, err = %v", got.Plugins, err)
		}
	})

	t.Run("no .afk.toml on the base", func(t *testing.T) {
		sh(t, root, "cd other && git rm -q .afk.toml && git commit -qm rm && git push -q origin trunk")
		if _, err := ReadBase(t.Context(), user); err == nil || !strings.Contains(err.Error(), ".afk.toml from origin/trunk") {
			t.Errorf("err = %v", err)
		}
	})
}

func TestReadBaseLocalDoesNotFetch(t *testing.T) {
	root, user, base := userRepo(t)
	sh(t, root, `
git clone -q origin.git other && cd other
echo n > n && git add n && git commit -qm next && git push -q origin main`)
	got, err := ReadBaseLocal(t.Context(), user)
	if err != nil || got.Commit != base {
		t.Errorf("ReadBaseLocal = %q, %v; want the origin/main it already has, %s", got.Commit, err, base)
	}
	sh(t, user, "git remote set-url origin /no/such/origin") // not even reachable
	if _, err = ReadBaseLocal(t.Context(), user); err != nil {
		t.Errorf("without the remote: %v", err)
	}
	if _, err = ReadBase(t.Context(), user); err == nil {
		t.Error("ReadBase of an unreachable origin: want an error")
	}
}

// TestReadBaseIgnoresWorkingTree: the commands that start something take
// .afk.toml and settings.json only from ReadBase.
func TestReadBaseIgnoresWorkingTree(t *testing.T) {
	_, user, _ := userRepo(t)
	sh(t, user, `printf '[forge]\nkind = "gitea"\nrepo = "x/y"\nurl = "https://g.example"\n[gate]\ncmd = "false"\n' > .afk.toml
echo '{"enabledPlugins": {"local@m": true}}' > .claude/settings.json`)
	got, err := ReadBase(t.Context(), user)
	if err != nil {
		t.Fatal(err)
	}
	if got.Config.Forge.Kind != "github" || got.Config.Gate.Cmd != "true" {
		t.Errorf("config = %+v, want the committed one", got.Config)
	}
	if strings.Contains(string(got.Settings), "local@m") || len(got.Plugins) != 0 {
		t.Errorf("settings = %s, plugins = %q, want the committed ones", got.Settings, got.Plugins)
	}
}

func TestReadBaseErrors(t *testing.T) {
	root, user, _ := userRepo(t)
	sh(t, root, "git -C origin.git branch -m main other")
	sh(t, user, "git update-ref -d refs/remotes/origin/main")
	_, err := ReadBase(t.Context(), user)
	if err == nil || !strings.Contains(err.Error(), "git remote set-head origin --auto") {
		t.Errorf("without origin/HEAD and origin/main: err = %v, want a hint to set origin/HEAD", err)
	}
}

// TestResultChangingStackFilesIsRejected: what the host builds and runs from
// the base branch cannot be changed by a Run or a Chat: the default [protect]
// has the compose files, .env and .afk/; the Dockerfiles named by the compose
// files come from the Stack (run.protected), here a service's Dockerfile.
func TestResultChangingStackFilesIsRejected(t *testing.T) {
	root, user, _ := userRepo(t)
	sh(t, user, `mkdir -p .afk docker
echo 'services: {}' > compose.yml
echo 'services: {}' > compose.afk.yml
echo A=1 > .env
echo 'FROM scratch' > .afk/Dockerfile
echo 'FROM scratch' > docker/Dockerfile.web
git add -A && git commit -qm stack && git push -q origin main && git fetch -q origin`)
	base, err := ReadBase(t.Context(), user)
	if err != nil {
		t.Fatal(err)
	}
	r := run{Options: Options{Base: base}, stackFiles: &stackFiles{paths: []string{"docker/Dockerfile.web"}}}
	for name, script := range map[string]string{
		"compose.yml":           `echo '# x' >> compose.yml`,
		"compose.afk.yml":       `echo '# x' >> compose.afk.yml`,
		".env":                  `echo B=2 >> .env`,
		"Dockerfile of afk":     `echo 'RUN evil' >> .afk/Dockerfile`,
		"new file in .afk":      `echo x > .afk/entrypoint.sh`,
		"Dockerfile of service": `echo 'RUN evil' >> docker/Dockerfile.web`,
	} {
		t.Run(name, func(t *testing.T) {
			q, err := OpenQuarantine(t.Context(), filepath.Join(t.TempDir(), "quarantine.git"))
			if err != nil {
				t.Fatal(err)
			}
			if err = q.FetchBase(t.Context(), user, base.Commit, "ticket-1"); err != nil {
				t.Fatal(err)
			}
			bundle := agentResult(t, root, base.Commit, script+" && git add -A && git commit -qm x")
			head, err := q.Import(t.Context(), bundle, "ticket-1")
			if err != nil {
				t.Fatal(err)
			}
			if _, err = q.Validate(t.Context(), base.Commit, head, r.protected()); err == nil {
				t.Error("Validate accepted a change of a protected Stack file")
			}
		})
	}
}

func TestProtectedAddsTheStacksDockerfiles(t *testing.T) {
	r := run{Options: Options{Base: Base{Config: config.Config{Protect: config.Protect{Paths: []string{".afk.toml"}}}}}, stackFiles: &stackFiles{}}
	if got := r.protected(); !slices.Equal(got, []string{".afk.toml"}) {
		t.Errorf("before Up: %v", got)
	}
	r.stackFiles.paths = []string{"docker/Dockerfile"}
	if got := r.protected(); !slices.Equal(got, []string{".afk.toml", "docker/Dockerfile"}) {
		t.Errorf("after Up: %v", got)
	}
}
