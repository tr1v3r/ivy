package ivy

// Engine-layer benchmarks for the ivy tree/forest.
//
// Reproduce the numbers (numbers live in the PR body / task output, not
// here, so they cannot silently rot):
//
//	go test -run '^$' -bench . -benchmem -count=6 ./ | tee bench.txt
//
// Profiling the hotspots:
//
//	go test -run '^$' -bench . -benchtime=2s -cpuprofile cpu.out -mutexprofile mutex.out .
//	go tool pprof -top -nodecount=15 stress.test cpu.out
//	go tool pprof -top -nodecount=15 -nodefraction=0.01 stress.test mutex.out
//
// Race-detector overhead: `go test -race -bench .` typically shows a 2-10×
// ns/op slowdown on these realize-heavy loops (every admission through
// realizeMu becomes a shadow-metadata update); race numbers are useful
// only as a relative signal, never as absolute throughput.

import (
	"context"
	"testing"
	"time"

	"github.com/tr1v3r/ivy/driver"
)

// benchMarkerTree builds a lazy tree with one directive per level of
// path, each appending a distinct suffix (same shape as the stress tests,
// so benchmark and stress numbers describe the same workload).
func benchMarkerTree(b *testing.B, path string) Tree {
	b.Helper()
	dirs := []Directive{NewDirective("/", appendProc("-x"))}
	acc := ""
	for _, seg := range splitPathSegments(path) {
		acc += "/" + seg
		dirs = append(dirs, NewDirective(acc, appendProc("-"+seg)))
	}
	tree, err := NewLazyTree(newTestDriver(), "bench_tree", "R", dirs...)
	if err != nil {
		b.Fatalf("build tree fail: %s", err)
	}
	return tree
}

// splitPathSegments returns the non-empty segments of a slash path.
func splitPathSegments(path string) []string {
	var segs []string
	cur := ""
	for i := 0; i < len(path); i++ {
		if path[i] == '/' {
			if cur != "" {
				segs = append(segs, cur)
			}
			cur = ""
			continue
		}
		cur += string(path[i])
	}
	if cur != "" {
		segs = append(segs, cur)
	}
	return segs
}

// BenchmarkTreeGet measures concurrent deep-path Get throughput per mode.
// All modes read the same tree shape; the interesting deltas are the
// realize fast path (standard/lazy/TTL-hit: one lock read) versus the
// re-realize-every-access mode (instant) and the TTL-miss mode (expired on
// every access — the worst-case re-source cost).
func BenchmarkTreeGet(b *testing.B) {
	const deep = "/a/b/c/d"

	b.Run("mode=standard", func(b *testing.B) {
		b.ReportAllocs()
		tree := benchMarkerTreeStd(b, deep)
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				if _, err := tree.Get(deep); err != nil {
					b.Fatalf("get: %s", err)
				}
			}
		})
	})
	b.Run("mode=lazy", func(b *testing.B) {
		b.ReportAllocs()
		tree := benchMarkerTree(b, deep)
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				if _, err := tree.Get(deep); err != nil {
					b.Fatalf("get: %s", err)
				}
			}
		})
	})
	b.Run("mode=instant", func(b *testing.B) {
		b.ReportAllocs()
		tree, err := NewLazyInstantTree(newTestDriver(), "bench_inst", "R", benchDirectives()...)
		if err != nil {
			b.Fatalf("build: %s", err)
		}
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				if _, err := tree.Get(deep); err != nil {
					b.Fatalf("get: %s", err)
				}
			}
		})
	})
	b.Run("mode=cachettl-hit", func(b *testing.B) {
		b.ReportAllocs()
		tree, err := NewLazyCacheTree(newTestDriver(), "bench_ttl", "R", time.Hour, benchDirectives()...)
		if err != nil {
			b.Fatalf("build: %s", err)
		}
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				if _, err := tree.Get(deep); err != nil {
					b.Fatalf("get: %s", err)
				}
			}
		})
	})
	// TTL=1ns: time.Since(realizedAt) exceeds the TTL on every access, so
	// each Get re-sources the whole path — the per-request cost ceiling of
	// the cache-TTL mode (thundering-herd aftermath for a cold path).
	b.Run("mode=cachettl-miss", func(b *testing.B) {
		b.ReportAllocs()
		tree, err := NewLazyCacheTree(newTestDriver(), "bench_ttl_miss", "R", time.Nanosecond, benchDirectives()...)
		if err != nil {
			b.Fatalf("build: %s", err)
		}
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				if _, err := tree.Get(deep); err != nil {
					b.Fatalf("get: %s", err)
				}
			}
		})
	})
}

