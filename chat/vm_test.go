package chat

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/graf0/afk/config"
)

// fakeVM is a vmDriver that records what it is asked.
type fakeVM struct {
	mu      sync.Mutex
	calls   []string
	state   string
	width   int
	height  int
	notPNG  bool
	qmpErr  string // the QMP reply of a failure
	failing bool
}

func (f *fakeVM) record(call string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, call)
}

func (f *fakeVM) State(_ context.Context, domain string) (state string, err error) {
	return f.state, nil
}

func (f *fakeVM) Address(_ context.Context, domain string) (address string) { return "192.168.122.10" }

func (f *fakeVM) Start(_ context.Context, domain string) (err error) {
	f.record("start " + domain)
	if f.failing {
		err = errors.New("libvirt: no")
	}
	return
}

func (f *fakeVM) Shutdown(_ context.Context, domain string) (err error) {
	f.record("shutdown " + domain)
	return
}

func (f *fakeVM) Screenshot(_ context.Context, domain string) (data []byte, err error) {
	f.record("screenshot " + domain)
	if f.notPNG {
		return []byte("P6 not a png"), nil
	}
	var buf bytes.Buffer
	err = png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, f.width, f.height)))
	return buf.Bytes(), err
}

func (f *fakeVM) QMP(_ context.Context, domain, command string) (reply string, err error) {
	f.record("qmp " + domain + " " + command)
	if f.qmpErr != "" {
		return `{"error":{"class":"GenericError","desc":"` + f.qmpErr + `"}}`, nil
	}
	return `{"return":{}}`, nil
}

func (f *fakeVM) Snapshot(_ context.Context, domain, name string) (err error) {
	f.record("snapshot " + domain + " " + name)
	return
}

func (f *fakeVM) Revert(_ context.Context, domain, name string) (err error) {
	f.record("revert " + domain + " " + name)
	return
}

// vmFixture is a Chat's channel with the VM routes of two VMs and a fake
// libvirt; other(…) is the channel of a second Chat on the same locks directory.
type vmFixture struct {
	t      *testing.T
	socket string
	drv    *fakeVM
	locks  *claims
	dir    string
}

var testVMs = []config.VM{{Name: "win11", User: "admin", OS: "windows"}, {Name: "deb", User: "tester", OS: "linux"}}

