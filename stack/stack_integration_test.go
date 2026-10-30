//go:build integration

package stack

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Run manually on a host with rootless podman, its socket and docker-compose
// v2 (README, "Wymagania hosta"):
//
//	go test -tags integration -run Integration -v ./stack/
//
// The fixture needs a local base image with bash and sleep (default
// docker.io/library/golang:1.27; AFK_TEST_IMAGE overrides it). No Claude
// token and no network are needed: the Agent is not part of a Stack.

const (
	defaultTestImage = "docker.io/library/golang:1.27"
	fakeToken        = "fake-42"
)

const fixtureCompose = `services:
  svc:
    image: %[1]s
    command: ["sleep", "infinity"]
    ports: ["80"]
`

const fixtureComposeAfk = `services:
  afk:
    build:
      context: .
      dockerfile: Dockerfile.afk
    depends_on: [svc]
    volumes:
      - ${AFK_TREE}/masked
      - cache:/cache
      - cache:${HOME}/cache
  afk-setup:
    image: %[1]s
    depends_on: [svc]
    command: sh -c 'cat /t/seed >> /t/setup.log'
    volumes:
      - ${AFK_TREE}:/t
volumes:
  cache:
    name: ${AFK_CACHE}-cache
`

const fixtureDockerfile = "FROM %s\nRUN echo %s > /marker\n"

