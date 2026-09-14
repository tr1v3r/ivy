package ivy

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tr1v3r/ivy/driver"
)

// TestTree_ConcurrentLazyCacheConsistency guards audit F3: under concurrent
// access to a lazy tree with cache TTL, the leaf must ALWAYS serve
// leafProcs(parentContent) — never raw parent content (its own processors
// skipped) and never a compounded chain (leafProcs applied to a previous
// leaf output).
//
// On master, inheritance (inherit()'s check-then-write) and realization
// were separate steps: under concurrent TTL expiry a node could compose
// from its own previous output ("R-b-b", compounding) or have a fresh
// result clobbered with raw parent content ("R") while realizedAt stayed
// fresh — ~2-4% of responses deviated under a 64-goroutine stress with a
// leaf processor slower than the TTL. The fix decides inheritance inside
// the realize write-locked critical section, making anomalies structurally
// impossible; the slow leaf processor keeps the historical window wide so
// this regression test stays meaningful against a revert.
func TestTree_ConcurrentLazyCacheConsistency(t *testing.T) {
	const (
		ttl     = 2 * time.Millisecond
		leafLat = 5 * time.Millisecond
	)
	slowLeaf := &driver.RawProcessor{Proc: func(_ *driver.RealizeContext, before []byte) ([]byte, error) {
		time.Sleep(leafLat) // hold realizeMu longer to expose interleavings
		out := make([]byte, 0, len(before)+2)
		out = append(out, before...)
		return append(out, "-b"...), nil
	}}

	tree, err := NewLazyCacheTree(newTestDriver(), "race_leaf", "R", ttl,
		NewDirective("/a/b", slowLeaf),
	)
	if err != nil {
		t.Fatalf("build fail: %s", err)
	}

	var total, anomalies int32
	var sample atomic.Value
	var wg sync.WaitGroup
	for g := 0; g < 64; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			deadline := time.Now().Add(1500 * time.Millisecond)
			for time.Now().Before(deadline) {
				v, err := tree.Get("/a/b")
				atomic.AddInt32(&total, 1)
				if err != nil || string(v) != "R-b" {
					if err == nil {
						sample.Store(string(v))
					}
					atomic.AddInt32(&anomalies, 1)
				}
				time.Sleep(150 * time.Microsecond)
			}
		}()
	}
	wg.Wait()

	if anomalies > 0 {
		bad, _ := sample.Load().(string)
		t.Errorf("concurrent lazy+TTL access inconsistent: %d/%d responses deviated from %q (sample %q)",
			anomalies, total, "R-b", bad)
	}
}
