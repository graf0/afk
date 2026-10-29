package tui

import (
	"slices"
	"strings"
	"testing"

	"github.com/graf0/afk/machine"
)

func TestOpenArgs(t *testing.T) {
	chat := strings.Join(openArgs("/bin/afk", Entry{Name: "chat-x", ID: "x", Project: "p", Kind: machine.KindChat, Repo: "/r"}), " ")
	for _, want := range []string{"new-window -d -t afk: -n chat-x -c /r /bin/afk chat attach x ;", "set-option -w -t afk:=chat-x @afk-kind chat ; set-option -w -t afk:=chat-x @afk-project p ;", "split-window -hbd -l 32 -t afk:=chat-x /bin/afk sidebar --current chat-x"} {
		if !strings.Contains(chat, want) {
			t.Errorf("chat args lack %q: %s", want, chat)
		}
	}
	runArgs := openArgs("/bin/afk", Entry{Name: "run-1-a", ID: "1-a", Kind: machine.KindRun})
	if slices.Contains(runArgs, "chat") {
		t.Errorf("run args: %v", runArgs)
	}
}

func TestOpenArgsUnsafeRepo(t *testing.T) {
	for _, repo := range []string{"#(touch /tmp/x)", "/r/../x", "/r;", "/r\n", "rel"} {
		args := openArgs("/bin/afk", Entry{Name: "chat-x", ID: "x", Kind: machine.KindChat, Repo: repo})
		if slices.Contains(args, "-c") {
			t.Errorf("repo %q passed to tmux: %v", repo, args)
		}
	}
	if dir, ok := safeDir("/r/a#b"); !ok || dir != "/r/a##b" {
		t.Errorf("safeDir = %q, %v", dir, ok)
	}
}

func TestOpenArgsUnsafeProject(t *testing.T) {
	args := strings.Join(openArgs("/bin/afk", Entry{Name: "chat-x", ID: "x", Project: "p;", Kind: machine.KindChat}), " ")
	if strings.Contains(args, "@afk-project") {
		t.Errorf("project with a trailing ; passed: %s", args)
	}
}
