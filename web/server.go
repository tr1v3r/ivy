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

	// wati shutdown signal
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

func InitForest(builders ...ivy.TreeBuilder) { f = ivy.NewForest(builders...) }

func RefreshForest() { f = f.Build() }

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
