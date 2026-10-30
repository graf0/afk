package run

import (
	"testing"

	"github.com/graf0/afk/forge"
)

func TestStackPaths(t *testing.T) {
	r := run{Options: Options{Ticket: forge.Ticket{Number: 7}, StateDir: "/state"}, project: "o-r"}
	if r.stackName() != "ticket-7" || r.stackDir() != "/state/o-r/runs/ticket-7" || r.treePath() != "/state/o-r/runs/ticket-7/repo" ||
		r.contextFile(ticketFileName) != "/state/o-r/runs/ticket-7/context/TICKET.md" {
		t.Errorf("name %q, dir %q, tree %q, ticket %q", r.stackName(), r.stackDir(), r.treePath(), r.contextFile(ticketFileName))
	}
	r.id = "preflight-ab12"
	if r.stackName() != "preflight-ab12" || r.stackDir() != "/state/o-r/runs/preflight-ab12" {
		t.Errorf("preflight: name %q, dir %q", r.stackName(), r.stackDir())
	}
}
