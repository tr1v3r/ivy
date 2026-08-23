// Package web exposes the ivy engine over HTTP.
//
// It wires a gin router with the rule-query API (GET /api/v1/rule passes
// URL query params into the tree's RealizeContext so param-aware
// processors can differentiate per request), a health-check ping, and
// placeholder routes for future management endpoints. Serve runs the
// HTTP server with graceful shutdown; InitForest/RefreshForest manage the
// engine's forest lifecycle for the server process.
package web

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/tr1v3r/pkg/guard"
	"github.com/tr1v3r/pkg/log"

	"github.com/tr1v3r/ivy"
	"github.com/tr1v3r/ivy/driver"
)

const treeName = "default"

var f ivy.Forest

// Serve starts the HTTP server on :8080 and blocks until a shutdown
// signal arrives, then drains in-flight requests within timeout.
func Serve(timeout time.Duration, handler http.Handler) {
	srv := &http.Server{
		Addr:    ":8080",
		Handler: handler,
	}

	go func() { // service connections
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal("listen: %s", err)
		}
	}()

	// wait for shutdown signal
	<-guard.Cancel()
	log.Info("Shutdown Server ...")

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	err := srv.Shutdown(ctx)
	cancel() // run before any Fatal exit below
	if err != nil {
		log.Fatal("Server Shutdown:", err)
	}
	// catching ctx.Done(). timeout of 5 seconds.
	select {
	case <-ctx.Done():
		log.Error("timeout of %s.", timeout)
	default:
		log.Info("work done.")
	}
	log.Info("Server exiting")
}

// InitForest builds the process-wide forest from the given tree builders.
// Call once during startup, before serving requests.
func InitForest(builders ...ivy.TreeBuilder) { f = ivy.NewForest(builders...) }

// RefreshForest rebuilds every tree in the forest by re-running its
// builder, picking up directive changes since the last build.
func RefreshForest() { f = f.Build() }

// DefaultBuilder returns a TreeBuilder for the tree named "default":
// a standard-mode JSON tree rooted at `{}` with the given directives.
func DefaultBuilder(directives ...ivy.Directive) ivy.TreeBuilder {
	return func() ivy.Tree {
		tree, err := ivy.NewTree(&webDriver{PathParser: driver.SlashPathParser, Modem: driver.DummyModem},
			treeName, `{}`, directives...)
		if err != nil {
			panic(fmt.Errorf("build new tree fail: %w", err))
		}
		return tree
	}
}

type webDriver struct {
	driver.Modem
	driver.PathParser

	driver.StdRealizer
}

func (webDriver) Name() string { return "default" }
