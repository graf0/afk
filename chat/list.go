package chat

import (
	"fmt"
	"io"
	"text/tabwriter"
	"time"

	"github.com/graf0/afk/machine"
)

// List prints the Chats among machines, one per line: name, state of the VM
// (running / shut off) and age at now.
func List(w io.Writer, machines []machine.Machine, now time.Time) (err error) {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	for _, m := range machines {
		if m.Kind != machine.KindChat {
			continue
		}
		state := "shut off"
		if m.Running {
			state = "running"
		}
		if _, err = fmt.Fprintf(tw, "%s\t%s\t%s\n", m.ID, state, formatAge(now.Sub(m.Created), m.Created.IsZero())); err != nil {
			return
		}
	}
	err = tw.Flush()
	return
}

// formatAge is d in its largest unit: minutes, hours or days; "?" when unknown.
func formatAge(d time.Duration, unknown bool) (age string) {
	switch {
	case unknown:
		return "?"
	case d < time.Hour:
		return fmt.Sprintf("%dm", max(int(d/time.Minute), 0))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d/time.Hour))
	default:
		return fmt.Sprintf("%dd", int(d/(24*time.Hour)))
	}
}
