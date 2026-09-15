package main

// Serve-layer reload stress (task t2, coverage-matrix cell 3 at the
// cmd/serve level): replicates the exact SIGHUP reload body —
// web.InitForest(rulesBuilder(load())) — in a loop while concurrent
// requests hit the real registered router. Covered for both builder
// flavors rulesBuilder can pick: RULES_TTL unset/zero (standard
// DefaultBuilder) and RULES_TTL positive (DefaultCacheBuilder).
//
// The rules file is replaced atomically (tmp file + rename) so a
// concurrent load() never observes a torn JSON document; readers must
// only ever observe whole generations.

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/tr1v3r/ivy/driver"
	"github.com/tr1v3r/ivy/web"
)

// writeRulesAtomic installs a one-directive rules file that creates
// {"gen":"<gen>"} at the tree root, replacing any previous file atomically.
// It returns an error instead of failing the test so the reload goroutine
// can propagate failures through a channel (t.Fatalf must only run on the
// test goroutine).
func writeRulesAtomic(file, gen string) error {
	items := []RuleDataItem{{
		Path: "/",
		Processors: []struct {
			Type string          `json:"type"`
			Data json.RawMessage `json:"data"`
		}{
			{Type: "json", Data: (&driver.JSONProcessor{
				T: "create", JSONPath: "gen", V: []byte(gen),
			}).Save()},
		},
	}}
	data, err := json.Marshal(items)
	if err != nil {
		return fmt.Errorf("marshal rules fail: %w", err)
	}
	tmp := file + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("write rules fail: %w", err)
	}
	if err := os.Rename(tmp, file); err != nil {
		return fmt.Errorf("rename rules fail: %w", err)
	}
	return nil
}

// readGen issues one GET /api/v1/rule and returns the served generation.
// Only call from the main test goroutine (it may call t.Fatalf).
func readGen(t *testing.T, r *gin.Engine) (string, bool) {
	t.Helper()
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/rule?name=default&path=/", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("expect 200, got %d (body: %s)", w.Code, w.Body.String())
	}
	var payload any
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatalf("invalid json response %q: %s", w.Body.String(), err)
	}
	encoded, _ := payload.(string)
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("response is not base64 rule bytes: %s", w.Body.String())
	}
	var doc map[string]any
	if err := json.Unmarshal(decoded, &doc); err != nil {
		t.Fatalf("rule is not json: %s (%s)", decoded, err)
	}
	gen, _ := doc["gen"].(string)
	return gen, true
}

// initForestFromRules loads the rules file the way main() does at
// startup and swaps the forest; a file-level load failure is fatal to
// the test (#58/W5).
func initForestFromRules(t *testing.T) {
	t.Helper()
	directives, err := load()
	if err != nil {
		t.Fatalf("load rules fail: %s", err)
	}
	web.InitForest(rulesBuilder(directives))
}

