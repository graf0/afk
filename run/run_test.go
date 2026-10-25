package run

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
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
	err := Execute(t.Context(), Options{
		Base:   Base{Config: config.Config{Labels: config.Labels{Spec: "spec"}}},
		Ticket: forge.Ticket{Number: 9, Labels: []string{"spec"}},
	})
	if err == nil || !strings.Contains(err.Error(), "is a Spec") {
		t.Errorf("err = %v", err)
	}
}

func TestDenialLogIsCreatedByTheFirstDenial(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ticket-1-egress.txt")
	l := &denialLog{path: path}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("file exists without a denial: %v", err)
	}
	for _, line := range []string{"denied a\n", "denied b\n"} {
		if _, err := l.Write([]byte(line)); err != nil {
			t.Fatal(err)
		}
	}
	denied, err := l.Close()
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(path); string(got) != "denied a\ndenied b\n" || denied != 2 {
		t.Errorf("file = %q, denied = %d", got, denied)
	}
}

func TestCloseLogsSummarizesDenials(t *testing.T) {
	dir := t.TempDir()
	for name, denials := range map[string][]string{"none": nil, "two": {"denied a\n", "denied b\n"}} {
		t.Run(name, func(t *testing.T) {
			var out strings.Builder
			path := filepath.Join(dir, name+".jsonl")
			file, err := os.Create(path)
			if err != nil {
				t.Fatal(err)
			}
			r := run{
				Options: Options{Out: &out},
				log:     &rotatingLog{path: path, file: file},
				denials: &denialLog{path: filepath.Join(dir, name+"-egress.txt")},
			}
			for _, line := range denials {
				_, _ = r.denials.Write([]byte(line))
			}
			r.closeLogs()
			want := ""
			if denials != nil {
				want = "= egress: 2 denied, see " + r.denials.path + "\n"
			}
			if out.String() != want {
				t.Errorf("out = %q, want %q", out.String(), want)
			}
			if _, err := os.Stat(r.denials.path); (err == nil) != (denials != nil) {
				t.Errorf("denials file stat: %v", err)
			}
		})
	}
}
