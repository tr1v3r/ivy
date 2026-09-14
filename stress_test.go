package ivy

// Engine-layer concurrency stress suite.
//
// Coverage matrix (each cell carries a deterministic assertion — exact
// content and/or exact realize counts — so every class of regression is
// observable, not just panics):
//
//	TestStressFourModesConcurrentGet      four modes × 64-goroutine deep-path reads
//	TestStressForestSwapDuringGet         concurrent GetVal × Build/RefreshTree/Set swap (engine face of W2/#68)
//	TestStressSetVsGetFunctional          concurrent Set × Get: update visible, never torn (#47 fixed)
//	TestStressCacheTTLExpiryStorm         TTL expiry storm: re-source exactness + thundering-herd quantification
//	TestStressParamAwareConcurrent        param-aware dynamic layer under concurrent distinct params (H4/#67)
//	TestStressRateLimiterDeterministic    rate limit 0/burst-1 under 64 goroutines: exactly one winner
//	TestStressRateLimiterSwapConsistency  SetRateLimit swaps vs concurrent Gets: realize==allow invariant
//	TestStressSetFallbackDuringGet        SetFallback swaps vs fallback-serving Gets (#47 fixed)
//	TestStressSetDefaultContextDuringGet  SetDefaultContext swaps vs realizing Gets (#47 fixed)
//	TestStressSetProcsSwapDuringGet       Set/apply chain swaps vs Gets (#47 fixed)
//
// The remaining known semantics gap is exercised only behind an opt-in
// gate so the default CI run (which is `go test -race ./...`) stays green:
//
//	TestStressKnownRace48_ParentRefreshStaleChild   t.Skip unless IVY_STRESS_UNFIXED=1 (issue #48)

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/time/rate"

	"github.com/tr1v3r/ivy/driver"
)

// stress geometry: 64 concurrent goroutines is the same order as the audit
// reproduction loads; iterations are FIXED (not deadline-based) so test
// duration is bounded and assertions stay deterministic under -race.
const (
	stressWorkers  = 64
	stressRounds   = 32
	stressMaxDiffs = 10 // cap on collected mismatch samples per test
)

// failCollector accumulates worker-goroutine failures (including recovered
// panics) and reports them on the test goroutine after the workers join.
type failCollector struct {
	mu   sync.Mutex
	msgs []string
}

func (f *failCollector) add(format string, args ...any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.msgs) < stressMaxDiffs {
		f.msgs = append(f.msgs, fmt.Sprintf(format, args...))
	}
}

// report fails t when any worker recorded a problem. A second counter is
// used for truncated tallies (the sample list saturates at the cap).
func (f *failCollector) report(t *testing.T, total int64) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, m := range f.msgs {
		t.Error(m)
	}
	if len(f.msgs) >= stressMaxDiffs && total > int64(len(f.msgs)) {
		t.Errorf("... and %d more anomalous responses (samples capped at %d)", total-int64(len(f.msgs)), stressMaxDiffs)
	}
}

// countingAppend returns a NON-idempotent processor (output = input +
// suffix) that also counts every realization. Compounding regressions
// (audit F1/#63) surface as doubled suffixes; mode-semantics regressions
// surface as wrong realize counts.
func countingAppend(counter *int32, suffix string) driver.Processor {
	return &driver.RawProcessor{Proc: func(_ *driver.RealizeContext, before []byte) ([]byte, error) {
		atomic.AddInt32(counter, 1)
		out := make([]byte, 0, len(before)+len(suffix))
		out = append(out, before...)
		return append(out, suffix...), nil
	}}
}

// markerTreeDirectives returns one directive per level of /a/b/c/d, each
// appending a per-level suffix. Realized content is therefore exactly
// "R-x" + "-a" + "-b" + "-c" + "-d" down the deepest path, with every
// intermediate path carrying its own exact prefix — cross-node content
// mixing is directly observable as a string mismatch.
func markerTreeDirectives(counter *int32) []Directive {
	return []Directive{
		NewDirective("/", countingAppend(counter, "-x")),
		NewDirective("/a", countingAppend(counter, "-a")),
		NewDirective("/a/b", countingAppend(counter, "-b")),
		NewDirective("/a/b/c", countingAppend(counter, "-c")),
		NewDirective("/a/b/c/d", countingAppend(counter, "-d")),
	}
}