func TestStressSIGHUPReloadWhileReads(t *testing.T) {
	for _, tc := range []struct {
		name string
		ttl  time.Duration
	}{
		{"rules_ttl_zero_standard_builder", 0},
		{"rules_ttl_positive_cache_builder", time.Hour},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			file := filepath.Join(dir, "rules.json")
			if err := writeRulesAtomic(file, "v1"); err != nil {
				t.Fatalf("install rules fail: %s", err)
			}
			t.Setenv("RULES_FILE", file)

			origTTL := ruleTTL
			ruleTTL = tc.ttl
			defer func() { ruleTTL = origTTL }()

			gin.SetMode(gin.TestMode)
			initForestFromRules(t)
			r := register(gin.New()) // no Recovery middleware: a panic kills the binary

			if gen, ok := readGen(t, r); !ok || gen != "v1" {
				t.Fatalf("initial load must serve v1, got %q", gen)
			}

			const workers, requests = 12, 100

			var readers sync.WaitGroup
			stop := make(chan struct{})

			// the SIGHUP handler body, in a loop, alternating generations;
			// write failures are reported through a channel so t.Fatalf
			// stays on the test goroutine
			reloadErrs := make(chan error, 1)
			var reloader sync.WaitGroup
			reloader.Add(1)
			go func() {
				defer reloader.Done()
				gen := "v1"
				for {
					select {
					case <-stop:
						return
					default:
					}
					if gen == "v1" {
						gen = "v2"
					} else {
						gen = "v1"
					}
					if err := writeRulesAtomic(file, gen); err != nil {
						select {
						case reloadErrs <- err:
						default:
						}
						return
					}
					// the SIGHUP body: a failed load keeps the current forest
					directives, err := load()
					if err != nil {
						select {
						case reloadErrs <- err:
						default:
						}
						return
					}
					web.InitForest(rulesBuilder(directives))
					time.Sleep(2 * time.Millisecond) // pacing only, not an assertion
				}
			}()

			for w := 0; w < workers; w++ {
				readers.Add(1)
				go func(wid int) {
					defer readers.Done()
					for i := 0; i < requests; i++ {
						probe := fmt.Sprintf("worker %d request %d", wid, i)
						rec := httptest.NewRecorder()
						r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/rule?name=default&path=/", nil))
						if rec.Code != http.StatusOK {
							t.Errorf("%s: reload must not fail reads, got %d (body: %s)", probe, rec.Code, rec.Body.String())
							return
						}
						var payload any
						if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
							t.Errorf("%s: invalid json response %q: %s", probe, rec.Body.String(), err)
							return
						}
						encoded, _ := payload.(string)
						decoded, err := base64.StdEncoding.DecodeString(encoded)
						if err != nil {
							t.Errorf("%s: response is not base64 rule bytes: %s", probe, rec.Body.String())
							return
						}
						if !strings.Contains(string(decoded), `"gen":"v1"`) && !strings.Contains(string(decoded), `"gen":"v2"`) {
							t.Errorf("%s: torn generation observed: %s", probe, decoded)
							return
						}
					}
				}(w)
			}
			readers.Wait()
			close(stop)
			reloader.Wait()
			select {
			case err := <-reloadErrs:
				t.Fatalf("reload goroutine failed to write rules: %s", err)
			default:
			}

			// a final reload is observable end-to-end: file -> load -> builder -> forest -> request
			if err := writeRulesAtomic(file, "v2"); err != nil {
				t.Fatalf("final rules write fail: %s", err)
			}
			initForestFromRules(t)
			if gen, ok := readGen(t, r); !ok || gen != "v2" {
				t.Fatalf("post-reload read must serve v2, got %q", gen)
			}
		})
	}
}

// TestStressSIGHUPReloadUpstreamCurl keeps the full production loader
// path in the loop: the rules file carries a curl processor pointing at
// an in-process upstream, and reloads race reads. Pins that a reload
// re-fetches upstream through the real load() -> CURLProcessor wiring
// and readers always see well-formed generations.
func TestStressSIGHUPReloadUpstreamCurl(t *testing.T) {
	var hits atomic.Int64
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(`{"up":true}`))
	}))
	defer up.Close()

	items := []RuleDataItem{{
		Path: "/",
		Processors: []struct {
			Type string          `json:"type"`
			Data json.RawMessage `json:"data"`
		}{
			{Type: "curl", Data: (&driver.CURLProcessor{URL: up.URL}).Save()},
		},
	}}
	data, err := json.Marshal(items)
	if err != nil {
		t.Fatalf("marshal rules fail: %s", err)
	}
	dir := t.TempDir()
	file := filepath.Join(dir, "rules.json")
	if err := os.WriteFile(file, data, 0o600); err != nil {
		t.Fatalf("write rules fail: %s", err)
	}
	t.Setenv("RULES_FILE", file)

	origTTL := ruleTTL
	ruleTTL = 0 // standard builder: realized per build, reload re-fetches
	defer func() { ruleTTL = origTTL }()

	gin.SetMode(gin.TestMode)
	initForestFromRules(t)
	r := register(gin.New())

	if n := hits.Load(); n != 1 {
		t.Fatalf("initial build must fetch upstream once, got %d", n)
	}

	const reloads = 50
	for i := 0; i < reloads; i++ {
		initForestFromRules(t) // the SIGHUP body
	}
	if n := hits.Load(); n != 1+reloads {
		t.Fatalf("each reload must re-fetch exactly once: want %d, got %d", 1+reloads, n)
	}

	// spot read after the reload storm
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/rule?name=default&path=/", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("expect 200 after reload storm, got %d", w.Code)
	}
	var payload any
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatalf("invalid json response: %s", w.Body.String())
	}
	encoded, _ := payload.(string)
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || !strings.Contains(string(decoded), `"up":true`) {
		t.Fatalf("unexpected rule content: %s (err=%v)", decoded, err)
	}
}
