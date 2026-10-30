package stack

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
)

var (
	// includePattern finds a top-level include: (also quoted, or in a flow mapping).
	includePattern = regexp.MustCompile(`(?m)(^|[{,]\s*)["']?include["']?\s*:`)
	// extendsPattern finds an extends: key; it matters together with filePattern.
	extendsPattern = regexp.MustCompile(`(?m)(^|[{,]\s*)\s*["']?extends["']?\s*:`)
	filePattern    = regexp.MustCompile(`(?m)(^|[{,\s])["']?file["']?\s*:`)
)

// rejectExternalFiles fails when the compose files in the export pull in other
// files: include: and extends with file:. The protected paths of a Stack are
// its compose files and the Dockerfiles they name; a file brought in by
// reference would run on the host unprotected. The check reads the YAML as
// text and errs on the side of rejecting (a file: key beside any extends:).
func rejectExternalFiles(exportDir string) (err error) {
	for _, name := range []string{composeFile, composeAfkFile} {
		data, readErr := os.ReadFile(filepath.Join(exportDir, name))
		if readErr != nil {
			return readErr
		}
		switch {
		case includePattern.Match(data):
			return fmt.Errorf("%s: include: is not supported: the files of a Stack must be the two compose files, which afk protects", name)
		case extendsPattern.Match(data) && filePattern.Match(data):
			return fmt.Errorf("%s: extends with file: is not supported: the files of a Stack must be the two compose files, which afk protects", name)
		}
	}
	return nil
}

// Protected returns the files of the Stack the host builds from, in addition
// to the compose files themselves: the Dockerfiles of the services with
// build:, relative to the repository root, in slash form, sorted. The files
// only, not their contexts. A service with dockerfile_inline has none.
// Known after Up; the paths come from the base branch, which decides what
// the host runs (ADR-0010), so a Run or Chat result that changes one is rejected.
func (s *Stack) Protected() (paths []string) { return slices.Clone(s.protected) }

// protectedDockerfiles lists the Dockerfiles of the services with build:. A
// Dockerfile outside the export is an error, as in Up's image tags.
func protectedDockerfiles(exportDir string, services map[string]composeService) (paths []string, err error) {
	for name, service := range services {
		if service.Build == nil {
			continue
		}
		rel, inline, pathErr := dockerfilePath(exportDir, service.Build)
		if pathErr != nil {
			return nil, fmt.Errorf("service %s: dockerfile: %w", name, pathErr)
		}
		if !inline && !slices.Contains(paths, rel) {
			paths = append(paths, rel)
		}
	}
	slices.Sort(paths)
	return paths, nil
}

// dockerfilePath is the Dockerfile of a resolved build section, relative to
// the export at exportDir (slash form). inline is true for dockerfile_inline,
// which has no file. The path outside the export is an error.
func dockerfilePath(exportDir string, build map[string]any) (rel string, inline bool, err error) {
	if _, inline = build["dockerfile_inline"]; inline {
		return
	}
	dir, _ := build["context"].(string)
	name, _ := build["dockerfile"].(string)
	if name == "" {
		name = "Dockerfile"
	}
	path := name
	if !filepath.IsAbs(path) {
		path = filepath.Join(dir, name)
	}
	rel, err = relativeTo(exportDir, path)
	if err != nil {
		rel = ""
		return
	}
	rel = filepath.ToSlash(rel)
	return
}
