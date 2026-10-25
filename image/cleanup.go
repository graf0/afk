package image

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"slices"
)

const (
	qcow2Magic = "QFI\xfb"
	// qcow2HeaderLen covers the magic, version and the backing file fields.
	qcow2HeaderLen = 20
)

// --- cleanup ---

// Cleanup removes the versions of the Base image of machine of project that
// are not the newest, not the one the newest replaced and not the backing
// file of any of overlays (the disks of the Run and Chat VMs; nil is an error,
// so that nothing is removed on a guess). A VM left after a failure thus keeps
// its version while its overlay exists. In-progress builds (.build-*) are
// never touched. It must run under the Project's build lock, so that no build
// installs a version meanwhile. It returns the removed versions.
//
// ponytail: the previous version stays because a Factory takes a version from
// Ensure and creates its overlay later, so a build finishing in between would
// otherwise remove the version under it. Known ceiling: a process stalled
// across two builds. The proper fix is creating the overlay under the shared
// lock.
func Cleanup(paths Paths, project, machine string, overlays []string) (removed []string, err error) {
	if overlays == nil {
		err = errors.New("the list of overlays is not known")
		return
	}
	machineDir := paths.machineDir(project, machine)
	entries, err := os.ReadDir(machineDir)
	if err != nil {
		err = fmt.Errorf("reading %s: %w", machineDir, err)
		return
	}
	var versions []string
	for _, entry := range entries {
		if entry.IsDir() && versionPattern.MatchString(entry.Name()) {
			versions = append(versions, entry.Name())
		}
	}
	if len(versions) < 3 { // the newest and the previous one stay anyway
		return
	}
	used, err := usedBackingDirs(overlays)
	if err != nil {
		return
	}
	slices.Sort(versions)
	var errs []error
	for _, version := range versions[:len(versions)-2] {
		dir := filepath.Join(machineDir, version)
		if used[dir] {
			continue
		}
		if rmErr := os.RemoveAll(dir); rmErr != nil {
			errs = append(errs, fmt.Errorf("removing %s: %w", dir, rmErr))
			continue
		}
		removed = append(removed, version)
	}
	err = errors.Join(errs...)
	return
}

// usedBackingDirs returns the directories of the backing files of overlays.
func usedBackingDirs(overlays []string) (dirs map[string]bool, err error) {
	dirs = map[string]bool{}
	for _, disk := range overlays {
		var backing string
		if backing, err = backingFile(disk); err != nil {
			return
		}
		if backing != "" {
			dirs[filepath.Dir(backing)] = true
		}
	}
	return
}

// backingFile reads the backing file path from the header of the qcow2 file
// at path (empty: none), cleaned, a relative one resolved against the
// directory of path. It does not open the backing file, which a running
// VM may be using.
func backingFile(path string) (backing string, err error) {
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) { // the VM was removed meanwhile
			err = nil
			return
		}
		err = fmt.Errorf("opening overlay: %w", err)
		return
	}
	defer func() { _ = file.Close() }()
	header := make([]byte, qcow2HeaderLen)
	if _, err = io.ReadFull(file, header); err != nil {
		err = fmt.Errorf("reading the header of %s: %w", path, err)
		return
	}
	if string(header[:4]) != qcow2Magic {
		err = fmt.Errorf("%s is not a qcow2 file", path)
		return
	}
	offset := binary.BigEndian.Uint64(header[8:16])
	size := binary.BigEndian.Uint32(header[16:20])
	if offset == 0 || size == 0 {
		return
	}
	if offset > math.MaxInt64 {
		err = fmt.Errorf("%s: backing file offset %d out of range", path, offset)
		return
	}
	if size > 1023 { // qcow2 limit
		err = fmt.Errorf("%s: backing file name of %d bytes", path, size)
		return
	}
	name := make([]byte, size)
	if _, err = file.ReadAt(name, int64(offset)); err != nil {
		err = fmt.Errorf("reading the backing file of %s: %w", path, err)
		return
	}
	backing = string(name)
	if !filepath.IsAbs(backing) {
		backing = filepath.Join(filepath.Dir(path), backing)
	}
	backing = filepath.Clean(backing)
	return
}
