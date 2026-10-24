package image

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// maxPackerSize bounds the unpacked binary (the real one is ~100 MB).
const maxPackerSize = 512 << 20

// packerRelease identifies a Packer release zip and the SHA256 it must have.
type packerRelease struct {
	Version string
	URL     string
	SHA256  string
}

// pinnedPacker is the only Packer version afk runs (docs/images.md, ADR-0006).
// A version bump needs a human to verify HashiCorp's GPG signature of SHA256SUMS.
var pinnedPacker = packerRelease{
	Version: "1.16.2",
	URL:     "https://releases.hashicorp.com/packer/1.16.2/packer_1.16.2_linux_amd64.zip",
	SHA256:  "abbfe0cf74985084112659b53e3565f264f70a20914c3b7c4f789f54f61c2414",
}

// ensurePacker returns the path of the pinned Packer binary in binDir,
// downloading it if absent. The zip's SHA256 is verified before anything is
// unpacked; the binary appears under its final name only after a successful
// verification. A `packer` from PATH is never considered.
func ensurePacker(ctx context.Context, client *http.Client, binDir string, release packerRelease) (path string, err error) {
	final := filepath.Join(binDir, "packer-"+release.Version)
	if _, statErr := os.Stat(final); statErr == nil {
		path = final
		return
	}
	if err = os.MkdirAll(binDir, 0o755); err != nil {
		err = fmt.Errorf("creating %s: %w", binDir, err)
		return
	}
	archive, err := downloadVerified(ctx, client, binDir, release)
	if err != nil {
		return
	}
	defer func() { _ = os.Remove(archive) }()
	if err = unpackPacker(archive, binDir, final); err != nil {
		return
	}
	path = final
	return
}

// downloadVerified downloads release.URL into a temp file in dir and checks its SHA256.
// On any failure the temp file is removed.
func downloadVerified(ctx context.Context, client *http.Client, dir string, release packerRelease) (archive string, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, release.URL, nil)
	if err != nil {
		err = fmt.Errorf("building request for %s: %w", release.URL, err)
		return
	}
	resp, err := client.Do(req)
	if err != nil {
		err = fmt.Errorf("downloading %s: %w", release.URL, err)
		return
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		err = fmt.Errorf("downloading %s: status %s", release.URL, resp.Status)
		return
	}
	file, err := os.CreateTemp(dir, ".packer-download-*")
	if err != nil {
		err = fmt.Errorf("creating download file: %w", err)
		return
	}
	defer func() {
		if err != nil {
			_ = os.Remove(file.Name())
		}
	}()
	defer func() { _ = file.Close() }()

	hash := sha256.New()
	if _, err = io.Copy(io.MultiWriter(file, hash), resp.Body); err != nil {
		err = fmt.Errorf("downloading %s: %w", release.URL, err)
		return
	}
	if got := hex.EncodeToString(hash.Sum(nil)); !strings.EqualFold(got, release.SHA256) {
		err = fmt.Errorf("packer %s checksum mismatch: got %s, want %s", release.Version, got, release.SHA256)
		return
	}
	if err = file.Close(); err != nil {
		err = fmt.Errorf("closing download file: %w", err)
		return
	}
	archive = file.Name()
	return
}

// unpackPacker extracts the "packer" entry of the zip to final, atomically.
func unpackPacker(archive, dir, final string) (err error) {
	zr, err := zip.OpenReader(archive)
	if err != nil {
		err = fmt.Errorf("opening packer zip: %w", err)
		return
	}
	defer func() { _ = zr.Close() }()
	for _, entry := range zr.File {
		if entry.Name != "packer" {
			continue
		}
		err = extractEntry(entry, dir, final)
		return
	}
	err = errors.New("packer zip has no packer binary")
	return
}

func extractEntry(entry *zip.File, dir, final string) (err error) {
	src, err := entry.Open()
	if err != nil {
		err = fmt.Errorf("opening packer in zip: %w", err)
		return
	}
	defer func() { _ = src.Close() }()
	tmp, err := os.CreateTemp(dir, ".packer-unpack-*")
	if err != nil {
		err = fmt.Errorf("creating %s: %w", dir, err)
		return
	}
	defer func() {
		if err != nil {
			_ = os.Remove(tmp.Name())
		}
	}()
	defer func() { _ = tmp.Close() }()
	written, err := io.Copy(tmp, io.LimitReader(src, maxPackerSize+1))
	if err != nil {
		err = fmt.Errorf("unpacking packer: %w", err)
		return
	}
	if written > maxPackerSize {
		err = errors.New("unpacking packer: binary too large")
		return
	}
	if err = tmp.Chmod(0o755); err != nil {
		err = fmt.Errorf("chmod packer: %w", err)
		return
	}
	if err = tmp.Close(); err != nil {
		err = fmt.Errorf("closing packer: %w", err)
		return
	}
	if err = os.Rename(tmp.Name(), final); err != nil {
		err = fmt.Errorf("installing packer: %w", err)
	}
	return
}
