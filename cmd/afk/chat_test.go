package main

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
)

func TestParseChat(t *testing.T) {
	for name, tc := range map[string]struct {
		args    []string
		want    chatCommand
		wantErr string
	}{
		"enter":                 {args: []string{"plan"}, want: chatCommand{kind: chatEnter, name: "plan"}},
		"enter --rm after":      {args: []string{"plan", "--rm"}, want: chatCommand{kind: chatEnter, name: "plan", remove: true}},
		"enter --rm before":     {args: []string{"--rm", "plan"}, want: chatCommand{kind: chatEnter, name: "plan", remove: true}},
		"args after --":         {args: []string{"plan", "--", "--resume", "-x"}, want: chatCommand{kind: chatEnter, name: "plan", claudeArgs: []string{"--resume", "-x"}}},
		"flag, name and args":   {args: []string{"--rm", "plan", "--", "--model", "opus"}, want: chatCommand{kind: chatEnter, name: "plan", remove: true, claudeArgs: []string{"--model", "opus"}}},
		"empty args after --":   {args: []string{"plan", "--"}, want: chatCommand{kind: chatEnter, name: "plan"}},
		"flag-like args stay":   {args: []string{"plan", "--", "--rm"}, want: chatCommand{kind: chatEnter, name: "plan", claudeArgs: []string{"--rm"}}},
		"ls":                    {args: []string{"ls"}, want: chatCommand{kind: chatList}},
		"shell":                 {args: []string{"shell", "plan"}, want: chatCommand{kind: chatShell, name: "plan"}},
		"rm":                    {args: []string{"rm", "plan"}, want: chatCommand{kind: chatRm, name: "plan"}},
		"no args":               {args: nil, wantErr: "usage"},
		"only --":               {args: []string{"--", "x"}, wantErr: "usage"},
		"only flag":             {args: []string{"--rm"}, wantErr: "usage"},
		"two names":             {args: []string{"a", "b"}, wantErr: "usage"},
		"unknown flag":          {args: []string{"a", "--x"}, wantErr: "usage"},
		"--expose is for #69":   {args: []string{"a", "--expose"}, wantErr: "usage"},
		"bad name":              {args: []string{"Plan"}, wantErr: "invalid Chat name"},
		"ls with a name":        {args: []string{"ls", "x"}, wantErr: "usage"},
		"ls with --":            {args: []string{"ls", "--", "x"}, wantErr: "usage"},
		"shell without a name":  {args: []string{"shell"}, wantErr: "usage"},
		"shell with two names":  {args: []string{"shell", "a", "b"}, wantErr: "usage"},
		"shell with --":         {args: []string{"shell", "a", "--", "b"}, wantErr: "usage"},
		"rm without a name":     {args: []string{"rm"}, wantErr: "usage"},
		"rm with --force":       {args: []string{"rm", "--force", "a"}, wantErr: "usage"},
		"rm bad name":           {args: []string{"rm", "Plan"}, wantErr: "invalid Chat name"},
		"old command new":       {args: []string{"new", "plan"}, wantErr: "usage"},
		"old command attach":    {args: []string{"attach", "plan"}, wantErr: "usage"},
		"old command export":    {args: []string{"export", "plan"}, wantErr: "usage"},
		"shell is no Chat name": {args: []string{"shell", "ls"}, wantErr: "command of `afk chat`"},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := parseChat(tc.args)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Errorf("err = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil || !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %+v, %v; want %+v", got, err, tc.want)
			}
		})
	}
}

func TestRunChatUsage(t *testing.T) {
	for _, args := range [][]string{nil, {"ls", "x"}, {"shell"}, {"rm"}, {"new", "x"}} {
		err := runChat(t.Context(), args, ".", &bytes.Buffer{})
		if err == nil || !strings.Contains(err.Error(), "afk chat <name>") {
			t.Errorf("runChat(%q): err = %v, want usage", args, err)
		}
	}
}
