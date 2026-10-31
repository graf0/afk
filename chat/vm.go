package chat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"image"
	_ "image/png" // the format of a screenshot
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/graf0/afk/agent"
	"github.com/graf0/afk/config"
)

// snapshotPattern is a snapshot name the Agent may use: it is an argument of
// virsh and a name in libvirt's metadata.
var snapshotPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)

// --- protocol ---

// keysRequest is the body of POST /vms/{vm}/keys.
type keysRequest struct {
	Keys []string `json:"keys"`
}

// typeRequest is the body of POST /vms/{vm}/type.
type typeRequest struct {
	Text string `json:"text"`
}

// pointRequest is the body of POST /vms/{vm}/click and /move: pixels of the
// last screenshot.
type pointRequest struct {
	X      int    `json:"x"`
	Y      int    `json:"y"`
	Button string `json:"button,omitempty"`
}

// snapshotRequest is the body of POST /vms/{vm}/snapshot and /revert.
type snapshotRequest struct {
	Name string `json:"name"`
}

// --- host: the test VM routes of the channel ---

// vmChannel is what the VM routes work on: the Project's [[vm]], libvirt, and
// the claims of this Chat.
type vmChannel struct {
	VMs    []config.VM
	Driver vmDriver
	Locks  *claims
}

// vmServer answers the Sandbox's `afk-cli vm` calls. A VM is claimed for
// the Chat by the first call that changes it, and stays so until release or
// the end of the process.
type vmServer struct {
	responder
	ch vmChannel

	mu    sync.Mutex
	sizes map[string]image.Point // domain -> size of its last screenshot
}

// vmRoutes is the test VM part of the channel.
func vmRoutes(ch vmChannel) (group routeGroup) {
	return func(mux *http.ServeMux, logger *log.Logger) {
		s := &vmServer{responder: responder{log: logger}, ch: ch, sizes: make(map[string]image.Point)}
		mux.HandleFunc("GET /vms", s.list)
		mux.HandleFunc("GET /vms/{vm}/screenshot", s.screenshot)
		mux.HandleFunc("POST /vms/{vm}/start", s.start)
		mux.HandleFunc("POST /vms/{vm}/shutdown", s.shutdown)
		mux.HandleFunc("POST /vms/{vm}/release", s.release)
		mux.HandleFunc("POST /vms/{vm}/keys", s.keys)
		mux.HandleFunc("POST /vms/{vm}/type", s.typeText)
		mux.HandleFunc("POST /vms/{vm}/click", s.click)
		mux.HandleFunc("POST /vms/{vm}/move", s.move)
		mux.HandleFunc("POST /vms/{vm}/snapshot", s.snapshot)
		mux.HandleFunc("POST /vms/{vm}/revert", s.revert)
	}
}

// domain is the VM of r's path if [[vm]] has it, and op names action on it; otherwise it answers 400 and
// ok is false. The name comes from the Agent: only a configured one is passed on.
func (s *vmServer) domain(w http.ResponseWriter, r *http.Request, action string) (vm config.VM, op string, ok bool) {
	op = "vm " + action + " " + strconv.Quote(r.PathValue("vm"))
	for _, candidate := range s.ch.VMs {
		if candidate.Name == r.PathValue("vm") {
			vm, op, ok = candidate, "vm "+action+" "+candidate.Name, true
			return
		}
	}
	names := make([]string, len(s.ch.VMs))
	for i, candidate := range s.ch.VMs {
		names[i] = candidate.Name
	}
	err := fmt.Errorf("unknown VM %q: [[vm]] has %s", r.PathValue("vm"), joinNames(names))
	s.reply(w, op, "", "", http.StatusBadRequest, err)
	return
}

func joinNames(names []string) (text string) {
	if len(names) == 0 {
		return "none"
	}
	return strings.Join(names, ", ")
}

// claim takes vm for the Chat; when another Chat holds it, it answers 409
// and ok is false.
func (s *vmServer) claim(w http.ResponseWriter, op string, vm config.VM) (ok bool) {
	err := s.ch.Locks.claim(vm.Name)
	if err == nil {
		return true
	}
	s.reply(w, op, "", "", lockStatus(err), err)
	return false
}

// lockStatus is the status of a failed claim or release: 409 for a VM another
// Chat holds, 500 for anything else.
func lockStatus(err error) (status int) {
	if errors.As(err, new(busyError)) {
		return http.StatusConflict
	}
	return http.StatusInternalServerError
}

