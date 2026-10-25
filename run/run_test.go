package run

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/graf0/afk/config"
	"github.com/graf0/afk/forge"
)

func TestTailBuffer(t *testing.T) {
	var b tailBuffer
	_, _ = b.Write([]byte(strings.Repeat("a", maxStderr)))
	_, _ = b.Write([]byte("end"))
	if got := b.String(); len(got) != maxStderr || !strings.HasSuffix(got, "aend") {
		t.Errorf("len %d, suffix %q", len(got), got[len(got)-4:])
	}
}

func TestLimitWriter(t *testing.T) {
	var out bytes.Buffer
	w := &limitWriter{w: &out, left: 5}
	if n, err := w.Write([]byte("abc")); n != 3 || err != nil {
		t.Fatalf("Write = %d, %v", n, err)
	}
	if _, err := w.Write([]byte("def")); err == nil {
		t.Error("write over the limit: want error")
	}
	if out.String() != "abc" {
		t.Errorf("out = %q", out.String())
	}
}

func TestRotatingLog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.jsonl")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	l := &rotatingLog{path: path, max: 8, file: file}
	for _, line := range []string{"1111\n", "222\n", "33\n", "4\n"} {
		if _, err = l.Write([]byte(line)); err != nil {
			t.Fatal(err)
		}
	}
	if err = l.Close(); err != nil {
		t.Fatal(err)
	}
	// "1111\n" is dropped: lines stay whole, at most two files remain.
	for name, want := range map[string]string{path + ".1": "222\n33\n", path: "4\n"} {
		if got, _ := os.ReadFile(name); string(got) != want {
			t.Errorf("%s = %q, want %q", filepath.Base(name), got, want)
		}
	}
}

func TestRunPrompt(t *testing.T) {
	r := run{
		Options: Options{Base: Base{Branch: "trunk", Config: config.Config{Protect: config.Protect{Paths: []string{".afk.toml"}}}}},
		branch:  "ticket-7",
	}
	prompt := r.prompt()
	for _, want := range []string{guestTicket, guestRepo, "`ticket-7`", "`origin/trunk`", "`.afk.toml`"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt lacks %q", want)
		}
	}
}

func TestExecuteRefusesSpec(t *testing.T) {
	err := Execute(t.Context(), Options{Ticket: forge.Ticket{Number: 9, SubIssues: 2}})
	if err == nil || !strings.Contains(err.Error(), "is a Spec") {
		t.Errorf("err = %v", err)
	}
}

func TestTaskSpec(t *testing.T) {
	r := run{branch: "ticket-9"}
	if got := r.task().SpecPath; got != "" {
		t.Errorf("SpecPath without a Spec = %q", got)
	}
	r.spec = &forge.Ticket{Number: 9}
	if got := r.task().SpecPath; got != guestSpec {
		t.Errorf("SpecPath = %q, want %q", got, guestSpec)
	}
}

func TestTaskPlugins(t *testing.T) {
	r := run{Options: Options{Base: Base{Plugins: []string{"b@y", "mattpocock-skills@claude-plugins-official"}}}}
	if got := r.task().Plugins; !slices.Equal(got, []string{"b", "mattpocock-skills"}) {
		t.Errorf("task plugins = %q", got)
	}
}
