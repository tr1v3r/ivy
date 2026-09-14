package web

// Concurrency stress suite for the web layer (team ivy-concurrency-stress,
// task t2). Every test here must satisfy the CI contract:
//   - default `go test ./...` green and CI runs the same suite under
//     `-race` (see .github/workflows/ci.yml), so no data race may exist on
//     any path exercised below;
//   - bounded duration: work is counted in requests, not wall-clock;
//   - upstreams are in-process httptest servers on 127.0.0.1 — zero
//     external network calls;
//   - no sleep-based assertions: sleeps only push time past a TTL boundary
//     where more elapsed time cannot flip the expected outcome.

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tr1v3r/ivy"
	"github.com/tr1v3r/ivy/driver"
)

// decodeRuleResponse unwraps a GetRule payload and returns the raw rule
// bytes. The handler JSON-encodes []byte, so a 200 body is a quoted
// base64 string. Any unexpected status or malformed wrapping is reported
// against the named probe.
func decodeRuleResponse(t *testing.T, probe string, w *httptest.ResponseRecorder) (string, bool) {
	t.Helper()
	if w.Code != http.StatusOK {
		t.Errorf("%s: expect 200, got %d (body: %s)", probe, w.Code, w.Body.String())
		return "", false
	}
	var payload any
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Errorf("%s: invalid json response %q: %s", probe, w.Body.String(), err)
		return "", false
	}
	encoded, _ := payload.(string)
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Errorf("%s: response is not base64 rule bytes: %s", probe, w.Body.String())
		return "", false
	}
	return string(decoded), true
}

// TestStressGetRuleMixedLoad is coverage-matrix cell 1: concurrent GET
// /api/v1/rule against the real gin router with a mixed workload —
// known and unknown tree names, shallow and deep paths, requests with
// and without params. It pins:
//   - the W1 404 branch (unknown ?name= answers 404, never panics — the
//     router has no Recovery middleware, so a panic would crash the test
//     binary);
//   - param passthrough: a param-aware template in the dynamic layer must
//     render each request's own ?who= value even when many different
//     values are in flight at once (cache pollution from another request
//     would surface as a mismatched value).
func TestStressGetRuleMixedLoad(t *testing.T) {
	InitForest(DefaultBuilder(
		ivy.NewDirective("/", &driver.JSONProcessor{T: "create", JSONPath: "engine", V: []byte("ivy")}),
		ivy.NewDirective("/svc/a", &driver.JSONProcessor{T: "create", JSONPath: "l1", V: []byte("a")}),
		ivy.NewDirective("/svc/a/b", &driver.JSONProcessor{T: "create", JSONPath: "l2", V: []byte("b")}),
		ivy.NewDirective("/svc/a/b/c", &driver.TemplateProcessor{Pattern: `{"who":"${who}","leaf":"c"}`}),
	))
	r := newTestRouter()

	const (
		workers  = 32
		requests = 64 // per worker
		classes  = 6  // probe classes below
	)

	// classHits proves every branch of the matrix was actually exercised.
	var classHits [classes]atomic.Int64

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(wid int) {
			defer wg.Done()
			for i := 0; i < requests; i++ {
				class := (wid + i) % classes
				probe := fmt.Sprintf("worker %d request %d class %d", wid, i, class)
				switch class {
				case 0: // shallow static path
					rec := httptest.NewRecorder()
					r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/rule?name=default&path=/", nil))
					if rule, ok := decodeRuleResponse(t, probe, rec); ok {
						if !strings.Contains(rule, `"engine":"ivy"`) {
							t.Errorf("%s: rule content mismatch: %s", probe, rule)
						}
					}
				case 1: // depth-2 static path (content inherits the parent)
					rec := httptest.NewRecorder()
					r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/rule?name=default&path=/svc/a", nil))
					if rule, ok := decodeRuleResponse(t, probe, rec); ok {
						if !strings.Contains(rule, `"l1":"a"`) || !strings.Contains(rule, `"engine":"ivy"`) {
							t.Errorf("%s: rule content mismatch: %s", probe, rule)
						}
					}
				case 2: // depth-3 static path
					rec := httptest.NewRecorder()
					r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/rule?name=default&path=/svc/a/b", nil))
					if rule, ok := decodeRuleResponse(t, probe, rec); ok {
						if !strings.Contains(rule, `"l2":"b"`) {
							t.Errorf("%s: rule content mismatch: %s", probe, rule)
						}
					}
				case 3: // deep path + param: dynamic layer must render this request's own value
					who := fmt.Sprintf("w%d-%d", wid, i)
					rec := httptest.NewRecorder()
					r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/rule?name=default&path=/svc/a/b/c&who="+who, nil))
					if rule, ok := decodeRuleResponse(t, probe, rec); ok {
						if want := `"who":"` + who + `"`; !strings.Contains(rule, want) {
							t.Errorf("%s: param pollution or miss: want %s in %s", probe, want, rule)
						}
					}
				case 4: // same deep path without a param: placeholder renders empty
					rec := httptest.NewRecorder()
					r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/rule?name=default&path=/svc/a/b/c", nil))
					if rule, ok := decodeRuleResponse(t, probe, rec); ok {
						if !strings.Contains(rule, `"who":""`) {
							t.Errorf("%s: unparametrized render mismatch: %s", probe, rule)
						}
					}
				case 5: // unknown tree name: W1 404 branch
					rec := httptest.NewRecorder()
					r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/rule?name=missing&path=/", nil))
					if rec.Code != http.StatusNotFound {
						t.Errorf("%s: expect 404, got %d (body: %s)", probe, rec.Code, rec.Body.String())
					} else if !strings.Contains(rec.Body.String(), "not found") {
						t.Errorf("%s: expected not-found payload, got %s", probe, rec.Body.String())
					}
				}
				classHits[class].Add(1)
			}
		}(w)
	}
	wg.Wait()

	for class := 0; class < classes; class++ {
		if classHits[class].Load() == 0 {
			t.Errorf("probe class %d was never exercised", class)
		}
	}
}