// list serves `vm list`: a line per VM with its state, os, owner and address.
func (s *vmServer) list(w http.ResponseWriter, r *http.Request) {
	var b strings.Builder
	for _, vm := range s.ch.VMs {
		state, err := s.ch.Driver.State(r.Context(), vm.Name)
		if err != nil {
			state = "unknown"
		}
		owner, held := s.ch.Locks.ownerOf(vm.Name)
		if !held {
			owner = "-"
		}
		address := s.ch.Driver.Address(r.Context(), vm.Name)
		if address == "" {
			address = "-"
		}
		fmt.Fprintf(&b, "%s  %s  os=%s  user=%s  owner=%s  address=%s\n", vm.Name, state, vm.OS, vm.User, owner, address)
	}
	if b.Len() == 0 {
		b.WriteString("no test VMs in [[vm]] of .afk.toml\n")
	}
	s.reply(w, "vm list", agent.Sanitize(b.String()), "", http.StatusBadGateway, nil)
}

// screenshot serves `vm screenshot <vm>`: the PNG of the screen. It changes
// nothing, so it claims nothing; its size is what click and move scale by.
func (s *vmServer) screenshot(w http.ResponseWriter, r *http.Request) {
	vm, op, ok := s.domain(w, r, "screenshot")
	if !ok {
		return
	}
	png, err := s.ch.Driver.Screenshot(r.Context(), vm.Name)
	var size image.Config
	if err == nil {
		if size, _, err = image.DecodeConfig(bytes.NewReader(png)); err != nil {
			err = fmt.Errorf("the screenshot is not a PNG: %w", err)
		}
	}
	if err != nil {
		s.reply(w, op, "", "", http.StatusBadGateway, err)
		return
	}
	s.mu.Lock()
	s.sizes[vm.Name] = image.Pt(size.Width, size.Height)
	s.mu.Unlock()
	s.log.Printf("%s: ok", op)
	_ = jsonEncode(w, channelReply{Output: fmt.Sprintf("%dx%d PNG", size.Width, size.Height), Data: png})
}

// change claims vm and runs do: the shape of every operation that changes it.
func (s *vmServer) change(w http.ResponseWriter, r *http.Request, vm config.VM, op, output string, do func(ctx context.Context) (err error)) {
	if !s.claim(w, op, vm) {
		return
	}
	s.reply(w, op, output, "", http.StatusBadGateway, do(r.Context()))
}

func (s *vmServer) start(w http.ResponseWriter, r *http.Request) {
	vm, op, ok := s.domain(w, r, "start")
	if !ok {
		return
	}
	s.change(w, r, vm, op, "started "+vm.Name+"\n", func(ctx context.Context) (err error) { return s.ch.Driver.Start(ctx, vm.Name) })
}

func (s *vmServer) shutdown(w http.ResponseWriter, r *http.Request) {
	vm, op, ok := s.domain(w, r, "shutdown")
	if !ok {
		return
	}
	s.change(w, r, vm, op, "shutdown asked of "+vm.Name+" (ACPI)\n", func(ctx context.Context) (err error) { return s.ch.Driver.Shutdown(ctx, vm.Name) })
}

// release serves `vm release <vm>`: the claim of this Chat ends. A VM nobody
// holds is not an error; one another Chat holds is 409.
func (s *vmServer) release(w http.ResponseWriter, r *http.Request) {
	vm, op, ok := s.domain(w, r, "release")
	if !ok {
		return
	}
	held, err := s.ch.Locks.release(vm.Name)
	output := "released " + vm.Name + "\n"
	if !held {
		output = vm.Name + " was not held by this Chat\n"
	}
	s.reply(w, op, output, "", lockStatus(err), err)
}

func (s *vmServer) keys(w http.ResponseWriter, r *http.Request) {
	vm, op, ok := s.domain(w, r, "key")
	if !ok {
		return
	}
	var in keysRequest
	if !s.decode(w, r, op, &in) {
		return
	}
	qs, err := normalizeKeys(in.Keys)
	if err != nil {
		s.reply(w, op, "", "", http.StatusBadRequest, err)
		return
	}
	op += " " + strings.Join(qs, "+")
	s.change(w, r, vm, op, "", func(ctx context.Context) (err error) { return s.input(ctx, vm.Name, comboEvents(qs)) })
}

func (s *vmServer) typeText(w http.ResponseWriter, r *http.Request) {
	vm, op, ok := s.domain(w, r, "type")
	if !ok {
		return
	}
	var in typeRequest
	if !s.decode(w, r, op, &in) {
		return
	}
	if err := checkText(in.Text); err != nil {
		s.reply(w, op, "", "", http.StatusBadRequest, err)
		return
	}
	op += fmt.Sprintf(" %d chars", len(in.Text))
	s.change(w, r, vm, op, "", func(ctx context.Context) (err error) {
		commands, err := typeCommands(in.Text)
		for _, command := range commands {
			if err != nil {
				break
			}
			err = s.qmp(ctx, vm.Name, command)
		}
		return
	})
}

