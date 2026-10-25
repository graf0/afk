package main

import (
	"slices"
	"strings"
	"testing"
)

func TestParseSelector(t *testing.T) {
	for _, args := range [][]string{nil, {"*"}} {
		if selector, err := parseSelector(args); err != nil || selector != nil {
			t.Errorf("parseSelector(%q) = %v, %v; want nil (every ready ticket)", args, selector, err)
		}
	}
	for args, want := range map[string][]int{"#5": {5}, "12": {12}, "#12 #15": {12, 15}, "#15 3": {15, 3}} {
		if selector, err := parseSelector(strings.Fields(args)); err != nil || !slices.Equal(selector, want) {
			t.Errorf("parseSelector(%q) = %v, %v; want %v", args, selector, err, want)
		}
	}
	for _, args := range [][]string{{"*", "#1"}, {"#0"}, {"#-1"}, {"#x"}, {"#1", "*"}} {
		if _, err := parseSelector(args); err == nil || !strings.Contains(err.Error(), "afk factory") {
			t.Errorf("parseSelector(%q): err = %v, want usage", args, err)
		}
	}
}
