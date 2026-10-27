package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestParseChatNew(t *testing.T) {
	for name, tc := range map[string]struct {
		args                []string
		wantName, wantModel string
		wantEffort, wantErr string
	}{
		"name only":        {args: []string{"plan"}, wantName: "plan"},
		"flags after":      {args: []string{"plan", "--model", "opus", "--effort", "high"}, wantName: "plan", wantModel: "opus", wantEffort: "high"},
		"flags before":     {args: []string{"--effort=max", "plan"}, wantName: "plan", wantEffort: "max"},
		"no name":          {args: []string{"--model", "opus"}, wantErr: "usage"},
		"two names":        {args: []string{"a", "b"}, wantErr: "usage"},
		"unknown flag":     {args: []string{"a", "--x"}, wantErr: "usage"},
		"bad name":         {args: []string{"Plan"}, wantErr: "invalid Chat name"},
		"bad effort":       {args: []string{"plan", "--effort", "huge"}, wantErr: "invalid effort"},
		"empty":            {args: nil, wantErr: "usage"},
		"leading dash arg": {args: []string{"--", "-x"}, wantErr: "invalid Chat name"},
	} {
		t.Run(name, func(t *testing.T) {
			n, m, e, err := parseChatNew(tc.args)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Errorf("err = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil || n != tc.wantName || m != tc.wantModel || e != tc.wantEffort {
				t.Errorf("got %q %q %q %v", n, m, e, err)
			}
		})
	}
}

func TestParseChatRm(t *testing.T) {
	for name, tc := range map[string]struct {
		args      []string
		wantName  string
		wantForce bool
		wantErr   string
	}{
		"name only":    {args: []string{"plan"}, wantName: "plan"},
		"force before": {args: []string{"--force", "plan"}, wantName: "plan", wantForce: true},
		"force after":  {args: []string{"plan", "--force"}, wantName: "plan", wantForce: true},
		"no name":      {args: []string{"--force"}, wantErr: "usage"},
		"two names":    {args: []string{"a", "b"}, wantErr: "usage"},
		"unknown flag": {args: []string{"a", "--x"}, wantErr: "usage"},
		"bad name":     {args: []string{"Plan"}, wantErr: "invalid Chat name"},
	} {
		t.Run(name, func(t *testing.T) {
			n, f, err := parseChatRm(tc.args)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Errorf("err = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil || n != tc.wantName || f != tc.wantForce {
				t.Errorf("got %q %v %v", n, f, err)
			}
		})
	}
}

func TestRunChatUsage(t *testing.T) {
	for _, args := range [][]string{nil, {"attach"}, {"attach", "x", "y"}, {"list", "x"}, {"new"}, {"rm"}} {
		err := runChat(t.Context(), args, ".", &bytes.Buffer{})
		if err == nil || !strings.Contains(err.Error(), "afk chat new") {
			t.Errorf("runChat(%q): err = %v, want usage", args, err)
		}
	}
}
