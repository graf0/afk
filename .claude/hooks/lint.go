//go:build ignore

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type hookInput struct {
	ToolInput struct {
		FilePath string `json:"file_path"`
	} `json:"tool_input"`
}

func main() {
	var input hookInput
	if err := json.NewDecoder(os.Stdin).Decode(&input); err != nil {
		os.Exit(0)
	}

	fp := filepath.ToSlash(input.ToolInput.FilePath)

	// Tylko pliki .go
	if !strings.HasSuffix(fp, ".go") {
		os.Exit(0)
	}

	// Pomiń wygenerowany kod
	if strings.HasSuffix(fp, "_templ.go") || strings.HasPrefix(fp, "db/generated/") {
		os.Exit(0)
	}

	// Uruchom lint na pakiecie pliku (pojedynczy plik gubi resztę pakietu → fałszywe typecheck);
	// --new-from-rev=HEAD: blokują tylko nowe problemy
	dir := filepath.Dir(input.ToolInput.FilePath)
	if wd, err := os.Getwd(); err == nil {
		if rel, err := filepath.Rel(wd, dir); err == nil {
			dir = rel
		}
	}
	cmd := exec.Command("golangci-lint", "run", "--timeout=20s", "--new-from-rev=HEAD", "./"+filepath.ToSlash(dir))
	out, err := cmd.CombinedOutput()
	if err == nil {
		os.Exit(0)
	}

	// Lint failed → block z błędami
	result := map[string]string{
		"decision": "block",
		"reason":   fmt.Sprintf("golangci-lint errors in %s:\n\n%s\nFix before continuing.", fp, strings.TrimSpace(string(out))),
	}
	json.NewEncoder(os.Stdout).Encode(result)
}