var stressMarkerPaths = []struct{ path, want string }{
	{"/", "R-x"},
	{"/a", "R-x-a"},
	{"/a/b", "R-x-a-b"},
	{"/a/b/c", "R-x-a-b-c"},
	{"/a/b/c/d", "R-x-a-b-c-d"},
}

// TestStressFourModesConcurrentGet hammers every tree mode with concurrent
// deep-path reads and asserts two things per mode:
//
//   - content exactness: every response matches the path's expected string,
//     so no cross-node mixing (wrong markers), no compounding (audit
//     F1/#63: realize must restart from the node base, never the previous
//     output) and no skipped processors (audit F3/#71 face);
//   - realize-count exactness: standard realizes each node once at build,
//     lazy and cache-TTL once per node on first access (double-checked
//     locking must collapse the herd), instant on EVERY traversal
//     (workers × rounds × Σ path depths).
//
// The counts make the four mode semantics themselves part of the contract:
// a caching regression shows up as an inflated/deflated counter even when
// the served content still looks right.
func TestStressFourModesConcurrentGet(t *testing.T) {
	const instantTotal = int32(stressWorkers * stressRounds * (1 + 2 + 3 + 4 + 5))

	type mode struct {
		name       string
		build      func(*int32) (Tree, error)
		buildCount int32 // realizes that happened during build
		finalCount int32 // realizes after the concurrent phase
	}

	modes := []mode{
		{"standard", func(c *int32) (Tree, error) {
			return NewTree(newTestDriver(), "stress_std", "R", markerTreeDirectives(c)...)
		}, 5, 5},
		{"lazy", func(c *int32) (Tree, error) {
			return NewLazyTree(newTestDriver(), "stress_lazy", "R", markerTreeDirectives(c)...)
		}, 0, 5},
		{"instant", func(c *int32) (Tree, error) {
			return NewLazyInstantTree(newTestDriver(), "stress_inst", "R", markerTreeDirectives(c)...)
		}, 0, instantTotal},
		{"cache-ttl", func(c *int32) (Tree, error) {
			// TTL far longer than the test: within the window the mode is
			// behaviorally lazy; expiry behavior has its own dedicated test.
			return NewLazyCacheTree(newTestDriver(), "stress_ttl", "R", time.Hour, markerTreeDirectives(c)...)
		}, 0, 5},
	}

	for _, m := range modes {
		t.Run(m.name, func(t *testing.T) {
			var counter int32
			tree, err := m.build(&counter)
			if err != nil {
				t.Fatalf("build fail: %s", err)
			}
			if got := atomic.LoadInt32(&counter); got != m.buildCount {
				t.Fatalf("post-build realize count = %d, want %d", got, m.buildCount)
			}

			var fails failCollector
			var anomalies int64
			var wg sync.WaitGroup
			for g := 0; g < stressWorkers; g++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					defer func() {
						if r := recover(); r != nil {
							fails.add("panic during concurrent Get: %v", r)
							atomic.AddInt64(&anomalies, 1)
						}
					}()
					for round := 0; round < stressRounds; round++ {
						for _, mp := range stressMarkerPaths {
							val, err := tree.Get(mp.path)
							if err != nil {
								fails.add("Get(%s) error: %s", mp.path, err)
								atomic.AddInt64(&anomalies, 1)
								continue
							}
							if got := string(val); got != mp.want {
								fails.add("Get(%s) = %q, want %q", mp.path, got, mp.want)
								atomic.AddInt64(&anomalies, 1)
							}
						}
					}
				}()
			}
			wg.Wait()

			if got := atomic.LoadInt32(&counter); got != m.finalCount {
				t.Errorf("realize count after concurrent phase = %d, want %d (mode semantics broken)", got, m.finalCount)
			}
			fails.report(t, atomic.LoadInt64(&anomalies))
		})
	}
}

