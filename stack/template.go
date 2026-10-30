package stack

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	buildContextDir = ".afk"
	dockerfileAfk   = buildContextDir + "/Dockerfile"
)

// composeAfkTemplate is the compose.afk.yml written for a Project without one.
//
// The image is tagged with the git tree of the build context, so the narrow
// context .afk changes the tag, and rebuilds the image, only when .afk changes.
const composeAfkTemplate = `# The Stack of a Chat or a Run is this file on top of compose.yml. Both are
# read from the base branch: commit and push changes before they take effect.
#
# The service "afk" is the Sandbox, where the Agent works. afk starts it with
# "sleep infinity" and an init process, so an ENTRYPOINT of the image must
# accept arguments.
services:
  afk:
    build:
      # Put in .afk only what the image needs: a wider context (e.g. ".")
      # rebuilds the image with every commit of the base branch.
      context: .afk
    # depends_on: [db]
    # volumes:
    #   # Mask: an anonymous volume under the tree hides what the host tree has
    #   # there (build output, dependencies of another OS). ${AFK_TREE} is the
    #   # tree of the Chat or Run, the same path on the host and in the Sandbox.
    #   - ${AFK_TREE}/node_modules
    #   # Cache: a named volume that survives the Stack. ${AFK_CACHE} is one
    #   # per Chat and one shared by all Runs of the Project.
    #   # The Agent's $HOME is the host's, so ${HOME} is the path in the Sandbox.
    #   - gomod:${HOME}/go/pkg/mod

  # afk-setup is optional: a one-shot service (e.g. seeding the database) that
  # runs on the first start of the Stack and when the Agent asks for a reset.
  # afk-setup:
  #   image: postgres:17
  #   depends_on: [db]
  #   command: ["psql", "-h", "db", "-f", "/seed.sql"]

# volumes:
#   gomod:
#     name: ${AFK_CACHE}-gomod

# Toolchain: install it in .afk/Dockerfile (the Sandbox image is built from it),
# not here. Services of the Project (database, server, frontend) belong in
# compose.yml.
`

// dockerfileTemplate is the .afk/Dockerfile written next to it.
const dockerfileTemplate = `FROM docker.io/library/debian:13

# The Sandbox has git, curl and TLS roots. Add the toolchain of the Project
# here (compilers, runtimes, package managers). afk runs the container with
# "sleep infinity", so do not rely on CMD.
RUN apt-get update \
    && apt-get install -y --no-install-recommends git ca-certificates curl \
    && rm -rf /var/lib/apt/lists/*
`

// ExistingTemplateFiles lists the files of the template that already exist in dir.
func ExistingTemplateFiles(dir string) (existing []string) {
	for _, name := range []string{composeAfkFile, dockerfileAfk} {
		if _, err := os.Lstat(filepath.Join(dir, name)); err == nil {
			existing = append(existing, name)
		}
	}
	return
}

// WriteTemplate writes compose.afk.yml and .afk/Dockerfile into dir, the
// working tree. It never overwrites: if either file exists it writes nothing.
// It does not commit.
func WriteTemplate(dir string) (err error) {
	if existing := ExistingTemplateFiles(dir); len(existing) > 0 {
		err = fmt.Errorf("%s already exists in %s and is not overwritten", strings.Join(existing, ", "), dir)
		return
	}
	files := []struct{ name, content string }{
		{composeAfkFile, composeAfkTemplate},
		{dockerfileAfk, dockerfileTemplate},
	}
	contextDir := filepath.Join(dir, buildContextDir)
	_, statErr := os.Stat(contextDir)
	var created []string
	defer func() {
		if err != nil {
			for _, path := range created {
				_ = os.Remove(path)
			}
			if statErr != nil {
				_ = os.Remove(contextDir) // only if we made it, and now empty
			}
		}
	}()
	if err = os.MkdirAll(contextDir, 0o755); err != nil {
		return
	}
	for _, f := range files {
		path := filepath.Join(dir, f.name)
		// O_EXCL: a file that appeared since the check is not ours to replace.
		var file *os.File
		if file, err = os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644); err == nil {
			created = append(created, path)
			_, err = file.WriteString(f.content)
			err = errors.Join(err, file.Close())
		}
		if err != nil {
			err = fmt.Errorf("writing %s: %w", f.name, err)
			return
		}
	}
	return
}
