package chat

import (
	"strings"
	"testing"
	"time"

	"github.com/graf0/afk/machine"
)

func TestList(t *testing.T) {
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	machines := []machine.Machine{
		{Kind: machine.KindRun, ID: "1-ab", Running: true, Created: now},
		{Kind: machine.KindChat, ID: "plan", Running: true, Created: now.Add(-5 * time.Minute)},
		{Kind: machine.KindChat, ID: "long-name", Created: now.Add(-30 * time.Hour)},
		{Kind: machine.KindChat, ID: "old", Created: now.Add(-72 * time.Hour)},
		{Kind: machine.KindChat, ID: "nodate"},
	}
	var out strings.Builder
	if err := List(&out, machines, now); err != nil {
		t.Fatal(err)
	}
	want := "plan       running   5m\n" +
		"long-name  shut off  30h\n" +
		"old        shut off  3d\n" +
		"nodate     shut off  ?\n"
	if out.String() != want {
		t.Errorf("list =\n%s\nwant\n%s", out.String(), want)
	}
}