// TestStressForestSwapDuringGet is the engine-layer face of audit W2 (#68):
// while readers resolve trees through the forest (GetVal /
// GetValWithContext), a refresher goroutine keeps swapping the tree
// instances under them via Build, RefreshTree and Set. After the W2 fix
// every swap goes through the forest locks, so this must stay -race clean
// and every read must return the exact rebuilt content (rebuilds are
// deterministic).
func TestStressForestSwapDuringGet(t *testing.T) {
	const treePath = "/a/b"
	// JSONProcessor "create" embeds V as a JSON string value
	const want = `{"svc":"forest","leaf":"true"}`

	// failCollector/atomic counters are declared before the builder: the
	// builder closure runs on the REFRESHER goroutine (Build/RefreshTree/Set
	// below), where t.Fatalf/FailNow must not be called — it would Goexit
	// the wrong goroutine. Builder failures are recorded and the nil tree
	// is skipped by forest.Set/Build, so the warm-up check on the test
	// goroutine fails the test properly instead.
	var fails failCollector
	var anomalies int64

	newTree := func() Tree {
		tree, err := NewTree(newTestDriver(), "swap_t", `{"svc":"forest"}`,
			NewDirective("/a/b", &driver.JSONProcessor{T: "create", JSONPath: "leaf", V: []byte("true")}),
		)
		if err != nil {
			fails.add("builder failed: %s", err)
			atomic.AddInt64(&anomalies, 1)
			return nil // forest.Build/Set skip nil trees
		}
		return tree
	}

	f := NewForest(func() Tree { return newTree() })
	if val, err := f.GetVal("swap_t", treePath); err != nil || string(val) != want {
		t.Fatalf("warm-up GetVal: val=%q err=%v", val, err)
	}

	stop := make(chan struct{})
	var wg sync.WaitGroup

	// refresher: 30 swaps through all three swap paths
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(stop)
		for i := 0; i < 30; i++ {
			f.Build()               // rebuild every tree from its builder
			f.RefreshTree("swap_t") // rebuild the named tree only
			f.Set(newTree())        // raw instance swap
			time.Sleep(time.Millisecond)
		}
	}()

	for g := 0; g < stressWorkers; g++ {
		wg.Add(1)
		go func(gid int) {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					fails.add("panic during GetVal: %v", r)
					atomic.AddInt64(&anomalies, 1)
				}
			}()
			// hard iteration cap keeps the test bounded even if the
			// refresher stalls; normally the stop channel ends the loop
			for i := 0; i < 100000; i++ {
				select {
				case <-stop:
					return
				default:
				}
				var (
					val []byte
					err error
				)
				if gid%2 == 0 {
					val, err = f.GetVal("swap_t", treePath)
				} else {
					val, err = f.GetValWithContext(&driver.RealizeContext{Context: context.Background()}, "swap_t", treePath)
				}
				if err != nil {
					fails.add("GetVal error: %s", err)
					atomic.AddInt64(&anomalies, 1)
					continue
				}
				if got := string(val); got != want {
					fails.add("GetVal = %q, want %q", got, want)
					atomic.AddInt64(&anomalies, 1)
				}
			}
		}(g)
	}
	wg.Wait()

	fails.report(t, atomic.LoadInt64(&anomalies))
}

// TestStressSetVsGetFunctional guards audit F2 (#66) semantics under
// load: a writer keeps replacing the root processor chain while readers
// hammer Get. Every response must be one of the two exact full values —
// never a torn merge, never a compounded chain, and once the writer
// finishes, the final value must be served.
//
// Since the #47 fix (Set/apply publishes the chain as one atomic
// snapshot) this also runs under -race as a true regression guard for
// the race itself, alongside TestStressSetProcsSwapDuringGet.
func TestStressSetVsGetFunctional(t *testing.T) {
	// lazy mode with cacheTTL==0 caches forever once realized, so Set's
	// cache invalidation (the F2 fix) is load-bearing here: without it the
	// final Get below would keep serving the first value forever.
	tree, err := NewLazyTree(newTestDriver(), "stress_set", "R",
		NewDirective("/", replaceProc(`{"v":"A"}`)),
	)
	if err != nil {
		t.Fatalf("build fail: %s", err)
	}

	const (
		valA = `{"v":"A"}`
		valB = `{"v":"B"}`
		sets = 200
	)

	stop := make(chan struct{})
	var wg sync.WaitGroup
	var fails failCollector
	var anomalies int64

	for g := 0; g < stressWorkers; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					fails.add("panic during Get: %v", r)
					atomic.AddInt64(&anomalies, 1)
				}
			}()
			for {
				select {
				case <-stop:
					return
				default:
				}
				val, err := tree.Get("/")
				if err != nil {
					fails.add("Get error: %s", err)
					atomic.AddInt64(&anomalies, 1)
					continue
				}
				// the only two legal states: the old or the new full value
				if got := string(val); got != valA && got != valB {
					fails.add("torn Get = %q, want %q or %q", got, valA, valB)
					atomic.AddInt64(&anomalies, 1)
				}
				// pace the readers so the writer's invalidateCache can
				// acquire the realize write lock without starving under
				// 64 spinning readers (Go RWMutex queues writers, but the
				// handoff is still slow under a continuous read storm)
				time.Sleep(50 * time.Microsecond)
			}
		}()
	}

	// writer: alternate A/B directives; final state is B
	for i := 0; i < sets; i++ {
		next := valA
		if i%2 == 1 {
			next = valB
		}
		if err := tree.Set(NewDirective("/", replaceProc(next))); err != nil {
			t.Errorf("Set #%d fail: %s", i, err)
		}
		time.Sleep(time.Millisecond)
	}
	close(stop)
	wg.Wait()

	// F2 contract: after Set returns, the update must be visible
	final, err := tree.Get("/")
	if err != nil {
		t.Fatalf("final Get fail: %s", err)
	}
	if got := string(final); got != valB {
		t.Errorf("final value = %q, want %q (Set update not effective)", got, valB)
	}
	fails.report(t, atomic.LoadInt64(&anomalies))
}