// TestStressRefreshForestWhileReads is coverage-matrix cell 2: concurrent
// requests while RefreshForest re-runs builders in a loop (the cmd/serve
// refresh trigger). Every response must stay a valid 200 carrying the
// upstream marker — readers may observe the old or the freshly rebuilt
// tree, but never a torn or failed one. Under -race this is the W2
// discriminator: RefreshForest must not reassign the global forest
// unsynchronized.
func TestStressRefreshForestWhileReads(t *testing.T) {
	var hits atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(`{"up":true}`))
	}))
	defer upstream.Close()

	InitForest(DefaultBuilder(ivy.NewDirective("/", &driver.CURLProcessor{URL: upstream.URL})))
	before := hits.Load() // the standard build realizes once

	r := newTestRouter()

	var readers sync.WaitGroup
	stop := make(chan struct{})

	// refresh loop (paced so it does not starve the readers on a slow CI box)
	var refreshers sync.WaitGroup
	for i := 0; i < 2; i++ {
		refreshers.Add(1)
		go func() {
			defer refreshers.Done()
			for {
				select {
				case <-stop:
					return
				default:
					RefreshForest()
					time.Sleep(500 * time.Microsecond) // pacing only, not an assertion
				}
			}
		}()
	}

	const workers, requests = 16, 100
	for w := 0; w < workers; w++ {
		readers.Add(1)
		go func(wid int) {
			defer readers.Done()
			for i := 0; i < requests; i++ {
				probe := fmt.Sprintf("worker %d request %d", wid, i)
				rec := httptest.NewRecorder()
				r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/rule?name=default&path=/", nil))
				if rule, ok := decodeRuleResponse(t, probe, rec); ok {
					if !strings.Contains(rule, `"up":true`) {
						t.Errorf("%s: rule content torn during refresh: %s", probe, rule)
					}
				}
			}
		}(w)
	}
	readers.Wait()
	close(stop)
	refreshers.Wait()

	// A final explicit refresh deterministically proves the rebuild path
	// re-runs the upstream exactly once more.
	RefreshForest()
	if delta := hits.Load() - before; delta < 1 {
		t.Errorf("explicit refresh must re-run the builder at least once, delta=%d", delta)
	}
}