func (s *vmServer) click(w http.ResponseWriter, r *http.Request) { s.point(w, r, "click") }
func (s *vmServer) move(w http.ResponseWriter, r *http.Request)  { s.point(w, r, "move") }

// point serves click and move: pixels of the last screenshot become the
// absolute axes of QEMU. Without a screenshot there is no scale: 400.
func (s *vmServer) point(w http.ResponseWriter, r *http.Request, name string) {
	vm, op, ok := s.domain(w, r, name)
	if !ok {
		return
	}
	var in pointRequest
	if !s.decode(w, r, op, &in) {
		return
	}
	button := in.Button
	if name == "click" && button == "" {
		button = "left"
	}
	s.mu.Lock()
	size, known := s.sizes[vm.Name]
	s.mu.Unlock()
	var events []qmpEvent
	var err error
	switch {
	case name == "move" && button != "":
		err = errors.New("button: only click has one")
	case name == "click" && !mouseButtons[button]:
		err = fmt.Errorf("button %q: want left, right or middle", button)
	case !known:
		err = errors.New("no screenshot of this VM yet: take one, its size is the scale of the coordinates")
	default:
		events, err = pointEvents(in.X, in.Y, size.X, size.Y)
	}
	if err != nil {
		s.reply(w, op, "", "", http.StatusBadRequest, err)
		return
	}
	if name == "click" {
		events = append(events, clickEvents(button)...)
	}
	op += fmt.Sprintf(" (%d, %d)", in.X, in.Y)
	s.change(w, r, vm, op, "", func(ctx context.Context) (err error) { return s.input(ctx, vm.Name, events) })
}

func (s *vmServer) snapshot(w http.ResponseWriter, r *http.Request) {
	s.snapshotOp(w, r, "snapshot", func(ctx context.Context, domain, name string) (err error) {
		return s.ch.Driver.Snapshot(ctx, domain, name)
	})
}

func (s *vmServer) revert(w http.ResponseWriter, r *http.Request) {
	s.snapshotOp(w, r, "revert", func(ctx context.Context, domain, name string) (err error) {
		return s.ch.Driver.Revert(ctx, domain, name)
	})
}

// snapshotOp serves snapshot and revert: a name by snapshotPattern.
func (s *vmServer) snapshotOp(w http.ResponseWriter, r *http.Request, name string, do func(ctx context.Context, domain, name string) (err error)) {
	vm, op, ok := s.domain(w, r, name)
	if !ok {
		return
	}
	var in snapshotRequest
	if !s.decode(w, r, op, &in) {
		return
	}
	if !snapshotPattern.MatchString(in.Name) {
		s.reply(w, op, "", "", http.StatusBadRequest, fmt.Errorf("snapshot name %q: want %s", in.Name, snapshotPattern))
		return
	}
	op += " " + in.Name
	s.change(w, r, vm, op, name+" "+in.Name+" of "+vm.Name+" done\n", func(ctx context.Context) (err error) { return do(ctx, vm.Name, in.Name) })
}

// input sends events to the domain in one input-send-event command.
func (s *vmServer) input(ctx context.Context, domain string, events []qmpEvent) (err error) {
	command, err := qmpCommand(events)
	if err != nil {
		return
	}
	err = s.qmp(ctx, domain, command)
	return
}

// qmp sends one command and fails on QEMU's error reply.
func (s *vmServer) qmp(ctx context.Context, domain, command string) (err error) {
	reply, err := s.ch.Driver.QMP(ctx, domain, command)
	if err != nil {
		return
	}
	var parsed struct {
		Error *struct {
			Desc string `json:"desc"`
		} `json:"error"`
	}
	if json.Unmarshal([]byte(reply), &parsed) == nil && parsed.Error != nil {
		err = fmt.Errorf("qemu: %s", parsed.Error.Desc)
	}
	return
}

// --- Sandbox: afk-cli vm ---

// vmArg splits args into the VM name in front, as the path of the VM, and the rest.
func vmArg(args []string) (path string, rest []string, err error) {
	if len(args) == 0 || args[0] == "" {
		err = errCLIUsage
		return
	}
	path, rest = "/vms/"+url.PathEscape(args[0]), args[1:]
	return
}

// parseVMList parses `vm list`.
func parseVMList(args []string, _ io.Reader) (call cliCall, err error) {
	if len(args) != 0 {
		err = errCLIUsage
		return
	}
	call = cliCall{method: http.MethodGet, path: "/vms"}
	return
}

