package main

import (
	"context"
	"os"
	"os/signal"

	"github.com/daviddwlee84/lazypueue/internal/cli"
	"github.com/daviddwlee84/lazypueue/internal/pueue"
)

var version = "dev"

func run() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	backend := pueue.New()
	defer backend.Close()
	return cli.Execute(ctx, cli.Options{Backend: backend, Version: version}, os.Args[1:])
}
func main() { os.Exit(run()) }
