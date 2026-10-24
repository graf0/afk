package run

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const testConfig = "[forge]\nkind = \"github\"\nrepo = \"o/r\"\n[protect]\npaths = [\"secrets/key\"]\n"

// isolateGit makes git ignore the user's and system config and sets an identity.
func isolateGit(t *testing.T) {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	for _, name := range []string{"GIT_AUTHOR_NAME", "GIT_COMMITTER_NAME"} {
		t.Setenv(name, "t")
	}
	for _, name := range []string{"GIT_AUTHOR_EMAIL", "GIT_COMMITTER_EMAIL"} {
		t.Setenv(name, "t@example.com")
	}
}

// sh runs script with bash in dir.
func sh(t *testing.T, dir, script string) (out string) {
	t.Helper()
	cmd := exec.Command("bash", "-euc", script)
	cmd.Dir = dir
	data, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s: %v\n%s", script, err, data)
	}
	return strings.TrimSpace(string(data))
}

// userRepo creates origin.git and a clone "user" in a temp dir, with the
// base commit pushed to origin/main.
func userRepo(t *testing.T) (root, user, base string) {
	t.Helper()
	isolateGit(t)
	root = t.TempDir()
	sh(t, root, `
git init -q --bare -b main origin.git
git init -q -b main user
cd user
mkdir -p .claude .github/workflows src secrets
echo '{}' > .claude/settings.json
echo 'on: push' > .github/workflows/ci.yml
echo 'package a' > src/a.go
echo k > secrets/key
cat > .afk.toml <<'EOF'
`+testConfig+`EOF
git add -A
git commit -qm base
git remote add origin ../origin.git
git push -q origin main
git fetch -q origin`)
	user = filepath.Join(root, "user")
	base = sh(t, user, "git rev-parse HEAD")
	return
}

// agentResult clones origin into a fresh "agent" dir, runs script there on
// branch ticket-1 and returns a bundle of base..HEAD (empty path when there
// are no commits).
func agentResult(t *testing.T, root, base, script string) (bundle string) {
	t.Helper()
	dir := filepath.Join(root, "agent")
	_ = os.RemoveAll(dir)
	sh(t, root, "git clone -q origin.git agent && cd agent && git checkout -q -b ticket-1")
	sh(t, dir, script)
	if sh(t, dir, "git rev-list --count "+base+"..HEAD") == "0" {
		return
	}
	bundle = filepath.Join(root, "result.bundle")
	sh(t, dir, "git bundle create -q "+bundle+" "+base+"..HEAD")
	return
}