// TestStressCacheTTLExpiryStorm drives the cache-TTL thundering-herd face:
// after a whole-path expiry, a burst of simultaneous deep Gets must
// re-source each node EXACTLY ONCE (the double-check inside the realize
// write lock collapses the herd) and every response must be the exact
// recomposed value. The realize counters quantify the herd: one
// re-realization per node per generation regardless of worker count.
func TestStressCacheTTLExpiryStorm(t *testing.T) {
	const (
		ttl         = 200 * time.Millisecond
		ttlSleep    = 260 * time.Millisecond // cross the expiry boundary with margin
		generations = 2
		nodes       = 5 // /, a, b, c, d
	)

	var counter int32
	tree, err := NewLazyCacheTree(newTestDriver(), "ttl_storm", "R", ttl, markerTreeDirectives(&counter)...)
	if err != nil {
		t.Fatalf("build fail: %s", err)
	}

	getDeep := func() (string, error) {
		val, err := tree.Get("/a/b/c/d")
		if err != nil {
			return "", err
		}
		return string(val), nil
	}

	// generation 0: first touch realizes every node once
	first, err := getDeep()
	if err != nil {
		t.Fatalf("first get fail: %s", err)
	}
	if first != "R-x-a-b-c-d" {
		t.Fatalf("first get = %q, want %q", first, "R-x-a-b-c-d")
	}
	if got := atomic.LoadInt32(&counter); got != nodes {
		t.Fatalf("generation 0 realize count = %d, want %d", got, nodes)
	}

	var (
		fails     failCollector
		anomalies int64
		wg        sync.WaitGroup
	)
	storm := func(gen int32) {
		wg.Add(stressWorkers)
		for g := 0; g < stressWorkers; g++ {
			go func() {
				defer wg.Done()
				defer func() {
					if r := recover(); r != nil {
						fails.add("panic during storm: %v", r)
						atomic.AddInt64(&anomalies, 1)
					}
				}()
				val, err := tree.Get("/a/b/c/d")
				if err != nil {
					fails.add("storm get error: %s", err)
					atomic.AddInt64(&anomalies, 1)
					return
				}
				if got := string(val); got != "R-x-a-b-c-d" {
					fails.add("storm get = %q, want %q", got, "R-x-a-b-c-d")
					atomic.AddInt64(&anomalies, 1)
				}
			}()
		}
		wg.Wait()

		want := nodes * (gen + 1)
		if got := atomic.LoadInt32(&counter); got != want {
			t.Errorf("after storm %d realize count = %d, want %d (herd not collapsed or content compounded)", gen+1, got, want)
		}
		requests := stressWorkers
		t.Logf("storm %d: %d concurrent requests → %d re-realizations per node (herd ratio %.3f)",
			gen+1, requests, 1, float64(1)/float64(requests))
	}

	for gen := int32(1); gen <= generations; gen++ {
		time.Sleep(ttlSleep) // let the whole path expire
		storm(gen)
	}
	fails.report(t, atomic.LoadInt64(&anomalies))
}

