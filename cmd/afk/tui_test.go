package main

import (
	"strings"
	"testing"
)

func TestTUIUsage(t *testing.T) {
	if err := runTUI(t.Context(), []string{"x"}); err == nil || !strings.Contains(err.Error(), tuiUsage) {
		t.Errorf("runTUI: %v", err)
	}
	if err := runSidebar(t.Context(), []string{"x"}); err == nil || !strings.Contains(err.Error(), "afk sidebar") {
		t.Errorf("runSidebar: %v", err)
	}
}