// benchDirectives returns the per-level marker directives for /a/b/c/d.
func benchDirectives() []Directive {
	return []Directive{
		NewDirective("/", appendProc("-x")),
		NewDirective("/a", appendProc("-a")),
		NewDirective("/a/b", appendProc("-b")),
		NewDirective("/a/b/c", appendProc("-c")),
		NewDirective("/a/b/c/d", appendProc("-d")),
	}
}

// benchMarkerTreeStd builds the standard-mode variant of the marker tree.
func benchMarkerTreeStd(b *testing.B, path string) Tree {
	b.Helper()
	tree, err := NewTree(newTestDriver(), "bench_std", "R", benchDirectives()...)
	if err != nil {
		b.Fatalf("build tree fail: %s", err)
	}
	return tree
}

// BenchmarkTreeGetDepth isolates traversal cost by path depth on a lazy
// tree (everything cached after the first touch): each extra level adds a
// child map lookup, a realizeMu read and a contentMu read.
func BenchmarkTreeGetDepth(b *testing.B) {
	for _, tc := range []struct{ name, path string }{
		{"depth=1", "/a"},
		{"depth=3", "/a/b/c"},
		{"depth=5", "/a/b/c/d/e"},
	} {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			tree := benchMarkerTree(b, tc.path)
			b.RunParallel(func(pb *testing.PB) {
				for pb.Next() {
					if _, err := tree.Get(tc.path); err != nil {
						b.Fatalf("get: %s", err)
					}
				}
			})
		})
	}
}

// BenchmarkParamAwareGetWithContext measures the per-request dynamic layer:
// static base from cache plus one template interpolation per call.
func BenchmarkParamAwareGetWithContext(b *testing.B) {
	b.ReportAllocs()
	seed := &driver.RawProcessor{Proc: func(_ *driver.RealizeContext, _ []byte) ([]byte, error) {
		return []byte(`{"u":"${user}","v":1}`), nil
	}}
	tree, err := NewLazyTree(newTestDriver(), "bench_param", "",
		NewDirective("/api/user", seed, &driver.TemplateProcessor{}),
	)
	if err != nil {
		b.Fatalf("build fail: %s", err)
	}

	b.RunParallel(func(pb *testing.PB) {
		// One RealizeContext per WORKER goroutine: the engine's descent
		// writes rc.TreePath/rc.ParentContent (issue #32, audit F8), so a
		// single rc shared across parallel workers would make
		// `go test -race -bench` fail with a report pointing at tree.go.
		rc := &driver.RealizeContext{
			Context: context.Background(),
			Params:  map[string]string{"user": "bench"},
		}
		for pb.Next() {
			if _, err := tree.GetWithContext(rc, "/api/user"); err != nil {
				b.Fatalf("get: %s", err)
			}
		}
	})
}

// BenchmarkForestGetVal adds the forest indirection (name lookup under
// f.mu.RLock plus the global allowGet check) on top of a cached tree Get.
func BenchmarkForestGetVal(b *testing.B) {
	b.ReportAllocs()
	tree, err := NewTree(newTestDriver(), "bench_forest", "R", benchDirectives()...)
	if err != nil {
		b.Fatalf("build fail: %s", err)
	}
	f := NewForest(func() Tree { return tree })

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if _, err := f.GetVal("bench_forest", "/a/b/c/d"); err != nil {
				b.Fatalf("getval: %s", err)
			}
		}
	})
}
