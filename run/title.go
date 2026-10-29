package run

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
	"unicode"
)

// titleInterval is how often the Factory refreshes the terminal title.
const titleInterval = 10 * time.Second

// Title keeps the terminal (tmux pane) title of an `afk factory` on a TTY:
// the ticket, the age of its Run and the time since the last output, set
// with OSC 2 (docs/workflow.md, TUI). It is also the Factory's output
// stream: every write counts as an event, and writes are serialized with
// the title ones. A nil *Title (no TTY) sets no title.
type Title struct {
	now func() time.Time

	mu        sync.Mutex
	tty       io.Writer
	ticket    string // "" while waiting
	done      bool
	runStart  time.Time
	lastEvent time.Time
}

// NewTitle is a Title writing to tty, which must be a terminal.
func NewTitle(tty io.Writer) (t *Title) {
	return &Title{tty: tty, now: time.Now}
}

// Write passes p to the terminal and counts it as an event.
func (t *Title) Write(p []byte) (n int, err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.lastEvent = t.now()
	return t.tty.Write(p)
}

// Start sets the title now and every titleInterval, until the returned
// stop is called: then it says the Factory is done.
func (t *Title) Start(ctx context.Context) (stop func()) {
	if t == nil {
		return func() {}
	}
	ctx, cancel := context.WithCancel(ctx)
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		t.refresh()
		ticker := time.NewTicker(titleInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				t.refresh()
			}
		}
	}()
	return func() {
		cancel()
		<-finished
		t.mu.Lock()
		t.done = true
		t.mu.Unlock()
		t.refresh()
	}
}

// begin marks the start of the Run of ticket.
func (t *Title) begin(ticket string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.ticket, t.runStart = ticket, t.now()
	t.mu.Unlock()
}

// end marks that no Run is going on: the Factory waits for the next ticket.
func (t *Title) end() {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.ticket = ""
	t.mu.Unlock()
}

// titleOf is the Title behind out, nil when out is not a terminal.
func titleOf(out io.Writer) (t *Title) {
	t, _ = out.(*Title)
	return
}

func (t *Title) refresh() {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	text := formatTitle(t.ticket, t.done, now.Sub(t.runStart), now.Sub(t.lastEvent))
	_, _ = fmt.Fprintf(t.tty, "\x1b]2;%s\a", text)
}

// formatTitle is the title text: it holds no control characters, the ticket
// is untrusted.
func formatTitle(ticket string, done bool, runAge, sinceEvent time.Duration) (text string) {
	switch {
	case done:
		return "afk: koniec"
	case ticket == "":
		return "afk: czekam"
	}
	ticket = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, ticket)
	return fmt.Sprintf("%s · run %s · zdarzenie %s temu", ticket, runAge.Round(time.Second), sinceEvent.Round(time.Second))
}