// TestStressParamAwareConcurrent guards the H4 (#67) param-aware contract
// under concurrent distinct params on the SAME node: every requester must
// receive its OWN rendering and the node cache must stay param-independent
// (the static base with placeholders intact). Both the plain
// TemplateProcessor chain and the CombinedProcessor-hiding-a-template
// chain are exercised — the latter is the exact H4 regression face.
func TestStressParamAwareConcurrent(t *testing.T) {
	t.Run("template-chain", func(t *testing.T) {
		seed := &driver.RawProcessor{Proc: func(_ *driver.RealizeContext, _ []byte) ([]byte, error) {
			return []byte(`{"u":"${user}"}`), nil
		}}
		tree, err := NewLazyTree(newTestDriver(), "param_storm", "",
			NewDirective("/api/user", seed, &driver.TemplateProcessor{}),
		)
		if err != nil {
			t.Fatalf("build fail: %s", err)
		}

		render := func(user string) string { return `{"u":"` + user + `"}` }

		stormParams(t, tree, "/api/user", render)

		// cache must be unpolluted: the static layer keeps the placeholder
		val, err := tree.Get("/api/user")
		if err != nil {
			t.Fatalf("static get fail: %s", err)
		}
		if got := string(val); got != `{"u":"${user}"}` {
			t.Errorf("static base polluted by dynamic layer: %q, want placeholder form", got)
		}
	})

	t.Run("combined-chain", func(t *testing.T) {
		// H4 regression face: the template hides inside a CombinedProcessor.
		// IsParamAware must look through it, keeping the whole chain in the
		// dynamic layer; otherwise the first request's params are cached and
		// served to every concurrent requester with different params.
		seed := &driver.RawProcessor{Proc: func(_ *driver.RealizeContext, _ []byte) ([]byte, error) {
			return []byte(`u=${user}`), nil
		}}
		marker := &driver.RawProcessor{Proc: func(_ *driver.RealizeContext, before []byte) ([]byte, error) {
			out := make([]byte, 0, len(before)+3)
			out = append(out, before...)
			return append(out, "|mk"...), nil
		}}
		combined := driver.CombineProcessor(seed, &driver.TemplateProcessor{}, marker)

		tree, err := NewLazyTree(newTestDriver(), "param_storm_combined", "",
			NewDirective("/api/user", combined),
		)
		if err != nil {
			t.Fatalf("build fail: %s", err)
		}

		render := func(user string) string { return "u=" + user + "|mk" }

		stormParams(t, tree, "/api/user", render)

		// the static layer of an all-dynamic chain is the inherited base
		val, err := tree.Get("/api/user")
		if err != nil {
			t.Fatalf("static get fail: %s", err)
		}
		if got := string(val); got != "" {
			t.Errorf("static base polluted by dynamic layer: %q, want empty base", got)
		}
	})
}

// stormParams drives stressWorkers×stressRounds GetWithContext calls with
// a UNIQUE param per call and asserts every response matches the caller's
// own rendering.
func stormParams(t *testing.T, tree Tree, path string, render func(user string) string) {
	t.Helper()
	var (
		fails     failCollector
		anomalies int64
		wg        sync.WaitGroup
	)
	for g := 0; g < stressWorkers; g++ {
		wg.Add(1)
		go func(gid int) {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					fails.add("panic during param storm: %v", r)
					atomic.AddInt64(&anomalies, 1)
				}
			}()
			for r := 0; r < stressRounds; r++ {
				user := fmt.Sprintf("u%d-%d", gid, r)
				rc := &driver.RealizeContext{
					Context: context.Background(),
					Params:  map[string]string{"user": user},
				}
				val, err := tree.GetWithContext(rc, path)
				if err != nil {
					fails.add("GetWithContext error: %s", err)
					atomic.AddInt64(&anomalies, 1)
					continue
				}
				if got, want := string(val), render(user); got != want {
					// a leaked rendering from another request is the H4 face
					fails.add("param leak: got %q, want own rendering %q", got, want)
					atomic.AddInt64(&anomalies, 1)
				}
			}
		}(g)
	}
	wg.Wait()
	fails.report(t, atomic.LoadInt64(&anomalies))
}

