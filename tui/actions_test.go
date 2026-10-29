package tui

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/graf0/afk/machine"
)

func TestArgs(t *testing.T) {
	chat := Entry{Name: "afk-chat-x", Kind: machine.KindChat, ID: "x"}
	run := Entry{Name: "afk-run-1-ab", Kind: machine.KindRun, ID: "1-ab"}
	if args, err := exportArgs(chat); err != nil || !slices.Equal(args, []string{"chat", "export", "x"}) {
		t.Errorf("exportArgs = %v, %v", args, err)
	}
	if _, err := exportArgs(run); err == nil {
		t.Error("exportArgs of a Run succeeded")
	}
	if args, _ := rmArgs(chat, true); !slices.Equal(args, []string{"chat", "rm", "--force", "x"}) {
		t.Errorf("rmArgs = %v", args)
	}
	if _, err := rmArgs(Entry{Kind: machine.KindChat, ID: "-f"}, false); err == nil {
		t.Error("rmArgs accepted an id that is a flag")
	}
	if args, err := killArgs(run); err != nil || !slices.Equal(args, []string{"vm", "kill", "afk-run-1-ab"}) {
		t.Errorf("killArgs = %v, %v", args, err)
	}
	if args, err := killArgs(Entry{Name: "x;", Kind: machine.KindRun, ID: "1-ab"}); err == nil || args != nil {
		t.Error("killArgs accepted an unsafe name")
	}
}

func TestProjectFor(t *testing.T) {
	root := t.TempDir()
	repoA, repoB := filepath.Join(root, "a"), filepath.Join(root, "b")
	for _, dir := range []string{filepath.Join(repoA, "sub"), repoB, filepath.Join(root, "none")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, repo := range []string{repoA, repoB} {
		if err := os.WriteFile(filepath.Join(repo, ".afk.toml"), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for name, tc := range map[string]struct {
		selected *Entry
		cwd      string
		want     string
	}{
		"selection wins":      {&Entry{Repo: repoB}, filepath.Join(repoA, "sub"), repoB},
		"cwd in a subdir":     {nil, filepath.Join(repoA, "sub"), repoA},
		"bad repo falls back": {&Entry{Repo: "rel"}, repoA, repoA},
		"no project":          {&Entry{}, filepath.Join(root, "none"), ""},
	} {
		got, err := projectFor(tc.selected, tc.cwd)
		if got != tc.want || (err != nil) != (tc.want == "") {
			t.Errorf("%s: projectFor = %q, %v; want %q", name, got, err, tc.want)
		}
	}
}

func TestWindowArgsForShellAndNewChat(t *testing.T) {
	got := strings.Join(windowArgs("/bin/afk", window{Name: "shell/afk-chat-x", Current: "afk-chat-x", Dir: "/r#", Cmd: keepOnFailure("/bin/afk", "chat", "shell", "x")}), " ")
	for _, want := range []string{"new-window -d -t afk: -n shell/afk-chat-x -c /r## sh -c", "/bin/afk chat shell x ; split-window -hbd -l 32 -t afk:=shell/afk-chat-x /bin/afk sidebar --current afk-chat-x"} {
		if !strings.Contains(got, want) {
			t.Errorf("args lack %q: %s", want, got)
		}
	}
	if strings.Contains(got, "@afk-kind") {
		t.Errorf("shell window has a kind: %s", got)
	}
}

func TestFactorySelector(t *testing.T) {
	for in, want := range map[string][]string{"": {"*"}, " * ": {"*"}, "#12 15": {"12", "15"}, "007": {"7"}} {
		if got, err := factorySelector(in); err != nil || !slices.Equal(got, want) {
			t.Errorf("factorySelector(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, in := range []string{"x", "* 1", "0", "-1", "#", "1;", "$(id)", "--force", "1 #"} {
		if got, err := factorySelector(in); err == nil || got != nil {
			t.Errorf("factorySelector(%q) = %v, %v; want an error", in, got, err)
		}
	}
}
