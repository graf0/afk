package image

import (
	"archive/zip"
	"bytes"
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

const (
	// maxPackerSize bounds the unpacked binary (the real one is ~100 MB).
	maxPackerSize = 512 << 20
	// maxPackerZipSize bounds the downloaded zip (the real one is ~30 MB); it is held in memory.
	maxPackerZipSize = 256 << 20
)

// pinnedPacker is the only Packer version afk runs (docs/images.md, ADR-0006).
// A version bump needs a human to verify HashiCorp's GPG signature of SHA256SUMS.
var pinnedPacker = packerRelease{
	Version:      "1.16.2",
	URL:          "https://releases.hashicorp.com/packer/1.16.2/packer_1.16.2_linux_amd64.zip",
	SHA256:       "abbfe0cf74985084112659b53e3565f264f70a20914c3b7c4f789f54f61c2414",
	BinarySHA256: "6f64a1e9e71be8b3a2f0225ccf0d51b5446fbf79d9ad323e917a0126ab6ee38a",
}

// packerRelease identifies a Packer release zip, the SHA256 it must have and
// the SHA256 of the `packer` binary inside it.
type packerRelease struct {
	Version      string
	URL          string
	SHA256       string // of the zip
	BinarySHA256 string // of the unpacked binary
}

// ensurePacker returns the path of the pinned Packer binary in binDir. The
// binary's SHA256 is checked on every call, not only after the download: a
// cached binary that no longer matches is removed and fetched again. A fresh
// download is verified (zip SHA256, then binary SHA256) before the binary
// appears under its final name. A `packer` from PATH is never considered.
func ensurePacker(ctx context.Context, client *http.Client, binDir string, release packerRelease) (path string, err error) {
	final := filepath.Join(binDir, "packer-"+release.Version)
	if verifyFile(final, release.BinarySHA256) == nil {
		path = final
		return
	}
	if err = os.Remove(final); err != nil && !errors.Is(err, os.ErrNotExist) {
		err = fmt.Errorf("removing unverified %s: %w", final, err)
		return
	}
	if err = os.MkdirAll(binDir, 0o755); err != nil {
		err = fmt.Errorf("creating %s: %w", binDir, err)
		return
	}
	archive, err := downloadVerified(ctx, client, release)
	if err != nil {
		return
	}
	if err = unpackPacker(archive, binDir, final, release.BinarySHA256); err != nil {
		return
	}
	if err = verifyFile(final, release.BinarySHA256); err != nil {
		_ = os.Remove(final)
		return
	}
	path = final
	return
}

// downloadVerified downloads release.URL into memory and checks its SHA256.
// The checked bytes are the ones returned.
func downloadVerified(ctx context.Context, client *http.Client, release packerRelease) (archive []byte, err error) {
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
	archive, err = io.ReadAll(io.LimitReader(resp.Body, maxPackerZipSize+1))
	if err != nil {
		err = fmt.Errorf("downloading %s: %w", release.URL, err)
		return
	}
	if len(archive) > maxPackerZipSize {
		err = fmt.Errorf("downloading %s: zip too large", release.URL)
		return
	}
	digest := sha256.Sum256(archive)
	if got := hex.EncodeToString(digest[:]); !strings.EqualFold(got, release.SHA256) {
		err = fmt.Errorf("packer %s zip checksum mismatch: got %s, want %s", release.Version, got, release.SHA256)
		archive = nil
	}
	return
}

// unpackPacker extracts the "packer" entry of the zip to final, atomically,
// and only if its SHA256 is wantSum.
func unpackPacker(archive []byte, dir, final, wantSum string) (err error) {
	zr, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		err = fmt.Errorf("opening packer zip: %w", err)
		return
	}
	for _, entry := range zr.File {
		if entry.Name != "packer" {
			continue
		}
		err = extractEntry(entry, dir, final, wantSum)
		return
	}
	err = errors.New("packer zip has no packer binary")
	return
}

func extractEntry(entry *zip.File, dir, final, wantSum string) (err error) {
	src, err := entry.Open()
	if err != nil {
		err = fmt.Errorf("opening packer in zip: %w", err)
		return
	}
	defer func() { _ = src.Close() }()
	tmp, err := os.CreateTemp(dir, ".packer-unpack-*")
	if err != nil {
		err = fmt.Errorf("creating temp file in %s: %w", dir, err)
		return
	}
	defer func() {
		if err != nil {
			_ = os.Remove(tmp.Name())
		}
	}()
	defer func() { _ = tmp.Close() }()
	hash := sha256.New()
	written, err := io.Copy(io.MultiWriter(tmp, hash), io.LimitReader(src, maxPackerSize+1))
	if err != nil {
		err = fmt.Errorf("unpacking packer: %w", err)
		return
	}
	if written > maxPackerSize {
		err = errors.New("unpacking packer: binary too large")
		return
	}
	if got := hex.EncodeToString(hash.Sum(nil)); !strings.EqualFold(got, wantSum) {
		err = fmt.Errorf("packer binary checksum mismatch: got %s, want %s", got, wantSum)
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

// verifyFile checks that the file at path has the SHA256 wantSum.
func verifyFile(path, wantSum string) (err error) {
	file, err := os.Open(path)
	if err != nil {
		return
	}
	defer func() { _ = file.Close() }()
	hash := sha256.New()
	if _, err = io.Copy(hash, file); err != nil {
		err = fmt.Errorf("hashing %s: %w", path, err)
		return
	}
	if got := hex.EncodeToString(hash.Sum(nil)); !strings.EqualFold(got, wantSum) {
		err = fmt.Errorf("%s checksum mismatch: got %s, want %s", path, got, wantSum)
	}
	return
}
