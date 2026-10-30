package agent

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"unicode"
)

// Report statuses.
const (
	StatusDone      Status = "done"
	StatusNeedsInfo Status = "needs_info"
)

// maxStreamLine bounds one stream-json line (a tool result can carry a file).
const maxStreamLine = 64 << 20

// Schema is the JSON Schema of the Agent's report (claude --json-schema).
const Schema = `{"type":"object","properties":{` +
	`"status":{"type":"string","enum":["done","needs_info"]},` +
	`"reason":{"type":"string"},` +
	`"summary":{"type":"string"},` +
	`"deviations":{"type":"array","items":{"type":"string"}},` +
	`"doubts":{"type":"array","items":{"type":"string"}},` +
	`"questions":{"type":"array","items":{"type":"string"}},` +
	`"findings_fixed":{"type":"array","items":{"type":"string"}},` +
	`"findings_skipped":{"type":"array","items":{"type":"string"}}},` +
	`"required":["status","reason","summary","deviations"]}`

// toolArgKeys are tried in order for the one-line summary of a tool call
// (agent_lines in docs/reference/ralph.sh).
var toolArgKeys = []string{"command", "file_path", "pattern", "description"}

// Status is the outcome the Agent reports.
type Status string

// Report is the Agent's structured result of a session.
type Report struct {
	Status     Status   `json:"status"`
	Reason     string   `json:"reason"`
	Summary    string   `json:"summary"`
	Deviations []string `json:"deviations"`
	Doubts     []string `json:"doubts"`
	Questions  []string `json:"questions"`

	// Only the review session fills these.
	FindingsFixed   []string `json:"findings_fixed"`
	FindingsSkipped []string `json:"findings_skipped"`
}

// streamEvent is the part of a Claude Code stream-json event afk reads.
type streamEvent struct {
	Type    string `json:"type"`
	Subtype string `json:"subtype"`
	Message struct {
		Content []struct {
			Type  string                     `json:"type"`
			Text  string                     `json:"text"`
			Name  string                     `json:"name"`
			Input map[string]json.RawMessage `json:"input"`
		} `json:"content"`
	} `json:"message"`
	IsError          bool            `json:"is_error"`
	Result           string          `json:"result"`
	StructuredOutput json.RawMessage `json:"structured_output"`
}

// --- stream ---

// Stream reads Claude Code's stream-json from r: every line goes to log
// as is, the Agent's text and one line per tool call to out (control
// characters removed, the stream comes from the untrusted Sandbox). It returns
// the report from the last result event. Each line is written to log in one
// Write, so a rotating log splits only between lines.
func Stream(r io.Reader, out, log io.Writer) (report Report, err error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64<<10), maxStreamLine)
	var result *streamEvent
	for scanner.Scan() {
		line := scanner.Bytes()
		// A copy: appending to line would overwrite the scanner's buffer.
		if _, err = log.Write(append(slices.Clip(line), '\n')); err != nil {
			err = fmt.Errorf("writing log: %w", err)
			return
		}
		var event streamEvent
		if json.Unmarshal(line, &event) != nil {
			continue
		}
		switch event.Type {
		case "assistant":
			for _, text := range eventLines(event) {
				_, _ = fmt.Fprintln(out, text)
			}
		case "result":
			result = &event
		}
	}
	if err = scanner.Err(); err != nil {
		err = fmt.Errorf("reading agent stream: %w", err)
		return
	}
	if result == nil {
		err = errors.New("agent stream ended without a result")
		return
	}
	report, err = parseResult(*result)
	return
}

// eventLines are the lines of an assistant event shown to the user.
func eventLines(event streamEvent) (lines []string) {
	for _, content := range event.Message.Content {
		switch {
		case content.Type == "text":
			lines = append(lines, Sanitize(content.Text))
		case content.Type == "tool_use" && content.Name != "StructuredOutput":
			lines = append(lines, Sanitize(strings.TrimSpace("→ "+content.Name+" "+toolArg(content.Input))))
		}
	}
	return
}

// toolArg is the first line of the first present argument in toolArgKeys.
func toolArg(input map[string]json.RawMessage) (arg string) {
	for _, key := range toolArgKeys {
		raw, ok := input[key]
		if !ok || string(raw) == "null" || string(raw) == "false" {
			continue
		}
		if json.Unmarshal(raw, &arg) != nil {
			arg = string(raw)
		}
		arg, _, _ = strings.Cut(arg, "\n")
		return
	}
	return
}

// Sanitize drops control characters (terminal escapes) except newline and
// tab, and format characters (bidi overrides, zero-width) from text that
// comes from the Sandbox.
func Sanitize(text string) (clean string) {
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, text)
}

// parseResult extracts the report from the result event.
func parseResult(event streamEvent) (report Report, err error) {
	if event.IsError || event.Subtype != "success" {
		// Claude Code reports e.g. a 401 as is_error with subtype "success".
		err = fmt.Errorf("agent session failed: %s %s", Sanitize(event.Subtype), Sanitize(event.Result))
		return
	}
	if len(event.StructuredOutput) == 0 || string(event.StructuredOutput) == "null" {
		err = errors.New("agent result has no report (structured_output)")
		return
	}
	if err = json.Unmarshal(event.StructuredOutput, &report); err != nil {
		err = fmt.Errorf("decoding agent report: %w", err)
		return
	}
	if report.Status != StatusDone && report.Status != StatusNeedsInfo {
		err = fmt.Errorf("agent report has invalid status %q", Sanitize(string(report.Status)))
	}
	return
}

// --- printing ---

// String renders the report for the terminal.
func (r Report) String() (text string) {
	var b strings.Builder
	fmt.Fprintf(&b, "status: %s — %s\n", r.Status, r.Reason)
	if r.Summary != "" {
		fmt.Fprintf(&b, "\n%s\n", r.Summary)
	}
	for _, section := range []struct {
		title string
		items []string
	}{{"Deviations", r.Deviations}, {"Doubts", r.Doubts}, {"Questions", r.Questions},
		{"Findings fixed", r.FindingsFixed}, {"Findings skipped", r.FindingsSkipped}} {
		if len(section.items) == 0 {
			continue
		}
		fmt.Fprintf(&b, "\n%s:\n", section.title)
		for _, item := range section.items {
			fmt.Fprintf(&b, "- %s\n", item)
		}
	}
	return Sanitize(b.String())
}
