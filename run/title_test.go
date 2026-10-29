package run

import (
	"bytes"
	"strings"
	"testing"
	"time"
	"unicode"
)

func TestFormatTitle(t *testing.T) {
	got := formatTitle("#7 evil\x1b]0;x\x07\n\u202etitle\x00", false, 95*time.Second, 4*time.Second)
	for _, r := range got {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			t.Fatalf("control character %q in %q", r, got)
		}
	}
	if want := "#7 evil]0;xtitle · run 1m35s · zdarzenie 4s temu"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if got := formatTitle("", false, 0, 0); got != "afk: czekam" {
		t.Errorf("waiting: %q", got)
	}
	if got := formatTitle("#1 x", true, 0, 0); got != "afk: koniec" {
		t.Errorf("done: %q", got)
	}
}

func TestTitleWritesOSC2AndCountsEvents(t *testing.T) {
	var tty bytes.Buffer
	now := time.Unix(1000, 0)
	title := NewTitle(&tty)
	title.now = func() time.Time { return now }
	title.begin("#5 x")
	now = now.Add(30 * time.Second)
	if _, err := title.Write([]byte("hi\n")); err != nil {
		t.Fatal(err)
	}
	now = now.Add(7 * time.Second)
	tty.Reset()
	title.refresh()
	if want := "\x1b]2;#5 x · run 37s · zdarzenie 7s temu\a"; tty.String() != want {
		t.Errorf("got %q, want %q", tty.String(), want)
	}
	title.end()
	tty.Reset()
	title.refresh()
	if !strings.Contains(tty.String(), "czekam") {
		t.Errorf("got %q", tty.String())
	}
}
