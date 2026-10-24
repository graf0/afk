package run

import (
	"bytes"
	"strings"
	"testing"

	"github.com/graf0/afk/config"
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
