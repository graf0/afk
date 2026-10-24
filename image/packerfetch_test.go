package image

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

const fakePackerContent = "#!/bin/sh\necho fake packer\n"

// fakeRelease serves a zip with a fake packer binary and returns a release
// pointing at it; sum is the checksum the release will demand.
func fakeRelease(t *testing.T, sum string) (release packerRelease, requests *atomic.Int32) {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range map[string]string{"LICENSE.txt": "license", "packer": fakePackerContent} {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = w.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	archive := buf.Bytes()
	if sum == "" {
		digest := sha256.Sum256(archive)
		sum = hex.EncodeToString(digest[:])
	}
	requests = new(atomic.Int32)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_, _ = w.Write(archive)
	}))
	t.Cleanup(server.Close)
	release = packerRelease{Version: "9.9.9", URL: server.URL + "/packer.zip", SHA256: sum}
	return
}

func TestEnsurePackerDownloadsAndVerifies(t *testing.T) {
	release, requests := fakeRelease(t, "")
	binDir := filepath.Join(t.TempDir(), "bin")

	path, err := ensurePacker(t.Context(), http.DefaultClient, binDir, release)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(binDir, "packer-9.9.9"); path != want {
		t.Errorf("path = %q, want %q", path, want)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != fakePackerContent {
		t.Errorf("binary content = %q", data)
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o755 {
		t.Errorf("mode = %v, want 0755", info.Mode().Perm())
	}

	// A second call uses the cached binary.
	if _, err = ensurePacker(t.Context(), http.DefaultClient, binDir, release); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 1 {
		t.Errorf("requests = %d, want 1", requests.Load())
	}
}

func TestEnsurePackerRejectsWrongChecksum(t *testing.T) {
	release, _ := fakeRelease(t, strings.Repeat("0", 64))
	binDir := filepath.Join(t.TempDir(), "bin")

	path, err := ensurePacker(t.Context(), http.DefaultClient, binDir, release)
	if err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("err = %v, want checksum error", err)
	}
	if path != "" {
		t.Errorf("path = %q, want empty", path)
	}
	entries, _ := os.ReadDir(binDir)
	if len(entries) != 0 {
		t.Errorf("bin dir not empty after failed verification: %v", entries)
	}

	// The unverified file must not be picked up by a later call either.
	if _, err = ensurePacker(t.Context(), http.DefaultClient, binDir, release); err == nil {
		t.Error("second call succeeded, want checksum error again")
	}
}

func TestEnsurePackerRejectsHTTPError(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()
	release := packerRelease{Version: "9.9.9", URL: server.URL, SHA256: strings.Repeat("0", 64)}
	if _, err := ensurePacker(t.Context(), http.DefaultClient, t.TempDir(), release); err == nil {
		t.Error("err = nil, want HTTP status error")
	}
}

func TestPinnedPackerRelease(t *testing.T) {
	// Values from docs/images.md; a bump must change code and docs together.
	if pinnedPacker.Version != "1.16.2" ||
		pinnedPacker.SHA256 != "abbfe0cf74985084112659b53e3565f264f70a20914c3b7c4f789f54f61c2414" ||
		pinnedPacker.URL != "https://releases.hashicorp.com/packer/1.16.2/packer_1.16.2_linux_amd64.zip" {
		t.Errorf("pinnedPacker = %+v", pinnedPacker)
	}
}
