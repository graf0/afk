// Command afk runs AI coding agents against a Project's tickets.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/graf0/afk/config"
	"github.com/graf0/afk/image"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	dir, err := os.Getwd()
	if err == nil {
		err = run(ctx, os.Args[1:], dir, os.Stdout)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "afk:", err)
		os.Exit(1)
	}
}

// run executes the command from args in the Project containing dir.
func run(ctx context.Context, args []string, dir string, stdout io.Writer) (err error) {
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
	case "image":
		err = runImage(ctx, args[1:], cfg, stdout)
	default:
		err = fmt.Errorf("unknown command %q", args[0])
	}
	return
}

// runImage executes `afk image <subcommand>`.
func runImage(ctx context.Context, args []string, cfg config.Config, stdout io.Writer) (err error) {
	if len(args) != 1 || args[0] != "build" {
		err = fmt.Errorf("usage: afk image build")
		return
	}
	paths, err := image.DefaultPaths()
	if err != nil {
		return
	}
	_, err = image.Build(ctx, image.Options{Paths: paths, Config: cfg, Out: stdout})
	return
}
