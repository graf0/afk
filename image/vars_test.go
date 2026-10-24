package image

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestWriteVarFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vars.json")
	vars := packerVars{
		BaseImage:      "https://example.com/debian.qcow2",
		BaseChecksum:   "file:https://example.com/SHA512SUMS",
		OutputDir:      "/var/lib/afk/base/.build-1/out",
		SSHKey:         "/home/u/.local/state/afk/ssh/id_ed25519",
		SSHPort:        40022,
		ProxyPort:      40128,
		SeedFiles:      []string{"/tmp/seed/user-data", "/tmp/seed/meta-data"},
		DiskName:       "debian13.qcow2",
		Provision:      []string{"echo \"quoted\"", "line one\nline two"},
		FinalProvision: []string{"final one"},
	}
	if err := writeVarFile(path, vars); err != nil {
		t.Fatal(err)
	}
	got := readJSON(t, path)
	want := map[string]any{
		"base_image":      "https://example.com/debian.qcow2",
		"base_checksum":   "file:https://example.com/SHA512SUMS",
		"output_dir":      "/var/lib/afk/base/.build-1/out",
		"ssh_key":         "/home/u/.local/state/afk/ssh/id_ed25519",
		"ssh_port":        float64(40022),
		"proxy_port":      float64(40128),
		"seed_files":      []any{"/tmp/seed/user-data", "/tmp/seed/meta-data"},
		"disk_name":       "debian13.qcow2",
		"provision":       []any{"echo \"quoted\"", "line one\nline two"},
		"final_provision": []any{"final one"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("var-file = %v, want %v", got, want)
	}
}

func TestWriteVarFileNilListsAreEmptyLists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vars.json")
	if err := writeVarFile(path, packerVars{}); err != nil {
		t.Fatal(err)
	}
	got := readJSON(t, path)
	for _, key := range []string{"provision", "final_provision", "seed_files"} {
		if list, ok := got[key].([]any); !ok || len(list) != 0 {
			t.Errorf("%s = %#v, want empty list (Packer rejects null)", key, got[key])
		}
	}
}

func readJSON(t *testing.T, path string) (decoded map[string]any) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("%s is not JSON: %v", path, err)
	}
	return
}
