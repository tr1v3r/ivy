package web

// Graceful-shutdown concurrency smoke (coverage-matrix cell 6): pins that
// the http.Server.Shutdown drain used by Serve leaves in-flight GetRule
// requests intact — handlers already executing complete and answer, they
// are not truncated by the shutdown.
//
// The blocking happens inside a param-aware combined processor, i.e. in
// the per-request dynamic layer (dynamicRealize), which takes no realize
// write lock: all requests can be in flight simultaneously instead of
// serializing behind the first one.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tr1v3r/ivy"
	"github.com/tr1v3r/ivy/driver"
)

func TestStressGracefulShutdownDrainsInflight(t *testing.T) {
	const inflight = 8

	entered := make(chan struct{}, inflight)
	release := make(chan struct{})
	gate := &driver.RawProcessor{Proc: func(_ *driver.RealizeContext, _ []byte) ([]byte, error) {
		entered <- struct{}{}
		<-release // hold the request inside the tree's dynamic layer
		return nil, nil
	}}
	// The template makes the chain param-aware, so the whole combined
	// processor stays in the dynamic layer (see driver.IsParamAware).
	render := &driver.TemplateProcessor{Pattern: `{"drained":"${tag}"}`}
	blocking := driver.CombineProcessor(gate, render)

	InitForest(DefaultBuilder(ivy.NewDirective("/", blocking)))
	r := newTestRouter()

	ts := httptest.NewServer(r)
	defer ts.Close()
	client := ts.Client()
	client.Timeout = 10 * time.Second // failure guard, not an assertion

	type result struct {
		tag  string
		code int
		body string
		err  error
	}
	results := make(chan result, inflight)

	var wg sync.WaitGroup
	for i := 0; i < inflight; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			tag := fmt.Sprintf("d%d", idx)
			resp, err := client.Get(ts.URL + "/api/v1/rule?name=default&path=/&tag=" + tag)
			if err != nil {
				results <- result{tag: tag, err: err}
				return
			}
			defer resp.Body.Close()
			var payload any
			if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
				results <- result{tag: tag, code: resp.StatusCode, err: err}
				return
			}
			encoded, _ := payload.(string)
			decoded, err := base64.StdEncoding.DecodeString(encoded)
			results <- result{tag: tag, code: resp.StatusCode, body: string(decoded), err: err}
		}(i)
	}

	// Wait until every request is inside the handler (deterministic
	// synchronization; the timeout is only a failure guard).
	for i := 0; i < inflight; i++ {
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatal("requests did not reach the handler in time")
		}
	}

	// Drain exactly like Serve does, then let the blocked work proceed.
	shutdownDone := make(chan error, 1)
	go func() { shutdownDone <- ts.Config.Shutdown(context.Background()) }()
	close(release)
	wg.Wait()

	select {
	case err := <-shutdownDone:
		if err != nil {
			t.Fatalf("Shutdown failed: %s", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Shutdown did not return after in-flight requests finished")
	}

	for i := 0; i < inflight; i++ {
		res := <-results
		if res.err != nil {
			t.Errorf("request %s: in-flight request truncated by shutdown: %s", res.tag, res.err)
			continue
		}
		if res.code != http.StatusOK {
			t.Errorf("request %s: expect 200 after drain, got %d", res.tag, res.code)
			continue
		}
		if want := `"drained":"` + res.tag + `"`; !strings.Contains(res.body, want) {
			t.Errorf("request %s: drained body mismatch: want %s in %s", res.tag, want, res.body)
		}
	}
}
