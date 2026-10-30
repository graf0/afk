package stack

import (
	"slices"
	"strings"
	"testing"
)

func TestHostEnv(t *testing.T) {
	got := hostEnv([]string{"PATH=/bin", "COMPOSE_FILE=/x", "DOCKER_HOST=tcp://evil", "DOCKER_CONFIG=/c", "CONTAINER_HOST=x", "HOME=/h"})
	if want := []string{"PATH=/bin", "HOME=/h"}; !slices.Equal(got, want) {
		t.Errorf("hostEnv = %v, want %v", got, want)
	}
}

func TestRejectSocketMounts(t *testing.T) {
	const socket = "/run/user/1000/podman/podman.sock"
	for _, source := range []string{socket, "/run/user/1000/podman", "/run/user/1000", "/"} {
		if err := rejectSocketMounts(socket, map[string][]string{"afk": {source}}); err == nil {
			t.Errorf("mount of %s accepted", source)
		}
	}
	for _, source := range []string{"/home/u/tree", "/run/user/1000/other", "", "named-volume"} {
		if err := rejectSocketMounts(socket, map[string][]string{"afk": {source}}); err != nil {
			t.Errorf("mount of %q rejected: %v", source, err)
		}
	}
}

func TestTailBuffer(t *testing.T) {
	tail := &tailBuffer{max: 4}
	n, err := tail.Write([]byte("abcdef"))
	if n != 6 || err != nil || tail.String() != "cdef" {
		t.Errorf("Write = %d, %v, tail %q", n, err, tail.String())
	}
	if _, err = tail.Write([]byte("gh")); err != nil || !strings.HasSuffix("efgh", tail.String()) || tail.String() != "efgh" {
		t.Errorf("tail = %q", tail.String())
	}
}