// TestStressInitForestSwapWhileReads is coverage-matrix cell 3: concurrent
// requests while InitForest swaps the whole forest — the SIGHUP reload
// path of cmd/serve, here against both builder flavors (standard
// DefaultBuilder and TTL DefaultCacheBuilder). Readers must only ever
// observe one of the swapped-in generations: a value outside the set
// means a torn swap or content mixing.
func TestStressInitForestSwapWhileReads(t *testing.T) {
	stdV1 := DefaultBuilder(
		ivy.NewDirective("/", &driver.JSONProcessor{T: "create", JSONPath: "gen", V: []byte("v1")}))
	ttlV2 := DefaultCacheBuilder(time.Hour,
		ivy.NewDirective("/", &driver.JSONProcessor{T: "create", JSONPath: "gen", V: []byte("v2")}))
	stdV3 := DefaultBuilder(
		ivy.NewDirective("/", &driver.JSONProcessor{T: "create", JSONPath: "gen", V: []byte("v3")}))

	InitForest(stdV1)
	r := newTestRouter()

	allowed := map[string]bool{"v1": true, "v2": true, "v3": true}

	var readers sync.WaitGroup
	stop := make(chan struct{})

	// swapper: cycles the three builders exactly like a SIGHUP loop would
	var swapper sync.WaitGroup
	swapper.Add(1)
	go func() {
		defer swapper.Done()
		cycle := []func() ivy.Tree{ttlV2, stdV3, stdV1}
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			InitForest(cycle[i%len(cycle)])
			time.Sleep(200 * time.Microsecond) // pacing only, not an assertion
		}
	}()

	const workers, requests = 16, 120
	for w := 0; w < workers; w++ {
		readers.Add(1)
		go func(wid int) {
			defer readers.Done()
			for i := 0; i < requests; i++ {
				probe := fmt.Sprintf("worker %d request %d", wid, i)
				rec := httptest.NewRecorder()
				r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/rule?name=default&path=/", nil))
				rule, ok := decodeRuleResponse(t, probe, rec)
				if !ok {
					return
				}
				var doc map[string]any
				if err := json.Unmarshal([]byte(rule), &doc); err != nil {
					t.Errorf("%s: rule is not json: %s (%s)", probe, rule, err)
					return
				}
				gen, _ := doc["gen"].(string)
				if !allowed[gen] {
					t.Errorf("%s: torn forest swap observed gen=%q (rule=%s)", probe, gen, rule)
					return
				}
			}
		}(w)
	}
	readers.Wait()
	close(stop)
	swapper.Wait()

	// The swap is observable and effective: install v2 and read it back.
	InitForest(ttlV2)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/rule?name=default&path=/", nil))
	if rule, ok := decodeRuleResponse(t, "final swap", rec); ok {
		if !strings.Contains(rule, `"gen":"v2"`) {
			t.Errorf("post-swap read must serve the v2 tree, got %s", rule)
		}
	}
}