// TestStressRateLimiterDeterministic pins the allow() concurrency
// semantics with an exactly-once assertion: a limiter of rate 0 / burst 1
// admits exactly ONE of 64 concurrent Gets; every other Get must fail with
// ErrRateLimited and the sole winner is the only realization (instant mode
// re-realizes on every admitted access).
func TestStressRateLimiterDeterministic(t *testing.T) {
	var counter int32
	tree, err := NewLazyInstantTree(newTestDriver(), "rl_determ", "R",
		NewDirective("/", countingAppend(&counter, "-x")),
	)
	if err != nil {
		t.Fatalf("build fail: %s", err)
	}
	tree.SetRateLimit(rate.Limit(0), 1) // one token, no refill

	var (
		okCount   int32
		limited   int32
		other     int32
		contentOk int32
		wg        sync.WaitGroup
	)
	for g := 0; g < stressWorkers; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			val, err := tree.Get("/")
			switch {
			case err == nil:
				atomic.AddInt32(&okCount, 1)
				if string(val) == "R-x" {
					atomic.AddInt32(&contentOk, 1)
				}
			case errors.Is(err, ErrRateLimited):
				atomic.AddInt32(&limited, 1)
			default:
				atomic.AddInt32(&other, 1)
			}
		}()
	}
	wg.Wait()

	if okCount != 1 || limited != stressWorkers-1 || other != 0 {
		t.Errorf("rate 0/burst 1 under %d goroutines: ok=%d limited=%d other=%d, want ok=1 limited=%d other=0",
			stressWorkers, okCount, limited, other, stressWorkers-1)
	}
	if okCount != contentOk {
		t.Errorf("the admitted request must return exact content: ok=%d contentOk=%d", okCount, contentOk)
	}
	if got := atomic.LoadInt32(&counter); got != 1 {
		t.Errorf("realize count = %d, want exactly 1 (only the admitted request realizes)", got)
	}
}

// TestStressRateLimiterSwapConsistency flips SetRateLimit while Gets run.
// Each swap installs a NEW limiter instance under rlMu, so the read side
// (allow()) is race-free; with an instant-mode tree every ADMITTED Get
// realizes exactly once, which yields the invariant realizeCount ==
// admittedCount regardless of how the flips interleave. Every rejected Get
// must carry ErrRateLimited.
func TestStressRateLimiterSwapConsistency(t *testing.T) {
	var counter int32
	tree, err := NewLazyInstantTree(newTestDriver(), "rl_swap", "R",
		NewDirective("/", countingAppend(&counter, "-x")),
	)
	if err != nil {
		t.Fatalf("build fail: %s", err)
	}

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { // limiter flipper
		defer wg.Done()
		defer close(stop)
		for i := 0; i < 200; i++ {
			if i%2 == 0 {
				tree.SetRateLimit(rate.Inf, 0) // admit everything
			} else {
				tree.SetRateLimit(rate.Limit(0), 1) // fresh single token
			}
			time.Sleep(time.Millisecond)
		}
	}()

	var (
		admitted int32
		limited  int32
		other    int32
	)
	for g := 0; g < stressWorkers; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				_, err := tree.Get("/")
				switch {
				case err == nil:
					atomic.AddInt32(&admitted, 1)
				case errors.Is(err, ErrRateLimited):
					atomic.AddInt32(&limited, 1)
				default:
					atomic.AddInt32(&other, 1)
				}
			}
		}()
	}
	wg.Wait()

	if other != 0 {
		t.Errorf("unexpected error kinds observed: %d", other)
	}
	if admitted == 0 {
		t.Error("every limiter flavor admitted zero requests; flipper/starvation bug")
	}
	if got := atomic.LoadInt32(&counter); got != admitted {
		t.Errorf("realize count %d != admitted count %d (limiter/realize invariant broken)", got, admitted)
	}
	t.Logf("admitted=%d limited=%d", admitted, limited)
}

// --- Set-family races (#47, FIXED) and the remaining known gap (#48) ----
//
// The three #47 regression guards below used to be env-gated probes that
// demonstrated the unsynchronized SetFallback/SetDefaultContext/Set-apply
// writes (1038 DATA RACE warnings on a 64-goroutine load). The fix
// publishes procs/dynamicFrom/fallback/defaultCtx atomically, so they now
// run unconditionally — including under -race in CI.
//
// #48 (parent TTL refresh leaves a stale child composition) is still
// open: its probe stays opt-in behind IVY_STRESS_UNFIXED=1 and encodes the
// FIXED contract, so it FAILS when run manually:
//
//	IVY_STRESS_UNFIXED=1 go test -run 'TestStressKnownRace48' -v .
//	    → FAIL "served stale composition" (child TTL pins old parent
//	      content while the parent already re-realized)