func newVMFixture(t *testing.T, dir, chat string) (fx *vmFixture) {
	t.Helper()
	fx = &vmFixture{t: t, drv: &fakeVM{state: "running", width: 1000, height: 500}, dir: dir}
	fx.locks = newClaims(dir, claimVM, chat)
	t.Cleanup(fx.locks.close)
	fx.socket = filepath.Join(t.TempDir(), "afk.sock")
	stop, err := serveChannel(t.Context(), fx.socket, io.Discard, vmRoutes(vmChannel{VMs: testVMs, Driver: fx.drv, Locks: fx.locks}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	return
}

func (fx *vmFixture) call(args ...string) (out string, err error) {
	fx.t.Helper()
	var stdout bytes.Buffer
	err = RunCLI(fx.t.Context(), fx.socket, args, strings.NewReader(""), &stdout, io.Discard)
	return stdout.String(), err
}

func (fx *vmFixture) mustCall(args ...string) (out string) {
	fx.t.Helper()
	out, err := fx.call(args...)
	if err != nil {
		fx.t.Fatalf("afk-ai %v: %v", args, err)
	}
	return
}

// status is the HTTP status of a raw request to the VM routes.
func (fx *vmFixture) status(method, path, body string) (code int) {
	rec := httptest.NewRecorder()
	channelHandler(io.Discard, vmRoutes(vmChannel{VMs: testVMs, Driver: fx.drv, Locks: fx.locks})).
		ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
	return rec.Code
}

func (fx *vmFixture) qmps() (commands []string) { return fx.drv.qmps() }

func (f *fakeVM) qmps() (commands []string) {
	for _, call := range f.calls {
		if command, ok := strings.CutPrefix(call, "qmp win11 "); ok {
			commands = append(commands, command)
		}
	}
	return
}

func TestVMList(t *testing.T) {
	dir := t.TempDir()
	fx := newVMFixture(t, dir, "plan")
	other := newVMFixture(t, dir, "other")
	out := fx.mustCall("vm", "list")
	for _, want := range []string{"win11  running  os=windows  user=admin  owner=-  address=192.168.122.10", "deb  running  os=linux"} {
		if !strings.Contains(out, want) {
			t.Errorf("list lacks %q:\n%s", want, out)
		}
	}
	other.mustCall("vm", "start", "win11")
	if out = fx.mustCall("vm", "list"); !strings.Contains(out, "win11  running  os=windows  user=admin  owner=other") {
		t.Errorf("list does not show the owner:\n%s", out)
	}
}

func TestVMListEmpty(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "afk.sock")
	stop, err := serveChannel(t.Context(), socket, io.Discard, vmRoutes(vmChannel{Driver: &fakeVM{}, Locks: newClaims(t.TempDir(), claimVM, "plan")}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	var out bytes.Buffer
	if err = RunCLI(t.Context(), socket, []string{"vm", "list"}, strings.NewReader(""), &out, io.Discard); err != nil || !strings.Contains(out.String(), "no test VMs") {
		t.Errorf("list = %q, %v", out.String(), err)
	}
}

func TestVMOnlyConfigured(t *testing.T) {
	fx := newVMFixture(t, t.TempDir(), "plan")
	for _, path := range []string{"start", "shutdown", "release", "keys", "type", "click", "move", "snapshot", "revert"} {
		if code := fx.status("POST", "/vms/other/"+path, `{}`); code != http.StatusBadRequest {
			t.Errorf("POST %s on an unknown VM = %d, want 400", path, code)
		}
	}
	if code := fx.status("GET", "/vms/other/screenshot", ""); code != http.StatusBadRequest {
		t.Errorf("screenshot of an unknown VM = %d, want 400", code)
	}
	_, err := fx.call("vm", "start", "--domain-flag")
	if err == nil || !strings.Contains(err.Error(), "unknown VM") || !strings.Contains(err.Error(), "win11, deb") {
		t.Errorf("err = %v, want the unknown VM and the known ones", err)
	}
	if len(fx.drv.calls) != 0 {
		t.Errorf("libvirt was called: %q", fx.drv.calls)
	}
}

func TestVMStartShutdown(t *testing.T) {
	fx := newVMFixture(t, t.TempDir(), "plan")
	fx.mustCall("vm", "start", "win11")
	fx.mustCall("vm", "shutdown", "win11")
	if want := []string{"start win11", "shutdown win11"}; !slices.Equal(fx.drv.calls, want) {
		t.Errorf("calls = %q, want %q", fx.drv.calls, want)
	}
	fx.drv.failing = true
	if _, err := fx.call("vm", "start", "deb"); err == nil || !strings.Contains(err.Error(), "libvirt: no") {
		t.Errorf("err = %v", err)
	}
}

func TestVMLockAcrossChats(t *testing.T) {
	dir := t.TempDir()
	a, b := newVMFixture(t, dir, "alpha"), newVMFixture(t, dir, "beta")
	a.mustCall("vm", "start", "win11")
	// Every changing operation of another Chat is refused with 409; nothing reaches libvirt.
	for _, path := range []string{"start", "shutdown", "keys", "type", "snapshot", "revert"} {
		body := `{"keys":["a"],"text":"x","x":1,"y":1,"name":"clean"}`
		if code := b.status("POST", "/vms/win11/"+path, body); code != http.StatusConflict {
			t.Errorf("POST %s by another Chat = %d, want 409", path, code)
		}
	}
	if code := b.status("POST", "/vms/win11/release", ""); code != http.StatusConflict {
		t.Errorf("release by another Chat = %d, want 409", code)
	}
	b.mustCall("vm", "screenshot", "win11", "--file", filepath.Join(t.TempDir(), "s.png"))
	for _, args := range [][]string{{"vm", "click", "win11", "1", "1"}, {"vm", "move", "win11", "1", "1"}} {
		if _, err := b.call(args...); err == nil || !strings.Contains(err.Error(), "held by Chat alpha") {
			t.Errorf("%v by another Chat: err = %v, want busy", args, err)
		}
	}
	if len(b.drv.qmps()) != 0 {
		t.Errorf("the other Chat reached QEMU: %q", b.drv.calls)
	}
	_, err := b.call("vm", "shutdown", "win11")
	if err == nil || !strings.Contains(err.Error(), "held by Chat alpha") {
		t.Errorf("err = %v, want the owner named", err)
	}
	// Another VM is free; the owner keeps working; screenshot and list never claim.
	b.mustCall("vm", "start", "deb")
	a.mustCall("vm", "shutdown", "win11")
	// release frees it, and the other Chat takes it.
	a.mustCall("vm", "release", "win11")
	b.mustCall("vm", "start", "win11")
	// The end of the process frees it too.
	b.locks.close()
	a.mustCall("vm", "start", "win11")
}

func TestVMBadRequestClaimsNothing(t *testing.T) {
	dir := t.TempDir()
	a, b := newVMFixture(t, dir, "alpha"), newVMFixture(t, dir, "beta")
	if _, err := a.call("vm", "key", "win11", "ctrl+bogus"); err == nil {
		t.Fatal("bogus key accepted")
	}
	b.mustCall("vm", "start", "win11") // alpha's invalid call claimed nothing
}

func TestVMRelease(t *testing.T) {
	fx := newVMFixture(t, t.TempDir(), "plan")
	if out := fx.mustCall("vm", "release", "win11"); !strings.Contains(out, "was not held") {
		t.Errorf("release of a free VM = %q", out)
	}
	fx.mustCall("vm", "start", "win11")
	if out := fx.mustCall("vm", "release", "win11"); !strings.Contains(out, "released") {
		t.Errorf("release = %q", out)
	}
}

func TestVMKey(t *testing.T) {
	fx := newVMFixture(t, t.TempDir(), "plan")
	fx.mustCall("vm", "key", "win11", "Ctrl+Alt+Del")
	commands := fx.qmps()
	if len(commands) != 1 {
		t.Fatalf("qmp = %q", commands)
	}
	want := []string{"ctrl:down", "alt:down", "delete:down", "delete:up", "alt:up", "ctrl:up"}
	if got := decodeEvents(t, commands[0]); !slices.Equal(got, want) {
		t.Errorf("events = %v, want %v", got, want)
	}
	for name, args := range map[string][]string{
		"unknown key": {"vm", "key", "win11", "ctrl+poweroff"},
		"too long":    {"vm", "key", "win11", "a+b+c+d+e+f+g"},
		"empty key":   {"vm", "key", "win11", "ctrl++a"},
		"no combo":    {"vm", "key", "win11"},
	} {
		if _, err := fx.call(args...); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
	if len(fx.qmps()) != 1 {
		t.Errorf("a refused combination reached QEMU: %q", fx.qmps())
	}
}

func TestVMQMPError(t *testing.T) {
	fx := newVMFixture(t, t.TempDir(), "plan")
	fx.drv.qmpErr = "no such device"
	if _, err := fx.call("vm", "key", "win11", "a"); err == nil || !strings.Contains(err.Error(), "no such device") {
		t.Errorf("err = %v, want QEMU's error", err)
	}
}

func TestVMType(t *testing.T) {
	fx := newVMFixture(t, t.TempDir(), "plan")
	fx.mustCall("vm", "type", "win11", "Hi!")
	commands := fx.qmps()
	if len(commands) != 1 {
		t.Fatalf("qmp = %q", commands)
	}
	if got := decodeEvents(t, commands[0]); len(got) != 10 || got[0] != "shift:down" || got[1] != "h:down" {
		t.Errorf("events = %v", got)
	}
	for name, text := range map[string]string{"non-ASCII": "zażółć", "too long": strings.Repeat("a", maxTypeChars+1), "empty": ""} {
		if _, err := fx.call("vm", "type", "win11", text); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
	if len(fx.qmps()) != 1 {
		t.Errorf("refused text reached QEMU: %q", fx.qmps())
	}
}

func TestVMTypeStdin(t *testing.T) {
	fx := newVMFixture(t, t.TempDir(), "plan")
	var out bytes.Buffer
	if err := RunCLI(t.Context(), fx.socket, []string{"vm", "type", "win11", "-"}, strings.NewReader("ab"), &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	if got := decodeEvents(t, fx.qmps()[0]); len(got) != 4 {
		t.Errorf("events = %v", got)
	}
}

func TestVMScreenshot(t *testing.T) {
	fx := newVMFixture(t, t.TempDir(), "plan")
	file := filepath.Join(t.TempDir(), "shot.png")
	out := fx.mustCall("vm", "screenshot", "win11", "--file", file)
	if !strings.Contains(out, file) || !strings.Contains(out, "1000x500") {
		t.Errorf("out = %q, want the file and the size", out)
	}
	f, err := os.Open(file)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if img, err := png.Decode(f); err != nil || img.Bounds().Dx() != 1000 {
		t.Errorf("saved file: %v", err)
	}
	fx.drv.notPNG = true
	if _, err = fx.call("vm", "screenshot", "win11", "--file", file); err == nil {
		t.Error("a screenshot that is not a PNG was accepted")
	}
}

func TestVMClickAndMove(t *testing.T) {
	fx := newVMFixture(t, t.TempDir(), "plan")
	// Without a screenshot there is no scale.
	if _, err := fx.call("vm", "click", "win11", "1", "1"); err == nil || !strings.Contains(err.Error(), "screenshot") {
		t.Errorf("click before a screenshot: err = %v", err)
	}
	fx.mustCall("vm", "screenshot", "win11", "--file", filepath.Join(t.TempDir(), "s.png")) // 1000x500
	fx.mustCall("vm", "click", "win11", "999", "499")
	fx.mustCall("vm", "click", "win11", "--button", "right", "0", "0")
	fx.mustCall("vm", "move", "win11", "500", "250")
	commands := fx.qmps()
	if len(commands) != 3 {
		t.Fatalf("qmp = %q", commands)
	}
	wants := [][]string{
		{"x=32767", "y=32767", "left:down", "left:up"},
		{"x=0", "y=0", "right:down", "right:up"},
		{"x=16400", "y=16416"}, // the middle of 1000x500: each axis scales by its own size
	}
	for i, want := range wants {
		got := decodeEvents(t, commands[i])
		if !slices.Equal(got, want) {
			t.Errorf("command %d events = %v, want %v", i, got, want)
		}
	}
	for name, args := range map[string][]string{
		"x outside":      {"vm", "click", "win11", "1000", "0"},
		"y outside":      {"vm", "click", "win11", "0", "500"},
		"negative":       {"vm", "move", "win11", "-1", "0"},
		"bad button":     {"vm", "click", "win11", "--button", "back", "1", "1"},
		"not a number":   {"vm", "click", "win11", "a", "1"},
		"button on move": {"vm", "move", "win11", "--button", "left", "1", "1"},
	} {
		if _, err := fx.call(args...); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
	if len(fx.qmps()) != 3 {
		t.Errorf("a refused point reached QEMU: %q", fx.qmps())
	}
}

func TestVMSnapshotAndRevert(t *testing.T) {
	fx := newVMFixture(t, t.TempDir(), "plan")
	fx.mustCall("vm", "snapshot", "win11", "clean-1.0")
	fx.mustCall("vm", "revert", "win11", "clean-1.0")
	if want := []string{"snapshot win11 clean-1.0", "revert win11 clean-1.0"}; !slices.Equal(fx.drv.calls, want) {
		t.Errorf("calls = %q, want %q", fx.drv.calls, want)
	}
	for _, name := range []string{"", "-rf", "a b", "a/b", "../x", "a;b", "-", strings.Repeat("a", 65), "zażółć"} {
		for _, action := range []string{"snapshot", "revert"} {
			if _, err := fx.call("vm", action, "win11", name); err == nil {
				t.Errorf("%s %q: want an error", action, name)
			}
		}
	}
	if len(fx.drv.calls) != 2 {
		t.Errorf("a bad snapshot name reached libvirt: %q", fx.drv.calls)
	}
}

func TestVMUsage(t *testing.T) {
	fx := newVMFixture(t, t.TempDir(), "plan")
	for _, args := range [][]string{
		{"vm"}, {"vm", "list", "x"}, {"vm", "start"}, {"vm", "start", "a", "b"}, {"vm", "screenshot", "win11", "--x"},
		{"vm", "click", "win11", "1"}, {"vm", "snapshot", "win11"}, {"vm", "nope"},
	} {
		if _, err := fx.call(args...); !errors.Is(err, errCLIUsage) {
			t.Errorf("%v: err = %v, want usage", args, err)
		}
	}
}
