package chat

import (
	"fmt"
	"io"
	"os"
	"text/tabwriter"
	"time"

	"github.com/graf0/afk/stack"
)

// List prints the Chats of the project, one per line: name, state (running
// when a process serves its channel, else stopped) and how long ago it was
// last started (at now). The state directories are the only record of Chats.
func List(w io.Writer, stateDir, project string, now time.Time) (err error) {
	names, err := stack.List(stateDir, project, stack.Chat)
	if err != nil {
		return
	}
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	for _, name := range names {
		dir := stack.Dir(stateDir, project, stack.Chat, name)
		state := "stopped"
		if running(dir) {
			state = "running"
		}
		// The lock file is touched by every start; a Chat never started has the directory's.
		info, statErr := os.Stat(lockPath(dir))
		if statErr != nil {
			info, statErr = os.Stat(dir)
		}
		if _, err = fmt.Fprintf(tw, "%s\t%s\t%s\n", name, state, formatAge(now.Sub(info.ModTime()), statErr != nil)); err != nil {
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
