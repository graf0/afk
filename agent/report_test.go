package agent

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

const sampleStream = `{"type":"system","subtype":"init","session_id":"s"}
{"type":"assistant","message":{"content":[{"type":"text","text":"Reading the ticket."},{"type":"tool_use","name":"Read","input":{"file_path":"/work/TICKET.md"}}]}}
{"type":"user","message":{"content":[{"type":"tool_result","content":"..."}]}}
not json at all
{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"go test ./...\necho done","description":"Run tests"}}]}}
{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Grep","input":{"pattern":"func main"}},{"type":"tool_use","name":"Task","input":{"description":"review","prompt":"x"}},{"type":"tool_use","name":"TodoWrite","input":{"todos":[]}}]}}
{"type":"assistant","message":{"content":[{"type":"text","text":"evil \u001b[2Jclear"},{"type":"tool_use","name":"StructuredOutput","input":{"status":"done"}}]}}
{"type":"result","subtype":"success","is_error":false,"result":"ok","structured_output":{"status":"done","reason":"Done.","summary":"Added x.","deviations":["none really"],"doubts":["check y"]}}
`

func TestStream(t *testing.T) {
	var out, log bytes.Buffer
	report, err := Stream(strings.NewReader(sampleStream), &out, &log)
	if err != nil {
		t.Fatal(err)
	}
	want := Report{Status: StatusDone, Reason: "Done.", Summary: "Added x.", Deviations: []string{"none really"}, Doubts: []string{"check y"}}
	if report.Status != want.Status || report.Reason != want.Reason || report.Summary != want.Summary ||
		!slices.Equal(report.Deviations, want.Deviations) || !slices.Equal(report.Doubts, want.Doubts) {
		t.Errorf("report = %+v, want %+v", report, want)
	}
	wantOut := "Reading the ticket.\n" +
		"→ Read /work/TICKET.md\n" +
		"→ Bash go test ./...\n" +
		"→ Grep func main\n" +
		"→ Task review\n" +
		"→ TodoWrite\n" +
		"evil [2Jclear\n"
	if out.String() != wantOut {
		t.Errorf("out =\n%s\nwant\n%s", out.String(), wantOut)
	}
	if log.String() != sampleStream {
		t.Errorf("log differs from the stream:\n%s", log.String())
	}
}

func TestStreamErrors(t *testing.T) {
	for name, tc := range map[string]struct{ stream, want string }{
		"no result":        {`{"type":"assistant","message":{"content":[]}}`, "without a result"},
		"error result":     {`{"type":"result","subtype":"error_max_turns","is_error":true,"result":"too long"}`, "error_max_turns"},
		"auth error":       {`{"type":"result","subtype":"success","is_error":true,"result":"Failed to authenticate. API Error: 401"}`, "Failed to authenticate"},
		"no report":        {`{"type":"result","subtype":"success","result":"x"}`, "no report"},
		"invalid status":   {`{"type":"result","subtype":"success","structured_output":{"status":"maybe"}}`, "invalid status"},
		"malformed report": {`{"type":"result","subtype":"success","structured_output":{"status":1}}`, "decoding"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Stream(strings.NewReader(tc.stream+"\n"), &bytes.Buffer{}, &bytes.Buffer{})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestStreamLimit(t *testing.T) {
	_, err := stream(strings.NewReader(sampleStream), &bytes.Buffer{}, &bytes.Buffer{}, 300)
	if err == nil || !strings.Contains(err.Error(), "larger than 300") {
		t.Errorf("err = %v, want limit error", err)
	}
}

func TestSanitize(t *testing.T) {
	in := "a\x1b[31mb\tc\n\u202eevil\u2066x\u200by\u0085z\x7f"
	if got, want := Sanitize(in), "a[31mb\tc\nevilxyz"; got != want {
		t.Errorf("Sanitize(%q) = %q, want %q", in, got, want)
	}
}

func TestSchemaIsJSON(t *testing.T) {
	var schema map[string]any
	if err := json.Unmarshal([]byte(Schema), &schema); err != nil {
		t.Fatal(err)
	}
}

func TestReportString(t *testing.T) {
	text := Report{Status: "needs_info", Reason: "Unclear.", Questions: []string{"Which API?"}}.String()
	for _, want := range []string{"status: needs_info — Unclear.", "Questions:\n- Which API?"} {
		if !strings.Contains(text, want) {
			t.Errorf("report lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "Deviations") {
		t.Errorf("empty section printed:\n%s", text)
	}
}
