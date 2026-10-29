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

func TestNewFactoryWindow(t *testing.T) {
	w, err := newFactoryWindow("/bin/afk", "k1", "o-r", "/home/me/r#1", []string{"12", "*"})
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(windowArgs("/bin/afk", w), " ")
	for _, want := range []string{
		"new-window -d -t afk: -n factory/k1 -c /home/me/r##1 /bin/afk factory 12 *",
		"set-option -w -t afk:=factory/k1 @afk-kind factory",
		"set-option -w -t afk:=factory/k1 @afk-project o-r",
		"set-option -w -t afk:=factory/k1 @afk-repo /home/me/r#1",
		"set-option -w -t afk:=factory/k1 pane-border-status top",
		"set-option -p -t afk:=factory/k1 @afk-main 1",
		"set-option -p -t afk:=factory/k1 remain-on-exit on",
		"select-pane -t afk:=factory/k1 -T afk factory",
		"split-window -hbd -l 32 -t afk:=factory/k1 /bin/afk sidebar --current factory/k1",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("args lack %q: %s", want, got)
		}
	}
	for _, bad := range [][3]string{{"rel", "o-r", "k"}, {"/r", "p;", "k"}, {"/r", "p", "k;"}, {"/r\n", "p", "k"}} {
		if _, err := newFactoryWindow("/bin/afk", bad[2], bad[1], bad[0], nil); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestShowArgs(t *testing.T) {
	create := func() []string { return []string{"new-window"} }
	got, err := showArgs("home\nchat-x\n", "chat-x", create)
	if want := []string{"select-window", "-t", "afk:=chat-x"}; err != nil || !slices.Equal(got, want) {
		t.Errorf("existing: %v, %v", got, err)
	}
	if got, err = showArgs("home\n", "chat-x", create); err != nil || !slices.Equal(got, []string{"new-window", ";", "select-window", "-t", "afk:=chat-x"}) {
		t.Errorf("missing: %v, %v", got, err)
	}
	if got, err = showArgs("home\n", "factory/a", nil); err == nil || !strings.Contains(err.Error(), "is gone") {
		t.Errorf("missing without create: %v, %v", got, err)
	}
}
