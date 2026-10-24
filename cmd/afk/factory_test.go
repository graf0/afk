package main

import (
	"strings"
	"testing"
)

func TestParseSelector(t *testing.T) {
	for arg, want := range map[string]int{"#5": 5, "12": 12} {
		if n, err := parseSelector([]string{arg}); err != nil || n != want {
			t.Errorf("parseSelector(%q) = %d, %v; want %d", arg, n, err, want)
		}
	}
	for _, args := range [][]string{nil, {"*"}, {"#0"}, {"#-1"}, {"#x"}, {"#1", "#2"}} {
		if _, err := parseSelector(args); err == nil || !strings.Contains(err.Error(), "afk factory") {
			t.Errorf("parseSelector(%q): err = %v, want usage", args, err)
		}
	}
}