func skipUnlessUnfixedStress(t *testing.T, issue string) {
	t.Helper()
	if os.Getenv("IVY_STRESS_UNFIXED") == "" {
		t.Skipf("known open race/semantics gap, see #%s; set IVY_STRESS_UNFIXED=1 to run this regression probe", issue)
	}
}

// TestStressSetFallbackDuringGet guards #47: SetFallback swaps the
// fallback processor while concurrent Gets hit a missing path (fallback
// fires on every read). The swap is atomic; every response must carry the
// fallback marker and stay -race clean.
func TestStressSetFallbackDuringGet(t *testing.T) {
	tree, err := NewLazyTree(newTestDriver(), "race47_fb", "R",
		NewDirective("/a/b", replaceProc("AB")),
	)
	if err != nil {
		t.Fatalf("build fail: %s", err)
	}

	stop := make(chan struct{})
	var wg sync.WaitGroup
	var fails failCollector
	var anomalies int64
	for g := 0; g < stressWorkers; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				val, err := tree.Get("/a/zzz")
				if err != nil {
					fails.add("get fail: %s", err)
					atomic.AddInt64(&anomalies, 1)
					continue
				}
				// during the swap window the fallback may not be
				// installed yet, so both the bare and the marked
				// response are legal; anything else is corruption
				if got := string(val); got != "R" && got != "R-fb" {
					fails.add("fallback get = %q, want %q or %q", got, "R", "R-fb")
					atomic.AddInt64(&anomalies, 1)
				}
				// pace the readers: on this fully-cached lazy tree they
				// would otherwise spin at full speed and starve the
				// writer's goroutine under -race (test took >13s unpaced)
				time.Sleep(50 * time.Microsecond)
			}
		}()
	}
	for i := 0; i < 200; i++ {
		tree.SetFallback(&driver.RawProcessor{Proc: func(_ *driver.RealizeContext, before []byte) ([]byte, error) {
			return append(append([]byte{}, before...), "-fb"...), nil
		}})
		time.Sleep(time.Millisecond)
	}
	close(stop)
	wg.Wait()

	// after the last SetFallback the marker must be served
	final, err := tree.Get("/a/zzz")
	if err != nil {
		t.Fatalf("final fallback get fail: %s", err)
	}
	if got := string(final); got != "R-fb" {
		t.Errorf("final fallback get = %q, want %q", got, "R-fb")
	}
	fails.report(t, atomic.LoadInt64(&anomalies))
}

// TestStressSetDefaultContextDuringGet guards #47: SetDefaultContext
// swaps the default RealizeContext while concurrent Gets realize through
// it (instant mode re-realizes on every access). The swap is atomic and
// must stay -race clean; every Get must still serve the exact content.
func TestStressSetDefaultContextDuringGet(t *testing.T) {
	tree, err := NewLazyInstantTree(newTestDriver(), "race47_ctx", "R",
		NewDirective("/", replaceProc("A")),
	)
	if err != nil {
		t.Fatalf("build fail: %s", err)
	}

	stop := make(chan struct{})
	var wg sync.WaitGroup
	var fails failCollector
	var anomalies int64
	for g := 0; g < stressWorkers; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				val, err := tree.Get("/")
				if err != nil {
					fails.add("get fail: %s", err)
					atomic.AddInt64(&anomalies, 1)
					continue
				}
				if got := string(val); got != "A" {
					fails.add("get = %q, want %q", got, "A")
					atomic.AddInt64(&anomalies, 1)
				}
			}
		}()
	}
	for i := 0; i < 200; i++ {
		tree.SetDefaultContext(&driver.RealizeContext{Context: context.Background()})
		time.Sleep(time.Millisecond)
	}
	close(stop)
	wg.Wait()
	fails.report(t, atomic.LoadInt64(&anomalies))
}

