package chat

import (
	"errors"
	"fmt"
	"maps"
	"net"
	"slices"
	"strings"
	"syscall"

	"github.com/graf0/afk/stack"
)

// exposeLocksName is the directory in the state (not the Project's: a host
// port belongs to the host) with the claims of Chats on published ports.
const exposeLocksName = "expose-locks"

// exposeSpecs is [expose] as the Stack takes it. A Chat started with
// --expose and nothing to expose is a mistake worth saying.
func exposeSpecs(expose map[string]string) (specs map[string][]string, err error) {
	if len(expose) == 0 {
		err = errors.New("--expose: .afk.toml on the base branch has no [expose]")
		return
	}
	specs = make(map[string][]string, len(expose))
	for service, spec := range expose {
		specs[service] = []string{spec}
	}
	return
}

// claimPorts takes the host ports of [expose] for this Chat, then checks that
// nothing else listens on them. A port another Chat holds is an error naming
// that Chat. A claim is keyed by the port alone, whatever the address: the
// ports of a Project are fixed, so one Chat at a time publishes them.
func claimPorts(locks *claims, expose map[string]string) (err error) {
	type endpoint struct{ address, port string }
	var endpoints []endpoint // a host port is in one service (config.Parse)
	for _, service := range slices.Sorted(maps.Keys(expose)) {
		var published string
		if published, err = stack.ParseExpose(expose[service]); err != nil {
			return
		}
		parts := strings.Split(published, ":") // address:host_port:container_port
		endpoints = append(endpoints, endpoint{parts[0], parts[1]})
	}
	for _, e := range endpoints {
		if claimErr := locks.claim(e.port); claimErr != nil {
			err = claimErr
			if _, ok := errors.AsType[busyError](claimErr); ok {
				err = fmt.Errorf("%w: end it first, or start this Chat without --expose", claimErr)
			}
			return
		}
	}
	for _, e := range endpoints {
		// Only a hint: whatever takes the port between here and Up fails Up.
		listener, listenErr := net.Listen("tcp", net.JoinHostPort(e.address, e.port))
		switch {
		case errors.Is(listenErr, syscall.EADDRINUSE):
			err = fmt.Errorf("port %s on %s is in use by something else: %w", e.port, e.address, listenErr)
			return
		case listenErr != nil:
			err = fmt.Errorf("checking port %s on %s: %w", e.port, e.address, listenErr)
			return
		}
		_ = listener.Close()
	}
	return
}
