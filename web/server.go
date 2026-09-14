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
	"net/http"
	"sync"
	"time"

	"github.com/tr1v3r/pkg/guard"
	"github.com/tr1v3r/pkg/log"

	"github.com/tr1v3r/ivy"
	"github.com/tr1v3r/ivy/driver"
)

const treeName = "default"

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

// f is the process-wide forest. All access goes through forestMu (or the
// startup-only InitForest write): cmd/serve refreshes the forest from a
// background ticker goroutine while HTTP handlers read it on every request,
// so an unsynchronized swap here is a data race (torn interface reads).
var (
	forestMu sync.RWMutex
	f        ivy.Forest
)

// currentForest returns the process-wide forest.
func currentForest() ivy.Forest {
	forestMu.RLock()
	defer forestMu.RUnlock()
	return f
}

// InitForest builds the process-wide forest from the given tree builders.
// Call once during startup, before serving requests.
func InitForest(builders ...ivy.TreeBuilder) {
	forestMu.Lock()
	defer forestMu.Unlock()
	f = ivy.NewForest(builders...)
}

// RefreshForest rebuilds every tree in the forest by re-running its
// builder, picking up directive changes since the last build.
//
// It must not reassign the global: Build() mutates and returns the same
// forest instance, so reassigning raced concurrent readers in GetRule
// (the 5s refresh goroutine in cmd/serve wrote `f` while handlers read
// it — caught by -race as write server.go / read handler.go).
func RefreshForest() { currentForest().Build() }

// DefaultBuilder returns a TreeBuilder for the tree named "default":
// a standard-mode JSON tree rooted at `{}` with the given directives.
func DefaultBuilder(directives ...ivy.Directive) ivy.TreeBuilder {
	return func() ivy.Tree {
		tree, err := ivy.NewTree(&webDriver{PathParser: driver.SlashPathParser, Modem: driver.DummyModem},
			treeName, `{}`, directives...)
		if err != nil {
			// A failing build (e.g. an unreachable or 5xx curl processor)
			// must not panic: NewForest stores builders without a recover
			// wrapper, so the panic escapes forest.Build() — fatal at
			// startup, and process-killing when it fires inside a
			// background refresh goroutine. Log, return nil (which
			// forest.Build skips), and keep serving the previously
			// built tree.
			log.Error("build default tree fail: %s", err)
			return nil
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