// TestStressSetProcsSwapDuringGet guards #47: Set→apply replaces the
// processor chain while concurrent Gets read it. The (procs, dynamicFrom)
// pair is published as one atomic snapshot, so readers never observe a
// torn chain and the swap must stay -race clean; every response must be
// one of the two exact full values.
func TestStressSetProcsSwapDuringGet(t *testing.T) {
	tree, err := NewLazyInstantTree(newTestDriver(), "race47_procs", "R",
		NewDirective("/", replaceProc("A")),
	)
	if err != nil {
		t.Fatalf("build fail: %s", err)
	}

	stop := make(chan struct{})
	var wg sync.WaitGroup
	var fails failCollector
	var anomalies int64
	for g := 0; g < stressWorkers; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				val, err := tree.Get("/")
				if err != nil {
					fails.add("get fail: %s", err)
					atomic.AddInt64(&anomalies, 1)
					continue
				}
				if got := string(val); got != "A" && got != "B" {
					fails.add("torn get = %q, want %q or %q", got, "A", "B")
					atomic.AddInt64(&anomalies, 1)
				}
			}
		}()
	}
	for i := 0; i < 200; i++ {
		next := "A"
		if i%2 == 1 {
			next = "B"
		}
		if err := tree.Set(NewDirective("/", replaceProc(next))); err != nil {
			t.Fatalf("set fail: %s", err)
		}
		time.Sleep(time.Millisecond)
	}
	close(stop)
	wg.Wait()
	fails.report(t, atomic.LoadInt64(&anomalies))
}

// TestStressKnownRace48_ParentRefreshStaleChild encodes the FIXED contract
// for #48: a child must not keep serving a composition built from a parent
// realization that the parent has already superseded.
//
// The stale window is opened deterministically (no timing race): the
// parent's realizedAt is rewound under its lock — exactly what natural TTL
// expiry does — while the leaf stays fresh. On master the leaf fast-path
// then serves the OLD composition for up to a full leaf TTL.
func TestStressKnownRace48_ParentRefreshStaleChild(t *testing.T) {
	skipUnlessUnfixedStress(t, "48")

	var gen int32
	parentProc := &driver.RawProcessor{Proc: func(_ *driver.RealizeContext, _ []byte) ([]byte, error) {
		n := atomic.AddInt32(&gen, 1)
		return []byte(fmt.Sprintf("P%d", n)), nil
	}}

	tr, err := NewLazyCacheTree(newTestDriver(), "kr48_stale", "R", time.Hour,
		NewDirective("/", parentProc),
		NewDirective("/leaf", appendProc("-L")),
	)
	if err != nil {
		t.Fatalf("build fail: %s", err)
	}

	deep, err := tr.Get("/leaf")
	if err != nil {
		t.Fatalf("first get fail: %s", err)
	}
	if string(deep) != "P1-L" {
		t.Fatalf("first get = %q, want %q", deep, "P1-L")
	}

	root := tr.(*tree)
	leaf := root.pickChild("leaf").(*tree)

	// premise check: the leaf is comfortably fresh (its TTL has not
	// elapsed), which is exactly the #48 stale-window precondition.
	leaf.realizeMu.RLock()
	leafFresh := !leaf.realizedAt.IsZero() && time.Since(leaf.realizedAt) < time.Minute
	leaf.realizeMu.RUnlock()
	if !leafFresh {
		t.Fatal("premise broken: leaf not fresh within its TTL")
	}

	// rewind the parent's freshness to simulate its TTL expiry while the
	// leaf is still comfortably fresh — mirrors the natural skew where the
	// child realized strictly later than the parent.
	root.realizeMu.Lock()
	root.realizedAt = time.Now().Add(-2 * time.Hour)
	root.realizeMu.Unlock()

	fresh, err := tr.Get("/")
	if err != nil {
		t.Fatalf("root refresh get fail: %s", err)
	}
	if string(fresh) != "P2" {
		t.Fatalf("root after rewind = %q, want P2", fresh)
	}

	afterLeaf, err := tr.Get("/leaf")
	if err != nil {
		t.Fatalf("leaf get fail: %s", err)
	}
	// desired contract: leaf recomposes from the CURRENT parent content
	if string(afterLeaf) != "P2-L" {
		t.Errorf("issue #48: leaf served stale composition %q after parent refresh, want %q", afterLeaf, "P2-L")
	}
}
