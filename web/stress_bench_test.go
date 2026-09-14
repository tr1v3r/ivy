package web

// Benchmarks for the web layer (task t2). Run them with:
//
//	go test ./web -run '^$' -bench 'BenchmarkGetRule' -benchmem -count 3 \
//	  | tee /tmp/ivy-web-bench.txt
//
// All benchmarks drive the real gin router directly via ServeHTTP (no
// network, no httptest client overhead); the only network I/O is the
// curl-processor revalidation against an in-process httptest upstream.
//
// TTL-expiry first-request latency (p50/p99), manual method:
//   - mean per revalidating request: BenchmarkGetRuleTTLRevalidate (TTL=1ns,
//     every access re-fetches); the delta against BenchmarkGetRuleTTLCacheHit
//     is the pure revalidation cost;
//   - distribution (p50/p99): TestStressTTLExpiryFirstRequestLatency below —
//     it sleeps past a 1ms TTL and measures the next single request, 300
//     times, then prints p50/p99. It is skipped by default because it is
//     sleep-bound; enable with IVY_STRESS_SLOW=1:
//
//	IVY_STRESS_SLOW=1 go test ./web -run TestStressTTLExpiryFirstRequestLatency -v
//
// -race overhead: run the same benchmarks with and without -race and
// compare ns/op (expect roughly 2-10x on these lock-heavy paths).

import (
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/tr1v3r/ivy"
	"github.com/tr1v3r/ivy/driver"
)

// benchUpstream starts an immediately-answering httptest upstream for
// curl-backed benchmarks (127.0.0.1 only, zero external calls).
func benchUpstream(b *testing.B) *httptest.Server {
	b.Helper()
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"up":true}`))
	}))
	b.Cleanup(up.Close)
	return up
}

func benchStaticTree(b *testing.B) *gin.Engine {
	b.Helper()
	InitForest(DefaultBuilder(
		ivy.NewDirective("/", &driver.JSONProcessor{T: "create", JSONPath: "engine", V: []byte("ivy")}),
		ivy.NewDirective("/svc/a", &driver.JSONProcessor{T: "create", JSONPath: "l1", V: []byte("a")}),
		ivy.NewDirective("/svc/a/b", &driver.JSONProcessor{T: "create", JSONPath: "l2", V: []byte("b")}),
	))
	return newTestRouter()
}

// BenchmarkGetRuleStaticDeep: steady-state GET throughput on a standard
// (build-once) tree at a depth-3 path.
func BenchmarkGetRuleStaticDeep(b *testing.B) {
	r := benchStaticTree(b)
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/rule?name=default&path=/svc/a/b", nil))
			if w.Code != http.StatusOK {
				b.Fatalf("expect 200, got %d", w.Code)
			}
		}
	})
}

// BenchmarkGetRuleTTLCacheHit: GET throughput on a cache-TTL tree while
// inside the TTL window — every request is a cache hit (the hot serving
// path of the RULES_TTL mode).
func BenchmarkGetRuleTTLCacheHit(b *testing.B) {
	up := benchUpstream(b)
	InitForest(DefaultCacheBuilder(time.Hour,
		ivy.NewDirective("/", &driver.CURLProcessor{URL: up.URL})))
	r := newTestRouter()

	// warm the lazy realization once so the loop measures pure hits
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/rule?name=default&path=/", nil))
	if w.Code != http.StatusOK {
		b.Fatalf("warmup request failed: %d", w.Code)
	}

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/rule?name=default&path=/", nil))
			if w.Code != http.StatusOK {
				b.Fatalf("expect 200, got %d", w.Code)
			}
		}
	})
}

// BenchmarkGetRuleTTLRevalidate: TTL=1ns so EVERY access takes the expiry
// slow path and re-fetches from the upstream — the cost of "first request
// after TTL expiry". Compare with BenchmarkGetRuleTTLCacheHit.
func BenchmarkGetRuleTTLRevalidate(b *testing.B) {
	up := benchUpstream(b)
	InitForest(DefaultCacheBuilder(time.Nanosecond,
		ivy.NewDirective("/", &driver.CURLProcessor{URL: up.URL})))
	r := newTestRouter()

	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/rule?name=default&path=/", nil))
			if w.Code != http.StatusOK {
				b.Fatalf("expect 200, got %d", w.Code)
			}
		}
	})
}

// BenchmarkGetRuleParamDynamic: GET throughput when the target node holds
// a param-aware template in the dynamic layer — measures the per-request
// dynamic re-render on top of the cached static base.
func BenchmarkGetRuleParamDynamic(b *testing.B) {
	InitForest(DefaultBuilder(
		ivy.NewDirective("/", &driver.JSONProcessor{T: "create", JSONPath: "engine", V: []byte("ivy")}),
		ivy.NewDirective("/svc/a/b/c", &driver.TemplateProcessor{Pattern: `{"who":"${who}","leaf":"c"}`}),
	))
	r := newTestRouter()

	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			i++
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(http.MethodGet,
				"/api/v1/rule?name=default&path=/svc/a/b/c&who=p"+strconv.Itoa(i), nil))
			if w.Code != http.StatusOK {
				b.Fatalf("expect 200, got %d", w.Code)
			}
		}
	})
}

// BenchmarkGetRuleUnknownTree: the W1 404 branch — the cheapest possible
// full-router request, a baseline for router overhead.
func BenchmarkGetRuleUnknownTree(b *testing.B) {
	r := benchStaticTree(b)
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/rule?name=missing&path=/", nil))
			if w.Code != http.StatusNotFound {
				b.Fatalf("expect 404, got %d", w.Code)
			}
		}
	})
}

// TestStressTTLExpiryFirstRequestLatency measures the latency of the
// first request after TTL expiry, one expiry at a time, and reports the
// p50/p99 distribution. Sleep-bound by construction (each sample must
// cross a real TTL boundary), so it only runs when IVY_STRESS_SLOW is set
// and never in CI.
func TestStressTTLExpiryFirstRequestLatency(t *testing.T) {
	if os.Getenv("IVY_STRESS_SLOW") == "" {
		t.Skip("sleep-bound distribution measurement; set IVY_STRESS_SLOW=1 to run manually")
	}

	const (
		ttl    = time.Millisecond
		sample = 300
	)
	var hits atomic.Int64
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(`{"up":true}`))
	}))
	defer up.Close()

	InitForest(DefaultCacheBuilder(ttl, ivy.NewDirective("/", &driver.CURLProcessor{URL: up.URL})))
	r := newTestRouter()

	latencies := make([]time.Duration, 0, sample)
	for i := 0; i < sample; i++ {
		time.Sleep(ttl + 2*time.Millisecond) // cross the expiry boundary
		start := time.Now()
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/rule?name=default&path=/", nil))
		elapsed := time.Since(start)
		if w.Code != http.StatusOK {
			t.Fatalf("sample %d: expect 200, got %d", i, w.Code)
		}
		latencies = append(latencies, elapsed)
	}
	if n := hits.Load(); n != sample {
		t.Fatalf("every sampled request must have revalidated exactly once, got %d upstream hits", n)
	}

	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	pct := func(p float64) time.Duration {
		idx := int(float64(len(latencies)-1) * p)
		return latencies[idx]
	}
	t.Logf("TTL=%s first-request-after-expiry over %d samples: p50=%s p90=%s p99=%s max=%s",
		ttl, sample, pct(0.50), pct(0.90), pct(0.99), latencies[len(latencies)-1])
}
