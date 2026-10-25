package run

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestReadBase(t *testing.T) {
	root, user, base := userRepo(t)
	// The working tree and a newer, unpushed commit must not matter.
	sh(t, user, `printf '[forge]\nkind = "gitea"\n' > .afk.toml && git commit -qam local`)

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
		if _, err := ReadBase(t.Context(), user); err == nil || !strings.Contains(err.Error(), ".afk.toml") {
			t.Errorf("err = %v", err)
		}
	})
}
