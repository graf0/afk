package stack

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"net/netip"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

const (
	// loopback is the address of a published port without one.
	loopback = "127.0.0.1"
	// tagHashLen is the length of the hash in an image tag.
	tagHashLen = 12
)

// Mount is a bind mount into the Sandbox, in addition to the ones the Stack
// always gives it (the tree and the Agent's $HOME).
type Mount struct {
	Source   string
	Target   string
	ReadOnly bool
	Dir      bool // a directory: the mount point in the Agent's home is made as one
}

// overrideInput is what the override of afk adds to the Project's files.
type overrideInput struct {
	Tree     string
	Home     string // the Agent's $HOME on the host
	HostHome string // the path it appears under in the Sandbox
	EnvFile  string
	Mounts   []Mount
	Ports    map[string][]string // service -> compose port specs; every service has an entry (maybe empty)
	Images   map[string]string   // service with build: -> tag of its image
}

// ParseExpose turns [address:]host_port:container_port into a compose port
// spec, with the loopback address by default.
func ParseExpose(spec string) (port string, err error) {
	parts := strings.Split(spec, ":")
	addr := loopback
	switch len(parts) {
	case 2:
	case 3:
		addr, parts = parts[0], parts[1:]
		if _, err = netip.ParseAddr(addr); err != nil {
			err = fmt.Errorf("expose %q: address: %w", spec, err)
			return
		}
	default:
		err = fmt.Errorf("expose %q: want [address:]host_port:container_port", spec)
		return
	}
	for _, p := range parts {
		if n, convErr := strconv.Atoi(p); convErr != nil || n < 1 || n > 65535 {
			err = fmt.Errorf("expose %q: port %q is not 1-65535", spec, p)
			return
		}
	}
	port = addr + ":" + parts[0] + ":" + parts[1]
	return
}

// imageTag is the tag of the image of a service with build:, from the hash
// of its definition: the build section, the git tree of the build context
// and, when it lies outside the context, the Dockerfile's blob (contextTree and
// dockerfileBlob come from the base commit). The tag does not depend on the
// Stack, so every Chat and Run of the Project shares the image.
func imageTag(project, service, export string, build map[string]any, contextTree, dockerfileBlob string) (tag string, err error) {
	definition := make(map[string]any, len(build))
	for k, v := range build {
		definition[k] = v
	}
	if dir, ok := build["context"].(string); ok {
		var rel string
		if rel, err = relativeTo(export, dir); err != nil {
			err = fmt.Errorf("service %s: build context: %w", service, err)
			return
		}
		definition["context"] = rel
	}
	data, err := json.Marshal(definition) // maps marshal sorted: stable
	if err != nil {
		return
	}
	sum := sha256.New()
	for _, part := range [][]byte{data, []byte(contextTree), []byte(dockerfileBlob)} {
		sum.Write(part)
		sum.Write([]byte{0})
	}
	hash := hex.EncodeToString(sum.Sum(nil))[:tagHashLen]
	// "/" cannot appear in a project name: tags of two Projects never collide.
	tag = fmt.Sprintf("localhost/afk/%s/%s:%s", project, strings.ToLower(service), hash)
	return
}

// relativeTo returns path relative to dir, or an error when it is not inside.
func relativeTo(dir, path string) (rel string, err error) {
	rel, err = filepath.Rel(dir, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		err = fmt.Errorf("%s is outside the base branch export", path)
	}
	return
}

// overrideFile renders the third compose file of a Stack. It is YAML written
// by hand: strings and flow collections encoded as JSON are valid YAML, and
// the `!override` tag on ports (compose 2.24+) replaces the ports of the
// Project's files instead of adding to them. Paths and values are escaped for
// compose's interpolation.
func overrideFile(in overrideInput) (data []byte, err error) {
	quote := func(v any) (s string) {
		encoded, _ := json.Marshal(v) // strings, string slices and maps cannot fail
		return string(encoded)
	}
	bind := func(source, target string, readOnly bool) (volume string) {
		return quote(map[string]any{"type": "bind", "source": escape(source), "target": escape(target), "read_only": readOnly})
	}
	names := map[string]bool{SandboxService: true}
	for name := range in.Images {
		names[name] = true
	}
	for name := range in.Ports {
		names[name] = true
	}
	var out strings.Builder
	out.WriteString("services:\n")
	for _, name := range slices.Sorted(maps.Keys(names)) {
		fmt.Fprintf(&out, "  %s:\n", quote(name))
		if tag, ok := in.Images[name]; ok {
			fmt.Fprintf(&out, "    image: %s\n", quote(tag))
		}
		// Ports of every service are replaced: only [expose] publishes anything.
		if ports, ok := in.Ports[name]; ok {
			fmt.Fprintf(&out, "    ports: !override %s\n", quote(append([]string{}, ports...)))
		}
		if name != SandboxService {
			continue
		}
		// The Sandbox is a place to exec into, whatever its image runs.
		out.WriteString("    userns_mode: keep-id\n") // files in the tree belong to the host user
		out.WriteString("    init: true\n")
		out.WriteString("    command: [\"sleep\", \"infinity\"]\n")
		fmt.Fprintf(&out, "    working_dir: %s\n", quote(escape(in.Tree)))
		// Absent after Up: the token is on disk only while the Stack starts.
		fmt.Fprintf(&out, "    env_file: [%s]\n", quote(map[string]any{"path": escape(in.EnvFile), "required": false}))
		fmt.Fprintf(&out, "    environment: %s\n", quote(map[string]string{"HOME": escape(in.HostHome)}))
		out.WriteString("    volumes:\n")
		volumes := []string{bind(in.Tree, in.Tree, false), bind(in.Home, in.HostHome, false)}
		for _, m := range in.Mounts {
			volumes = append(volumes, bind(m.Source, m.Target, m.ReadOnly))
		}
		for _, v := range volumes {
			fmt.Fprintf(&out, "      - %s\n", v)
		}
	}
	data = []byte(out.String())
	return
}

// escape protects s from compose's variable interpolation.
func escape(s string) (escaped string) {
	return strings.ReplaceAll(s, "$", "$$")
}
