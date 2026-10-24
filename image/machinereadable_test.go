package image

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestParseEvent(t *testing.T) {
	tests := []struct {
		name string
		line string
		want packerEvent
	}{
		{
			name: "ui message",
			line: "1700000000,qemu.debian,ui,message,==> qemu.debian: Waiting for SSH",
			want: packerEvent{Time: time.Unix(1700000000, 0), Target: "qemu.debian", Type: "ui", Data: []string{"message", "==> qemu.debian: Waiting for SSH"}},
		},
		{
			name: "commas in data are decoded after splitting",
			line: "1700000000,,ui,say,one%!(PACKER_COMMA) two%!(PACKER_COMMA) three",
			want: packerEvent{Time: time.Unix(1700000000, 0), Type: "ui", Data: []string{"say", "one, two, three"}},
		},
		{
			name: "encoded newlines",
			line: `1700000000,,error,first\nsecond`,
			want: packerEvent{Time: time.Unix(1700000000, 0), Type: "error", Data: []string{"first\nsecond"}},
		},
		{
			name: "no data",
			line: "1700000000,,ping",
			want: packerEvent{Time: time.Unix(1700000000, 0), Type: "ping"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseEvent(tt.line)
			if err != nil {
				t.Fatal(err)
			}
			if !got.Time.Equal(tt.want.Time) || got.Target != tt.want.Target || got.Type != tt.want.Type || strings.Join(got.Data, "|") != strings.Join(tt.want.Data, "|") {
				t.Errorf("parseEvent() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestParseEventRejectsMalformed(t *testing.T) {
	for _, line := range []string{"", "garbage", "notatime,target,ui,say,x", "1700000000,target"} {
		if _, err := parseEvent(line); err == nil {
			t.Errorf("parseEvent(%q) succeeded, want error", line)
		}
	}
}

func TestStreamEvents(t *testing.T) {
	input := strings.Join([]string{
		"1,qemu.debian,ui,say,==> qemu.debian: Starting VM",
		"2,qemu.debian,ui,message,    qemu.debian: apt%!(PACKER_COMMA) done",
		"3,qemu.debian,ui,error,==> qemu.debian: Build failed",
		"4,,error,step failed%!(PACKER_COMMA) exit 100",
		"5,qemu.debian,artifact-count,1",
		"not a machine readable line",
		"",
	}, "\n")
	var out bytes.Buffer
	buildErrors, err := streamEvents(strings.NewReader(input), &out)
	if err != nil {
		t.Fatal(err)
	}
	wantOut := "==> qemu.debian: Starting VM\n    qemu.debian: apt, done\n==> qemu.debian: Build failed\nerror: step failed, exit 100\nnot a machine readable line\n"
	if out.String() != wantOut {
		t.Errorf("output = %q, want %q", out.String(), wantOut)
	}
	if len(buildErrors) != 1 || buildErrors[0] != "step failed, exit 100" {
		t.Errorf("buildErrors = %q", buildErrors)
	}
}