// vmAction parses a command that is `<vm>` alone: POST /vms/<vm>/<action>.
func vmAction(action string) (parse func(args []string, _ io.Reader) (call cliCall, err error)) {
	return func(args []string, _ io.Reader) (call cliCall, err error) {
		path, rest, err := vmArg(args)
		if err == nil && len(rest) != 0 {
			err = errCLIUsage
		}
		call = cliCall{method: http.MethodPost, path: path + "/" + action}
		return
	}
}

// parseVMScreenshot parses `vm screenshot <vm> [--file <path>]`: the PNG is
// saved in the Sandbox, by default in its temporary directory.
func parseVMScreenshot(args []string, _ io.Reader) (call cliCall, err error) {
	path, rest, err := vmArg(args)
	if err != nil {
		return
	}
	flags := flag.NewFlagSet("vm screenshot", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	file := flags.String("file", filepath.Join(os.TempDir(), "afk-screenshot-"+url.PathEscape(args[0])+".png"), "")
	if flags.Parse(rest) != nil || flags.NArg() != 0 || *file == "" {
		err = errCLIUsage
		return
	}
	call = cliCall{method: http.MethodGet, path: path + "/screenshot", saveTo: *file}
	return
}

// parseVMKey parses `vm key <vm> <combo>`: keys joined by +, such as ctrl+alt+delete.
func parseVMKey(args []string, _ io.Reader) (call cliCall, err error) {
	path, rest, err := vmArg(args)
	if err != nil || len(rest) != 1 {
		err = errCLIUsage
		return
	}
	call = cliCall{method: http.MethodPost, path: path + "/keys", body: keysRequest{Keys: strings.Split(rest[0], "+")}}
	return
}

// parseVMType parses `vm type <vm> <text>`; the text `-` is read from stdin.
func parseVMType(args []string, stdin io.Reader) (call cliCall, err error) {
	path, rest, err := vmArg(args)
	if err != nil || len(rest) != 1 {
		err = errCLIUsage
		return
	}
	text := rest[0]
	if text == "-" {
		var data []byte
		if data, err = io.ReadAll(stdin); err != nil {
			return
		}
		text = string(data)
	}
	call = cliCall{method: http.MethodPost, path: path + "/type", body: typeRequest{Text: text}}
	return
}

// parseVMPoint parses `vm click <vm> [--button <b>] <x> <y>` and `vm move <vm> <x> <y>`.
func parseVMPoint(action string) (parse func(args []string, _ io.Reader) (call cliCall, err error)) {
	return func(args []string, _ io.Reader) (call cliCall, err error) {
		path, rest, err := vmArg(args)
		if err != nil {
			return
		}
		req := pointRequest{}
		flags := flag.NewFlagSet("vm "+action, flag.ContinueOnError)
		flags.SetOutput(io.Discard)
		if action == "click" {
			flags.StringVar(&req.Button, "button", "", "")
		}
		if flags.Parse(rest) != nil || flags.NArg() != 2 {
			err = errCLIUsage
			return
		}
		var xErr, yErr error
		req.X, xErr = strconv.Atoi(flags.Arg(0))
		req.Y, yErr = strconv.Atoi(flags.Arg(1))
		if err = errors.Join(xErr, yErr); err != nil {
			err = errCLIUsage
			return
		}
		call = cliCall{method: http.MethodPost, path: path + "/" + action, body: req}
		return
	}
}

// parseVMSnapshot parses `vm snapshot <vm> <name>` and `vm revert <vm> <name>`.
func parseVMSnapshot(action string) (parse func(args []string, _ io.Reader) (call cliCall, err error)) {
	return func(args []string, _ io.Reader) (call cliCall, err error) {
		path, rest, err := vmArg(args)
		if err != nil || len(rest) != 1 {
			err = errCLIUsage
			return
		}
		call = cliCall{method: http.MethodPost, path: path + "/" + action, body: snapshotRequest{Name: rest[0]}}
		return
	}
}

// saveScreenshot writes the PNG of a reply to file, a line of the result to stdout.
func saveScreenshot(file string, reply channelReply, stdout io.Writer) (err error) {
	if len(reply.Data) == 0 {
		err = errors.New("the reply has no screenshot")
		return
	}
	if err = os.WriteFile(file, reply.Data, 0o644); err != nil {
		err = fmt.Errorf("saving the screenshot: %w", err)
		return
	}
	_, err = fmt.Fprintf(stdout, "%s (%s)\n", file, reply.Output)
	return
}