func TestQuarantineValidate(t *testing.T) {
	root, user, base := userRepo(t)
	protected := []string{".afk.toml", ".github/", ".gitea/", "**/.claude/", "**/.mcp.json", "secrets/key"}
	for name, tc := range map[string]struct {
		script string
		want   []string // protected paths in the error; nil: valid
	}{
		"ordinary change":        {`echo 'package b' >> src/a.go && git commit -qam x`, nil},
		"two commits":            {`echo 1 > new && git add new && git commit -qm 1 && echo 2 > new && git commit -qam 2`, nil},
		".afk.toml":              {`echo '# x' >> .afk.toml && git commit -qam x`, []string{".afk.toml"}},
		"file in .claude/":       {`echo '{"x":1}' > .claude/settings.json && git commit -qam x`, []string{".claude/settings.json"}},
		"hook added":             {`mkdir .claude/hooks && echo 'rm -rf ~' > .claude/hooks/pre.sh && git add -A && git commit -qm x`, []string{".claude/hooks/pre.sh"}},
		"rename out":             {`git mv .github/workflows/ci.yml ci.yml && git commit -qm x`, []string{".github/workflows/ci.yml"}},
		"rename in":              {`mkdir -p .gitea && git mv src/a.go .gitea/a.go && git commit -qm x`, []string{".gitea/a.go"}},
		"mode change":            {`chmod +x .claude/settings.json && git commit -qam x`, []string{".claude/settings.json"}},
		"dir replaced by link":   {`git rm -rq .claude && ln -s /etc .claude && git add .claude && git commit -qm x`, []string{".claude", ".claude/settings.json"}},
		"case variant":           {`mkdir .CLAUDE && echo x > .CLAUDE/hook && git add -A && git commit -qm x`, []string{".CLAUDE/hook"}},
		"changed and reverted":   {`echo '# x' >> .afk.toml && git commit -qam x && git revert --no-edit HEAD >/dev/null`, []string{".afk.toml"}},
		"parent of user entry":   {`git rm -rq secrets && echo x > secrets && git add secrets && git commit -qm x`, []string{"secrets", "secrets/key"}},
		"hostile path name":      {`mkdir .claude/x && touch $'.claude/x/\e[2J\u202e' && git add -A && git commit -qm x`, []string{".claude/x/[2J"}},
		"nested .claude":         {`mkdir -p sub/.claude && echo '{}' > sub/.claude/settings.json && git add -A && git commit -qm x`, []string{"sub/.claude/settings.json"}},
		"deep .claude hook":      {`mkdir -p a/b/.Claude/hooks && echo x > a/b/.Claude/hooks/x && git add -A && git commit -qm x`, []string{"a/b/.Claude/hooks/x"}},
		".mcp.json":              {`echo '{}' > .mcp.json && mkdir -p sub && echo '{}' > sub/.mcp.json && git add -A && git commit -qm x`, []string{".mcp.json", "sub/.mcp.json"}},
		"allowed agent files":    {`echo x > CLAUDE.md && echo '* text' > .gitattributes && touch .gitmodules && mkdir d && echo x > d/claude && git add -A && git commit -qm x`, nil},
		"merge touching .claude": {`git checkout -q -b side && echo y > .claude/x && git add -A && git commit -qm side && git checkout -q ticket-1 && echo z > z && git add z && git commit -qm z && git merge -q --no-edit side`, []string{".claude/x"}},
	} {
		t.Run(name, func(t *testing.T) {
			q, err := OpenQuarantine(t.Context(), filepath.Join(t.TempDir(), "quarantine.git"))
			if err != nil {
				t.Fatal(err)
			}
			if err = q.FetchBase(t.Context(), user, base, "ticket-1"); err != nil {
				t.Fatal(err)
			}
			bundle := agentResult(t, root, base, tc.script)
			head, err := q.Import(t.Context(), bundle, "ticket-1")
			if err != nil {
				t.Fatal(err)
			}
			commits, err := q.Validate(t.Context(), base, head, protected)
			if tc.want == nil {
				if err != nil || commits == 0 {
					t.Errorf("Validate = %d, %v; want valid", commits, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("Validate = %d, nil; want rejection of %v", commits, tc.want)
			}
			if strings.ContainsAny(err.Error(), "\x1b\u202e") {
				t.Errorf("err = %q, contains terminal controls", err)
			}
			for _, path := range tc.want {
				if !strings.Contains(err.Error(), path) {
					t.Errorf("err = %v, want path %s", err, path)
				}
			}
		})
	}
}

func TestQuarantineValidateHistory(t *testing.T) {
	root, user, base := userRepo(t)
	q, err := OpenQuarantine(t.Context(), filepath.Join(t.TempDir(), "quarantine.git"))
	if err != nil {
		t.Fatal(err)
	}
	if err = q.FetchBase(t.Context(), user, base, "ticket-1"); err != nil {
		t.Fatal(err)
	}
	if _, err = q.Validate(t.Context(), base, base, nil); err == nil || !strings.Contains(err.Error(), "no commits") {
		t.Errorf("no commits: err = %v", err)
	}

	// Unrelated history: an orphan branch with the same files.
	agentResult(t, root, base, "true")
	dir := filepath.Join(root, "agent")
	sh(t, dir, "git checkout -q --orphan other && git commit -qm orphan")
	bundle := filepath.Join(root, "orphan.bundle")
	sh(t, dir, "git bundle create -q "+bundle+" HEAD")
	head, err := q.Import(t.Context(), bundle, "ticket-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = q.Validate(t.Context(), base, head, nil); err == nil || !strings.Contains(err.Error(), "does not contain the base") {
		t.Errorf("unrelated history: err = %v", err)
	}
}

func TestQuarantineSetup(t *testing.T) {
	root, user, base := userRepo(t)
	dir := filepath.Join(t.TempDir(), "p", "quarantine.git")
	q, err := OpenQuarantine(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(dir, "hooks")); !os.IsNotExist(err) {
		t.Errorf("hooks dir: %v, want none", err)
	}
	if _, err = OpenQuarantine(t.Context(), dir); err != nil {
		t.Errorf("reopening: %v", err)
	}
	if err = q.FetchBase(t.Context(), user, base, "ticket-1"); err != nil {
		t.Fatal(err)
	}

	// The base bundle clones into what the guest gets.
	bundle := filepath.Join(root, "base.bundle")
	if err = q.Bundle(t.Context(), "ticket-1", bundle); err != nil {
		t.Fatal(err)
	}
	got := sh(t, root, "git init -q -b ticket-1 guest && cd guest && git fetch -q "+bundle+" +refs/afk/base/ticket-1:refs/remotes/origin/main && git checkout -q -B ticket-1 origin/main && git rev-parse HEAD")
	if got != base {
		t.Errorf("guest HEAD = %s, want %s", got, base)
	}

	if err = q.CheckNew(t.Context(), "ticket-1"); err != nil {
		t.Errorf("CheckNew before Accept: %v", err)
	}
	if err = q.Accept(t.Context(), "ticket-1", base); err != nil {
		t.Fatal(err)
	}
	if err = q.CheckNew(t.Context(), "ticket-1"); err == nil || !strings.Contains(err.Error(), "already has a result") {
		t.Errorf("CheckNew after Accept: err = %v", err)
	}
	if err = q.Accept(t.Context(), "ticket-1", base); err == nil {
		t.Error("second Accept overwrote the result")
	}
	refs := strings.Fields(sh(t, root, "git --git-dir="+dir+" for-each-ref --format='%(refname)'"))
	if !slices.Equal(refs, []string{"refs/afk/base/ticket-1", "refs/heads/ticket-1"}) {
		t.Errorf("refs = %v", refs)
	}
}

func TestProtectedChanges(t *testing.T) {
	protected := []string{".afk.toml", ".github/", "**/.claude/", "**/.mcp.json", "secrets/key"}
	changed := []string{
		"src/a.go", ".afk.toml", "x/.afk.toml", ".github/ci.yml", "x/.github/ci.yml",
		"x/y/.claude", "x/.claude/settings.json", ".mcp.json", "a/.MCP.json", "a/.mcp.json.bak",
		"secrets", "x/secrets/key", "CLAUDE.md", "my.claude/x",
	}
	want := []string{".afk.toml", ".github/ci.yml", "x/y/.claude", "x/.claude/settings.json", ".mcp.json", "a/.MCP.json", "secrets"}
	if got := protectedChanges(changed, protected); !slices.Equal(got, want) {
		t.Errorf("protectedChanges = %q, want %q", got, want)
	}
}
