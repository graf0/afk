package image

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

// maxLineSize bounds one machine-readable line; shell output lines can be long.
const maxLineSize = 1 << 20

// packerComma is how Packer encodes a comma inside a machine-readable field.
const packerComma = "%!(PACKER_COMMA)"

var fieldDecoder = strings.NewReplacer(packerComma, ",", `\n`, "\n", `\r`, "\r")

// Event is one line of `packer -machine-readable` output:
// timestamp,target,type,data...
type Event struct {
	Time   time.Time
	Target string
	Type   string
	Data   []string
}

// ParseEvent parses one machine-readable line. Fields are split first and
// decoded afterwards, so an encoded comma never splits a field.
func ParseEvent(line string) (event Event, err error) {
	fields := strings.Split(line, ",")
	if len(fields) < 3 {
		err = fmt.Errorf("parsing machine-readable line %q: want at least 3 fields", line)
		return
	}
	seconds, err := strconv.ParseInt(fields[0], 10, 64)
	if err != nil {
		err = fmt.Errorf("parsing machine-readable timestamp in %q: %w", line, err)
		return
	}
	event = Event{
		Time:   time.Unix(seconds, 0),
		Target: fieldDecoder.Replace(fields[1]),
		Type:   fieldDecoder.Replace(fields[2]),
	}
	for _, field := range fields[3:] {
		event.Data = append(event.Data, fieldDecoder.Replace(field))
	}
	return
}

// Text returns the human-readable form of the event, or "" if it has none.
// Only ui and error events are shown; the rest is bookkeeping.
func (e Event) Text() (text string) {
	switch {
	case e.Type == "ui" && len(e.Data) >= 2:
		text = strings.Join(e.Data[1:], ",")
	case e.Type == "error" && len(e.Data) >= 1:
		text = "error: " + strings.Join(e.Data, ",")
	}
	return
}

// streamEvents reads Packer output from r, writing it readably to out as it
// arrives. Lines that are not machine-readable are passed through. It returns
// the messages of Packer's error events.
func streamEvents(r io.Reader, out io.Writer) (buildErrors []string, err error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), maxLineSize)
	for scanner.Scan() {
		line := strings.TrimRight(scanner.Text(), "\r")
		if line == "" {
			continue
		}
		event, parseErr := ParseEvent(line)
		if parseErr != nil {
			_, err = fmt.Fprintln(out, line)
		} else if text := event.Text(); text != "" {
			_, err = fmt.Fprintln(out, text)
			if event.Type == "error" {
				buildErrors = append(buildErrors, strings.Join(event.Data, ","))
			}
		}
		if err != nil {
			err = fmt.Errorf("writing build output: %w", err)
			return
		}
	}
	if err = scanner.Err(); err != nil {
		_, _ = io.Copy(io.Discard, r) // never leave Packer blocked on a full pipe
		err = fmt.Errorf("reading packer output: %w", err)
	}
	return
}