// TestStressCacheTTLThunderingHerd is coverage-matrix cell 4: concurrent
// origin re-fetch for a curl-backed cache-TTL tree. The upstream counts
// calls and answers with a small delay so a stampede is observable in the
// counter. Phases:
//   - A: first access realizes lazily — exactly 1 upstream call;
//   - B: a 64-way burst inside the TTL window — all cache hits, counter
//     unchanged;
//   - C: after expiry a 64-way burst revalidates — the realize write lock
//     plus its double-check collapses the herd to exactly 1 more upstream
//     call (no thundering herd, and this is exact, not a bound);
//   - D: phase C repeats — one call per crossed expiry window, independent
//     of concurrency.
func TestStressCacheTTLThunderingHerd(t *testing.T) {
	var hits atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		time.Sleep(15 * time.Millisecond) // make a stampede visible; bounded and rare
		_, _ = w.Write([]byte(`{"up":true}`))
	}))
	defer upstream.Close()

	const (
		ttl     = 80 * time.Millisecond
		burst   = 64
		margin  = 120 * time.Millisecond
		wantHit = `"up":true`
	)
	InitForest(DefaultCacheBuilder(ttl, ivy.NewDirective("/", &driver.CURLProcessor{URL: upstream.URL})))
	r := newTestRouter()

	burstRead := func(label string) {
		t.Helper()
		start := make(chan struct{})
		var wg sync.WaitGroup
		for i := 0; i < burst; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				probe := fmt.Sprintf("%s burst worker", label)
				rec := httptest.NewRecorder()
				r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/rule?name=default&path=/", nil))
				if rule, ok := decodeRuleResponse(t, probe, rec); ok {
					if !strings.Contains(rule, wantHit) {
						t.Errorf("%s: rule content mismatch: %s", probe, rule)
					}
				}
			}()
		}
		close(start)
		wg.Wait()
	}

	// Phase A: lazy first realize.
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/rule?name=default&path=/", nil))
	if rule, ok := decodeRuleResponse(t, "phase A", rec); ok {
		if !strings.Contains(rule, wantHit) {
			t.Errorf("phase A: rule content mismatch: %s", rule)
		}
	}
	if n := hits.Load(); n != 1 {
		t.Fatalf("phase A: first access must realize exactly once, got %d upstream calls", n)
	}

	// Phase B: burst inside the TTL window — pure cache hits.
	burstRead("phase B")
	if n := hits.Load(); n != 1 {
		t.Fatalf("phase B: within TTL a burst must not re-fetch, got %d upstream calls", n)
	}

	// Phases C and D: one burst per expiry window, each exactly 1 re-fetch.
	for phase, want := range []int64{2, 3} {
		time.Sleep(ttl + margin) // cross the TTL boundary; extra sleep cannot flip the outcome
		burstRead(fmt.Sprintf("phase %c", 'C'+rune(phase)))
		if n := hits.Load(); n != want {
			t.Fatalf("phase %c: %d-way burst after expiry must collapse to exactly one re-fetch, got %d upstream calls (thundering herd)", 'C'+rune(phase), burst, n)
		}
	}
}

// TestStressBuilderFailureWhileReads is coverage-matrix cell 5: the
// builder-failure injection (upstream turns 5xx) racing a refresh loop
// and concurrent reads. W4 semantics: the failing builder must return
// nil, the forest must keep the previously built tree, every read stays
// 200 with the last-good content, and nothing panics (the router has no
// Recovery middleware, so a builder panic would crash the whole test
// binary).
func TestStressBuilderFailureWhileReads(t *testing.T) {
	var hits atomic.Int64
	var fail atomic.Bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		if fail.Load() {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(`{"up":true}`))
	}))
	defer upstream.Close()

	InitForest(DefaultBuilder(ivy.NewDirective("/", &driver.CURLProcessor{URL: upstream.URL})))
	if n := hits.Load(); n != 1 {
		t.Fatalf("startup build should realize the curl processor once, got %d upstream hits", n)
	}
	r := newTestRouter()

	fail.Store(true)

	var readers sync.WaitGroup
	stop := make(chan struct{})

	var refreshers sync.WaitGroup
	for i := 0; i < 2; i++ {
		refreshers.Add(1)
		go func() {
			defer refreshers.Done()
			for {
				select {
				case <-stop:
					return
				default:
					RefreshForest()                    // builder fails, must be a no-op on the forest
					time.Sleep(500 * time.Microsecond) // pacing only, not an assertion
				}
			}
		}()
	}

	const workers, requests = 16, 100
	for w := 0; w < workers; w++ {
		readers.Add(1)
		go func(wid int) {
			defer readers.Done()
			for i := 0; i < requests; i++ {
				probe := fmt.Sprintf("worker %d request %d", wid, i)
				rec := httptest.NewRecorder()
				r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/rule?name=default&path=/", nil))
				if rec.Code != http.StatusOK {
					t.Errorf("%s: failed refresh must not affect reads, got %d (body: %s)", probe, rec.Code, rec.Body.String())
					return
				}
				if rule, ok := decodeRuleResponse(t, probe, rec); ok {
					if !strings.Contains(rule, `"up":true`) {
						t.Errorf("%s: last-good content must keep serving, got %s", probe, rule)
					}
				}
			}
		}(w)
	}
	readers.Wait()
	close(stop)
	refreshers.Wait()

	// Deterministic proof of the W4 path: exactly one more upstream attempt.
	before := hits.Load()
	RefreshForest()
	if delta := hits.Load() - before; delta != 1 {
		t.Errorf("one failing refresh must attempt the upstream exactly once, delta=%d", delta)
	}
}