func TestIntegrationStack(t *testing.T) {
	image := os.Getenv("AFK_TEST_IMAGE")
	if image == "" {
		image = defaultTestImage
	}
	if err := exec.Command("podman", "image", "exists", image).Run(); err != nil {
		t.Skipf("test image %s is not available locally: %v", image, err)
	}
	project := fmt.Sprintf("afkt-%d", time.Now().UnixNano()%1_000_000_000)
	stateDir := t.TempDir()
	t.Cleanup(func() { removeTestImages(project) })

	repo, base := gitRepo(t, map[string]string{
		composeFile:      fmt.Sprintf(fixtureCompose, image),
		composeAfkFile:   fmt.Sprintf(fixtureComposeAfk, image),
		"Dockerfile.afk": fmt.Sprintf(fixtureDockerfile, image, "base"),
	})
	// An uncommitted edit of the working tree must not reach the Sandbox.
	if err := os.WriteFile(filepath.Join(repo, "Dockerfile.afk"), []byte(fmt.Sprintf(fixtureDockerfile, image, "evil")), 0o644); err != nil {
		t.Fatal(err)
	}

	newStack := func(t *testing.T, kind Kind, name, commit string, expose map[string][]string, out *bytes.Buffer) (s *Stack) {
		t.Helper()
		s, err := New(Options{
			Repo: repo, Commit: commit, Project: project, Kind: kind, Name: name, StateDir: stateDir,
			Env: map[string]string{"CLAUDE_CODE_OAUTH_TOKEN": fakeToken}, Expose: expose, Out: out,
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = s.Remove(context.Background()) })
		return
	}

	var volumes int // of the Chat, after its first Up
	chat := newStack(t, Chat, "plan", base, nil, &bytes.Buffer{})
	var upOut bytes.Buffer
	chat.opts.Out = &upOut

	t.Run("up: images from the base branch, services reachable, setup once", func(t *testing.T) {
		if err := chat.Up(t.Context()); err != nil {
			t.Fatalf("Up: %v\n%s", err, upOut.String())
		}
		if !strings.Contains(upOut.String(), "building images: afk") {
			t.Errorf("first Up did not build the Sandbox image:\n%s", upOut.String())
		}
		if got := execOut(t, chat, "cat /marker"); got != "base" {
			t.Errorf("/marker = %q, want base (the working tree says evil)", got)
		}
		if got := execOut(t, chat, "getent hosts svc | wc -l"); got != "1" {
			t.Errorf("service svc not resolvable from the Sandbox: %q", got)
		}
		if _, err := os.Stat(filepath.Join(chat.Tree(), "setup.log")); err == nil {
			t.Error("Up ran afk-setup: it is for Setup, after the clone is in the tree")
		}
	})

	t.Run("setup sees the tree, and runs once", func(t *testing.T) {
		if err := os.WriteFile(filepath.Join(chat.Tree(), "seed"), []byte("from the clone\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		for range 2 {
			if err := chat.Setup(t.Context()); err != nil {
				t.Fatalf("Setup: %v\n%s", err, upOut.String())
			}
		}
		if got := readLines(t, filepath.Join(chat.Tree(), "setup.log")); got != 1 {
			t.Errorf("setup ran %d times, want 1 (it reads the seed of the tree)", got)
		}
	})

	t.Run("files written in the Sandbox belong to the host user", func(t *testing.T) {
		execOut(t, chat, "touch written")
		info, err := os.Stat(filepath.Join(chat.Tree(), "written"))
		if err != nil {
			t.Fatal(err)
		}
		if uid := fileUID(t, info); uid != os.Getuid() {
			t.Errorf("owner uid = %d, want %d", uid, os.Getuid())
		}
	})

	t.Run("mask from AFK_TREE hides the directory from the host", func(t *testing.T) {
		execOut(t, chat, "touch masked/inside")
		if _, err := os.Stat(filepath.Join(chat.Tree(), "masked", "inside")); err == nil {
			t.Error("file written under the masked directory reached the host tree")
		}
	})

	t.Run("named volume is writable by the Agent", func(t *testing.T) {
		execOut(t, chat, "touch /cache/inside && touch ~/cache/inside")
	})

	t.Run("token: in the Sandbox environment, nowhere in the state", func(t *testing.T) {
		if got := execOut(t, chat, "echo $CLAUDE_CODE_OAUTH_TOKEN"); got != fakeToken {
			t.Errorf("token in the Sandbox = %q", got)
		}
		for _, dir := range []string{chat.Tree(), chat.Home()} {
			if out, _ := exec.Command("grep", "-rlF", fakeToken, dir).Output(); len(out) > 0 {
				t.Errorf("token on disk in %s: %s", dir, out)
			}
		}
		// Nothing of the Stack's state keeps the token: the env file lives only while it starts.
		if out, _ := exec.Command("grep", "-rlF", fakeToken, stateDir).Output(); len(out) > 0 {
			t.Errorf("token in the state directory: %s", out)
		}
	})

	t.Run("ports of compose.yml are not published without Expose", func(t *testing.T) {
		if got := podmanOut(t, "ps", "--format", "{{.Ports}}", "--filter", "label=com.docker.compose.project="+chat.ProjectName()); strings.Contains(got, "->") {
			t.Errorf("published ports: %q", got)
		}
	})

	t.Run("no podman socket in the Sandbox", func(t *testing.T) {
		got := execOut(t, chat, `printenv DOCKER_HOST CONTAINER_HOST; find / -xdev \( -name 'podman.sock' -o -name 'docker.sock' \) 2>/dev/null; true`)
		if got != "" {
			t.Errorf("Sandbox sees the socket: %q", got)
		}
	})

	t.Run("Down: containers and network gone, volume and state stay", func(t *testing.T) {
		volumes = countVolumes(t, chat)
		if err := chat.Down(t.Context()); err != nil {
			t.Fatal(err)
		}
		if got := podmanOut(t, "ps", "-a", "--quiet", "--filter", "label=com.docker.compose.project="+chat.ProjectName()); got != "" {
			t.Errorf("containers after Down: %q", got)
		}
		if got := podmanOut(t, "network", "ls", "--quiet", "--filter", "name="+chat.ProjectName()); got != "" {
			t.Errorf("network after Down: %q", got)
		}
		if got := podmanOut(t, "volume", "ls", "--quiet", "--filter", "name="+chat.cacheScope()+"-cache"); got == "" {
			t.Error("cache volume removed by Down")
		}
		if _, err := os.Stat(filepath.Join(chat.Tree(), "written")); err != nil {
			t.Errorf("state removed by Down: %v", err)
		}
	})

	t.Run("up again: no build, setup does not repeat", func(t *testing.T) {
		upOut.Reset()
		if err := chat.Up(t.Context()); err != nil {
			t.Fatalf("Up: %v\n%s", err, upOut.String())
		}
		if strings.Contains(upOut.String(), "building images") {
			t.Errorf("unchanged definition rebuilt:\n%s", upOut.String())
		}
		if err := chat.Setup(t.Context()); err != nil {
			t.Fatal(err)
		}
		if got := readLines(t, filepath.Join(chat.Tree(), "setup.log")); got != 1 {
			t.Errorf("setup ran %d times after restart, want 1", got)
		}
		if got := countVolumes(t, chat); got != volumes {
			t.Errorf("volumes after Up, Down, Up = %d, want %d: Down orphaned the masks", got, volumes)
		}
	})

	t.Run("run and chat of one Project run at once", func(t *testing.T) {
		run := newStack(t, Run, "ticket-1", base, nil, &bytes.Buffer{})
		if err := run.Up(t.Context()); err != nil {
			t.Fatal(err)
		}
		if run.ProjectName() == chat.ProjectName() {
			t.Fatal("same compose project")
		}
		if got := execOut(t, run, "cat /marker"); got != "base" {
			t.Errorf("Run /marker = %q", got)
		}
		if got := execOut(t, chat, "cat /marker"); got != "base" {
			t.Errorf("Chat /marker = %q", got)
		}
		// The cache of Runs is shared and outlives a Run; the Chat's is its own.
		if err := run.Remove(t.Context()); err != nil {
			t.Fatal(err)
		}
		if got := podmanOut(t, "volume", "ls", "--quiet", "--filter", "name="+run.cacheScope()+"-cache"); got == "" {
			t.Error("Remove of a Run deleted the cache of all Runs")
		}
		if _, err := os.Stat(run.dir); err == nil {
			t.Error("state of the Run survived Remove")
		}
		t.Cleanup(func() { _ = exec.Command("podman", "volume", "rm", "--force", run.cacheScope()+"-cache").Run() })
	})

	t.Run("changed Dockerfile on the base branch: new tag, build", func(t *testing.T) {
		gitIn(t, repo, "checkout", "--", "Dockerfile.afk") // drop the evil edit
		if err := os.WriteFile(filepath.Join(repo, "Dockerfile.afk"), []byte(fmt.Sprintf(fixtureDockerfile, image, "base2")), 0o644); err != nil {
			t.Fatal(err)
		}
		gitIn(t, repo, "add", "-A")
		gitIn(t, repo, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", "dockerfile")
		next := newStack(t, Chat, "other", gitIn(t, repo, "rev-parse", "HEAD"), nil, &bytes.Buffer{})
		var out bytes.Buffer
		next.opts.Out = &out
		if err := next.Up(t.Context()); err != nil {
			t.Fatalf("Up: %v\n%s", err, out.String())
		}
		if !strings.Contains(out.String(), "building images: afk") {
			t.Errorf("new Dockerfile did not build:\n%s", out.String())
		}
		if got := execOut(t, next, "cat /marker"); got != "base2" {
			t.Errorf("/marker = %q, want base2", got)
		}
		if tags := podmanOut(t, "images", "--noheading", "--filter", "reference=localhost/afk/"+project+"/afk"); strings.Count(tags, "\n") != 1 {
			t.Errorf("want two image tags for the Sandbox, got:\n%s", tags)
		}
		if err := next.Remove(t.Context()); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("expose: second Stack on a taken port fails", func(t *testing.T) {
		port := freePort(t)
		expose := map[string][]string{"svc": {fmt.Sprintf("%d:80", port)}}
		first := newStack(t, Chat, "web1", base, expose, &bytes.Buffer{})
		if err := first.Up(t.Context()); err != nil {
			t.Fatal(err)
		}
		second := newStack(t, Chat, "web2", base, expose, &bytes.Buffer{})
		err := second.Up(t.Context())
		if err == nil || !strings.Contains(err.Error(), fmt.Sprint(port)) {
			t.Errorf("err = %v, want a conflict naming port %d", err, port)
		}
	})

	t.Run("Remove: state, volumes and containers gone", func(t *testing.T) {
		if err := chat.Remove(t.Context()); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(chat.dir); err == nil {
			t.Error("state survived Remove")
		}
		if got := podmanOut(t, "volume", "ls", "--quiet", "--filter", "name="+chat.cacheScope()+"-cache"); got != "" {
			t.Errorf("volume after Remove: %q", got)
		}
		if got := podmanOut(t, "ps", "-a", "--quiet", "--filter", "label=com.docker.compose.project="+chat.ProjectName()); got != "" {
			t.Errorf("containers after Remove: %q", got)
		}
	})
}

// --- helpers ---

func execOut(t *testing.T, s *Stack, command string) (out string) {
	t.Helper()
	var buf bytes.Buffer
	if err := s.Exec(t.Context(), command, &buf); err != nil {
		t.Fatalf("Exec %q: %v", command, err)
	}
	return strings.TrimSpace(buf.String())
}

func countVolumes(t *testing.T, s *Stack) (n int) {
	t.Helper()
	return len(strings.Fields(podmanOut(t, "volume", "ls", "--quiet", "--filter", "label=com.docker.compose.project="+s.ProjectName())))
}

func podmanOut(t *testing.T, args ...string) (out string) {
	t.Helper()
	data, err := exec.Command("podman", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("podman %v: %v: %s", args, err, data)
	}
	return strings.TrimSpace(string(data))
}

func readLines(t *testing.T, path string) (n int) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(data), "\n")
}

func fileUID(t *testing.T, info os.FileInfo) (uid int) {
	t.Helper()
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatal("no unix stat")
	}
	return int(stat.Uid)
}

func freePort(t *testing.T) (port int) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// removeTestImages deletes the images the test built for project.
func removeTestImages(project string) {
	out, _ := exec.Command("podman", "images", "--quiet", "--filter", "reference=localhost/afk/"+project+"/*").Output()
	for _, id := range strings.Fields(string(out)) {
		_ = exec.Command("podman", "rmi", "--force", id).Run()
	}
}

// TestIntegrationTemplate starts a Stack from the written template alone: the
// Sandbox must come up with git, under the sleep infinity afk forces.
func TestIntegrationTemplate(t *testing.T) {
	const baseImage = "docker.io/library/debian:13"
	if out, err := exec.Command("podman", "pull", "--quiet", baseImage).CombinedOutput(); err != nil {
		t.Fatalf("podman pull %s: %v: %s", baseImage, err, out)
	}
	project := fmt.Sprintf("afkt-%d", time.Now().UnixNano()%1_000_000_000)
	t.Cleanup(func() { removeTestImages(project) })

	dir := t.TempDir()
	if err := WriteTemplate(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, composeFile), []byte("services: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, dir, "init", "-q", "-b", "main")
	gitIn(t, dir, "add", "-A")
	gitIn(t, dir, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", "x")
	commit := gitIn(t, dir, "rev-parse", "HEAD")

	var out bytes.Buffer
	s, err := New(Options{Repo: dir, Commit: commit, Project: project, Kind: Chat, Name: "tpl", StateDir: t.TempDir(), Out: &out})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Remove(context.Background()) })
	if err = s.Up(t.Context()); err != nil {
		t.Fatalf("Up: %v\n%s", err, out.String())
	}
	if got := execOut(t, s, "git --version"); !strings.HasPrefix(got, "git version") {
		t.Errorf("git --version = %q", got)
	}
}
