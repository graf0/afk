// Command afk runs AI coding agents against a Project's tickets.
package main

import (
	"fmt"
	"io"
	"os"

	"github.com/graf0/afk/config"
)

func main() {
	dir, err := os.Getwd()
	if err == nil {
		err = run(os.Args[1:], dir, os.Stdout)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "afk:", err)
		os.Exit(1)
	}
}

// run executes the command from args in the Project containing dir.
func run(args []string, dir string, stdout io.Writer) (err error) {
	cfg, err := config.Load(dir)
	if err != nil {
		err = fmt.Errorf("loading project: %w", err)
		return
	}
	if len(args) == 0 {
		return
	}
	switch args[0] {
	case "config":
		var data []byte
		data, err = cfg.Marshal()
		if err != nil {
			return
		}
		_, err = stdout.Write(data)
	default:
		err = fmt.Errorf("unknown command %q", args[0])
	}
	return
}
