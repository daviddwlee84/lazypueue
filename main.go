package main

import (
	"context"
	"github.com/daviddwlee84/lazypueue/internal/scoopupgrade"
	"os"
	"os/signal"
	"syscall"

	"github.com/daviddwlee84/lazypueue/internal/cli"
	"github.com/daviddwlee84/lazypueue/internal/pueue"
	"github.com/daviddwlee84/lazypueue/internal/selfupdate"
)

var version = "dev"

func run() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	backend := pueue.New()
	defer backend.Close()
	return cli.Execute(ctx, cli.Options{Backend: backend, Version: selfupdate.Version(version)}, os.Args[1:])
}
func main() {
	if code, handled := scoopupgrade.HandleHelper(scoopupgrade.Product{Binary: "lazypueue", Module: "github.com/daviddwlee84/lazypueue", Main: "github.com/daviddwlee84/lazypueue"}); handled {
		os.Exit(code)
	}
	os.Exit(run())
}
