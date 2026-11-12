package ivy

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/time/rate"

	"github.com/tr1v3r/ivy/driver"
)

func graftTestTree(t *testing.T, mode, template string, directives ...Directive) *tree {
	t.Helper()
	var tr Tree
	var err error
	switch mode {
	case "standard":
		tr, err = NewTree(newTestDriver(), "test", template, directives...)
	case "lazy":
		tr, err = NewLazyTree(newTestDriver(), "test", template, directives...)
	case "instant":
		tr, err = NewLazyInstantTree(newTestDriver(), "test", template, directives...)
	case "ttl":
		tr, err = NewLazyCacheTree(newTestDriver(), "test", template, time.Hour, directives...)
	default:
		t.Fatalf("unknown mode %q", mode)
	}
	if err != nil {
		t.Fatalf("build %s tree: %v", mode, err)
	}
	return tr.(*tree)
}

func graftTestGet(t *testing.T, tr Tree, path, want string) {
	t.Helper()
	got, err := tr.Get(path)
	if err != nil || string(got) != want {
		t.Fatalf("Get(%q) = %q, %v; want %q", path, got, err, want)
	}
}

func graftTestCountProc(n *atomic.Int32, suffix string) driver.Processor {
	appendSuffix := appendProc(suffix)
	return &driver.RawProcessor{Proc: func(rc *driver.RealizeContext, before []byte) ([]byte, error) {
		n.Add(1)
		return appendSuffix.Process(rc, before)
	}}
}

func graftTestExpire(tr *tree) {
	tr.realizeMu.Lock()
	tr.realizedAt = time.Now().Add(-2 * time.Hour)
	tr.realizeMu.Unlock()
}

func TestGraftAt_RebaseAndReplace(t *testing.T) {
	for _, tc := range []struct {
		name, path, want string
	}{
		{"new_direct_child", "/new", "D-s-l-t"},
		{"replace_direct_child", "/old", "D-s-l-t"},
		{"replace_deep_child", "/parent/old", "D-p-s-l-t"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dst := graftTestTree(t, "standard", "D",
				NewDirective("/old/stale", appendProc("-old")),
				NewDirective("/parent", appendProc("-p")),
				NewDirective("/parent/old/stale", appendProc("-old")),
			)
			src := graftTestTree(t, "lazy", "SOURCE",
				NewDirective("/", appendProc("-s")),
				NewDirective("/leaf", appendProc("-l")),
				NewDirective("/leaf/tip", appendProc("-t")),
			)
			graftTestGet(t, src, "/leaf/tip", "SOURCE-s-l-t")
			if err := GraftAt(dst, tc.path, src); err != nil {
				t.Fatal(err)
			}
			graftTestGet(t, dst, tc.path+"/leaf/tip", tc.want)
			if dst.Has(tc.path + "/stale") {
				t.Fatal("replacement retained a prior descendant")
			}
			// Every level must be rebased, not only the mounted root.
			q := newPathQuery(dst.driver, tc.path+"/leaf/tip")
			node := dst
			for level := 1; level <= q.level(); level++ {
				child, ok := node.pickChild(q.name(level)).(*tree)
				if !ok {
					t.Fatalf("missing native child at level %d", level)
				}
				wantPath := node.driver.AppendPath(node.Path(), q.name(level))
				if child.level != level || child.Path() != wantPath {
					t.Fatalf("child metadata = (%q, %d); want (%q, %d)", child.Path(), child.level, wantPath, level)
				}
				node = child
			}
			if err := dst.Set(NewDirective(tc.path+"/added", appendProc("-added"))); err != nil {
				t.Fatal(err)
			}
			if !dst.Has(tc.path + "/added") {
				t.Fatal("Set did not use rebased destination levels")
			}
			parentResult := "D-s"
			if tc.path == "/parent/old" {
				parentResult = "D-p-s"
			}
			graftTestGet(t, dst, tc.path+"/added", parentResult+"-added")
			if err := dst.Del(tc.path + "/leaf/tip"); err != nil {
				t.Fatal(err)
			}
			if dst.Has(tc.path + "/leaf/tip") {
				t.Fatal("Del did not use rebased destination levels")
			}
			if src.Path() != "" || src.level != 0 || src.Has("/added") {
				t.Fatal("mount or destination Set modified the source topology")
			}
			graftTestGet(t, src, "/leaf/tip", "SOURCE-s-l-t")
		})
	}
}

func TestGraftAt_SourceReuseAndSnapshot(t *testing.T) {
	proc := appendProc("-s")
	src := graftTestTree(t, "standard", "SOURCE",
		NewDirective("/", proc), NewDirective("/leaf", appendProc("-l")))
	graftTestGet(t, src, "/leaf", "SOURCE-s-l")
	a := graftTestTree(t, "standard", "A")
	b := graftTestTree(t, "standard", "B")
	if err := GraftAt(a, "/one", src); err != nil {
		t.Fatal(err)
	}
	if err := GraftAt(b, "/two", src); err != nil {
		t.Fatal(err)
	}
	ac := a.pickChild("one").(*tree)
	bc := b.pickChild("two").(*tree)
	if ac == src || bc == src || ac == bc || ac.pickChild("leaf") == src.pickChild("leaf") {
		t.Fatal("source topology was attached by reference rather than copied")
	}
	if ac.procs[0] != proc || bc.procs[0] != proc {
		t.Fatal("processor references were not preserved")
	}
	graftTestGet(t, a, "/one/leaf", "A-s-l")
	graftTestGet(t, b, "/two/leaf", "B-s-l")
	if err := src.Set(NewDirective("/", appendProc("-changed"))); err != nil {
		t.Fatal(err)
	}
	if err := src.Del("/leaf"); err != nil {
		t.Fatal(err)
	}
	graftTestGet(t, a, "/one/leaf", "A-s-l")
	graftTestGet(t, b, "/two/leaf", "B-s-l")
	graftTestGet(t, src, "/", "SOURCE-changed")
}

func TestGraftAt_AllModePairs(t *testing.T) {
	modes := []string{"standard", "lazy", "instant", "ttl"}
	for _, parentMode := range modes {
		for _, childMode := range modes {
			t.Run(parentMode+"/"+childMode, func(t *testing.T) {
				var parentRuns, childRuns atomic.Int32
				dst := graftTestTree(t, parentMode, "D", NewDirective("/", graftTestCountProc(&parentRuns, "-p")))
				src := graftTestTree(t, childMode, "SOURCE", NewDirective("/", graftTestCountProc(&childRuns, "-c")))
				// A realized source must not transplant its cached SOURCE content.
				graftTestGet(t, src, "/", "SOURCE-c")
				childRuns.Store(0)
				if err := GraftAt(dst, "/mount", src); err != nil {
					t.Fatal(err)
				}
				mounted := dst.pickChild("mount").(*tree)
				if mounted.lazyMode != src.lazyMode || mounted.instantMode != src.instantMode || mounted.cacheTTL != src.cacheTTL {
					t.Fatal("mount changed source evaluation mode")
				}
				wantAtMount := int32(0)
				if parentMode == "standard" && childMode == "standard" {
					wantAtMount = 1
				}
				if got := childRuns.Load(); got != wantAtMount {
					t.Fatalf("child executions at mount = %d; want %d", got, wantAtMount)
				}
				graftTestGet(t, dst, "/mount", "D-p-c")
				if parentRuns.Load() != 1 || childRuns.Load() != 1 {
					t.Fatalf("after first query parent/child runs = %d/%d; want 1/1", parentRuns.Load(), childRuns.Load())
				}
				graftTestGet(t, dst, "/mount", "D-p-c")
				wantParent, wantChild := int32(1), int32(1)
				if parentMode == "instant" {
					wantParent = 2
				}
				if parentMode == "instant" || childMode == "instant" {
					wantChild = 2
				}
				if parentRuns.Load() != wantParent || childRuns.Load() != wantChild {
					t.Fatalf("after repeated query runs = %d/%d; want %d/%d", parentRuns.Load(), childRuns.Load(), wantParent, wantChild)
				}
				if parentMode == "ttl" {
					graftTestExpire(dst)
				}
				if childMode == "ttl" {
					graftTestExpire(mounted)
				}
				graftTestGet(t, dst, "/mount", "D-p-c")
				if parentMode == "instant" || parentMode == "ttl" {
					wantParent++
				}
				if parentMode == "instant" || parentMode == "ttl" || childMode == "instant" || childMode == "ttl" {
					wantChild++
				}
				if parentRuns.Load() != wantParent || childRuns.Load() != wantChild {
					t.Fatalf("after forced TTL expiry runs = %d/%d; want %d/%d", parentRuns.Load(), childRuns.Load(), wantParent, wantChild)
				}
			})
		}
	}
}

func TestGraftAt_DeferredParentRefreshesStandardDescendants(t *testing.T) {
	for _, mode := range []string{"lazy", "ttl"} {
		t.Run(mode, func(t *testing.T) {
			var parentRuns, childRuns, leafRuns atomic.Int32
			parentProc := &driver.RawProcessor{Proc: func(_ *driver.RealizeContext, _ []byte) ([]byte, error) {
				return []byte(fmt.Sprintf("P%d", parentRuns.Add(1))), nil
			}}
			dst := graftTestTree(t, mode, "D", NewDirective("/", parentProc))
			src := graftTestTree(t, "standard", "SOURCE",
				NewDirective("/", graftTestCountProc(&childRuns, "-s")),
				NewDirective("/leaf", graftTestCountProc(&leafRuns, "-l")))
			childRuns.Store(0)
			leafRuns.Store(0)
			if err := GraftAt(dst, "/mount", src); err != nil {
				t.Fatal(err)
			}
			if parentRuns.Load() != 0 || childRuns.Load() != 0 || leafRuns.Load() != 0 {
				t.Fatal("standard descendants were eagerly evaluated below a deferred ancestor")
			}
			graftTestGet(t, dst, "/mount/leaf", "P1-s-l")
			graftTestGet(t, dst, "/mount/leaf", "P1-s-l")
			if mode == "ttl" {
				graftTestExpire(dst)
			} else if err := dst.Set(NewDirective("/", parentProc)); err != nil {
				t.Fatal(err)
			}
			graftTestGet(t, dst, "/mount/leaf", "P2-s-l")
			if childRuns.Load() != 2 || leafRuns.Load() != 2 {
				t.Fatalf("standard subtree did not recompose exactly once: child/leaf = %d/%d", childRuns.Load(), leafRuns.Load())
			}
		})
	}
}

func TestGraftAt_NilParentContentIsInherited(t *testing.T) {
	var returnNil atomic.Bool
	dst := graftTestTree(t, "instant", "D", NewDirective("/", &driver.RawProcessor{
		Proc: func(_ *driver.RealizeContext, _ []byte) ([]byte, error) {
			if returnNil.Load() {
				return nil, nil
			}
			return []byte("BEFORE"), nil
		},
	}))
	var nilInputs atomic.Int32
	src := graftTestTree(t, "standard", "SOURCE",
		NewDirective("/", &driver.RawProcessor{Proc: func(rc *driver.RealizeContext, before []byte) ([]byte, error) {
			if before == nil {
				nilInputs.Add(1)
			}
			return appendProc("-s").Process(rc, before)
		}}),
		NewDirective("/leaf", appendProc("-l")))
	if err := GraftAt(dst, "/mount", src); err != nil {
		t.Fatal(err)
	}
	graftTestGet(t, dst, "/mount/leaf", "BEFORE-s-l")
	returnNil.Store(true)
	graftTestGet(t, dst, "/mount/leaf", "-s-l")
	if nilInputs.Load() != 1 {
		t.Fatalf("mounted node saw nil input %d times; want once", nilInputs.Load())
	}
}

func TestGraftAt_DynamicParamsRemainIsolated(t *testing.T) {
	var staticRuns atomic.Int32
	dst := graftTestTree(t, "standard", "hello ${user}")
	src := graftTestTree(t, "lazy", "SOURCE",
		NewDirective("/", graftTestCountProc(&staticRuns, "!"), &driver.TemplateProcessor{}))
	if err := GraftAt(dst, "/mount", src); err != nil {
		t.Fatal(err)
	}
	for _, user := range []string{"alice", "bob", "alice"} {
		got, err := dst.GetWithContext(paramRC(map[string]string{"user": user}), "/mount")
		if err != nil || string(got) != "hello "+user+"!" {
			t.Fatalf("query %s = %q, %v", user, got, err)
		}
	}
	graftTestGet(t, dst, "/mount", "hello ${user}!")
	if staticRuns.Load() != 1 {
		t.Fatalf("static prefix ran %d times; want once", staticRuns.Load())
	}
	mounted := dst.pickChild("mount").(*tree)
	if string(mounted.get()) != "hello ${user}!" {
		t.Fatalf("dynamic query polluted mounted cache: %q", mounted.get())
	}
	graftTestGet(t, src, "/", "SOURCE!")
}

type graftTestParamProcessor struct{ *driver.RawProcessor }

func (*graftTestParamProcessor) ParamKeys() []string { return []string{"user"} }

func TestGraftAt_DynamicAndFallbackMutationCannotCorruptCaches(t *testing.T) {
	for _, kind := range []string{"dynamic", "fallback"} {
		t.Run(kind, func(t *testing.T) {
			mutate := &driver.RawProcessor{Proc: func(rc *driver.RealizeContext, before []byte) ([]byte, error) {
				if len(before) != 0 {
					before[0] = 'X'
				}
				if rc != nil && len(rc.ParentContent) != 0 {
					rc.ParentContent[0] = 'Y'
				}
				return before, nil
			}}
			procs := []driver.Processor{appendProc("-static")}
			if kind == "dynamic" {
				procs = append(procs, &graftTestParamProcessor{mutate})
			}
			src := graftTestTree(t, "lazy", "SOURCE", NewDirective("/", procs...))
			if kind == "fallback" {
				src.SetFallback(mutate)
			}
			graftTestGet(t, src, "/", "SOURCE-static")
			dst := graftTestTree(t, "standard", "DEST")
			if err := GraftAt(dst, "/mount", src); err != nil {
				t.Fatal(err)
			}
			path := "/mount"
			if kind == "fallback" {
				path += "/missing"
			}
			for i := 0; i < 2; i++ {
				out, err := dst.GetWithContext(paramRC(map[string]string{"user": "alice"}), path)
				if err != nil || string(out) != "XEST-static" {
					t.Fatalf("mutating %s result = %q, %v", kind, out, err)
				}
				graftTestGet(t, dst, "/", "DEST")
				graftTestGet(t, dst, "/mount", "DEST-static")
				graftTestGet(t, src, "/", "SOURCE-static")
			}
		})
	}
}

type graftTestSourceDriver struct {
	*testPathDriver
	calls *atomic.Int32
}

func (*graftTestSourceDriver) Name() string { return "graft-source" }
func (d *graftTestSourceDriver) Realize(rc *driver.RealizeContext, before []byte, procs ...driver.Processor) ([]byte, error) {
	d.calls.Add(1)
	return d.StdRealizer.Realize(rc, before, procs...)
}

func TestGraftAt_RetainsSourceRealizerButUsesDestinationPaths(t *testing.T) {
	var calls atomic.Int32
	sourceDriver := &graftTestSourceDriver{
		testPathDriver: &testPathDriver{Modem: driver.DummyModem, DelimiterPathParser: new(driver.DelimiterPathParser).WithDelimiter(".")},
		calls:          &calls,
	}
	src, err := NewLazyTree(sourceDriver, "source", "SOURCE",
		NewDirective(".", appendProc("-s")), NewDirective(".leaf", appendProc("-l")))
	if err != nil {
		t.Fatal(err)
	}
	dst := graftTestTree(t, "standard", "D", NewDirective("/parent", appendProc("-p")))
	if err := GraftAt(dst, "/parent/mount", src); err != nil {
		t.Fatal(err)
	}
	graftTestGet(t, dst, "/parent/mount/leaf", "D-p-s-l")
	if calls.Load() != 2 {
		t.Fatalf("source realizer calls = %d; want 2", calls.Load())
	}
	if err := dst.Set(NewDirective("/parent/mount/added", appendProc("-a"))); err != nil {
		t.Fatal(err)
	}
	graftTestGet(t, dst, "/parent/mount/added", "D-p-s-a")
	if !dst.Has("/parent/mount/added") {
		t.Fatal("mounted source driver still parses destination paths with its original delimiter")
	}
	graftTestGet(t, src, ".leaf", "SOURCE-s-l")
}

func TestGraftAt_PreservesFallbackContextAndIndependentLimiter(t *testing.T) {
	type contextKey struct{}
	src := graftTestTree(t, "lazy", "SOURCE", NewDirective("/", &driver.RawProcessor{
		Proc: func(rc *driver.RealizeContext, before []byte) ([]byte, error) {
			value, _ := rc.Value(contextKey{}).(string)
			return appendProc("-"+value).Process(rc, before)
		},
	}))
	src.SetDefaultContext(&driver.RealizeContext{Context: context.WithValue(context.Background(), contextKey{}, "source-context")})
	src.SetFallback(appendProc("-fallback"))
	src.SetRateLimit(rate.Limit(0), 1)
	dst := graftTestTree(t, "standard", "D")
	for _, name := range []string{"one", "two"} {
		if err := GraftAt(dst, "/"+name, src); err != nil {
			t.Fatal(err)
		}
		graftTestGet(t, dst, "/"+name, "D-source-context")
		graftTestGet(t, dst, "/"+name+"/missing", "D-source-context-fallback")
	}
	one := dst.pickChild("one").(*tree)
	two := dst.pickChild("two").(*tree)
	if one.rateLimiter == nil || one.rateLimiter == two.rateLimiter || one.rateLimiter == src.rateLimiter {
		t.Fatal("rate limiters must preserve settings without sharing token state")
	}
	graftTestGet(t, src, "/", "SOURCE-source-context")
	// Lazy mode has no TTL; explicit invalidation requires another realization.
	one.realizeMu.Lock()
	one.realizedAt = time.Time{}
	one.realizeMu.Unlock()
	if _, err := dst.Get("/one"); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("clone lost rate limit settings: got %v", err)
	}
	graftTestGet(t, dst, "/two", "D-source-context")
}

type graftTestCustomTree struct{ Tree }

func TestGraftAt_RejectsInvalidInputsWithoutChangingDestination(t *testing.T) {
	dst := graftTestTree(t, "standard", "D", NewDirective("/keep", appendProc("-keep")))
	src := graftTestTree(t, "lazy", "SOURCE", NewDirective("/", appendProc("-s")))
	var nilNative *tree
	for _, tc := range []struct {
		name string
		dst  Tree
		path string
		src  Tree
	}{
		{"nil_destination", nil, "/mount", src},
		{"typed_nil_destination", nilNative, "/mount", src},
		{"nil_source", dst, "/mount", nil},
		{"typed_nil_source", dst, "/mount", nilNative},
		{"custom_destination", &graftTestCustomTree{dst}, "/mount", src},
		{"custom_source", dst, "/mount", &graftTestCustomTree{src}},
		{"subtree_destination", dst.pickChild("keep"), "/keep/mount", src},
		{"root", dst, "/", src},
		{"empty_root", dst, "", src},
		{"missing_parent", dst, "/missing/mount", src},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := GraftAt(tc.dst, tc.path, tc.src); err == nil {
				t.Fatal("expected error")
			}
			graftTestGet(t, dst, "/keep", "D-keep")
			if dst.Has("/mount") || dst.Has("/missing") || dst.Has("/keep/mount") {
				t.Fatal("invalid mount modified the destination topology")
			}
		})
	}
}

func TestGraftAt_SetStandardNodesBeforeDeferredParentIsRealized(t *testing.T) {
	var parentRuns, updatedRuns, addedRuns atomic.Int32
	dst := graftTestTree(t, "lazy", "D", NewDirective("/", graftTestCountProc(&parentRuns, "-p")))
	src := graftTestTree(t, "standard", "SOURCE",
		NewDirective("/", appendProc("-s")), NewDirective("/leaf", appendProc("-l")))
	if err := GraftAt(dst, "/mount", src); err != nil {
		t.Fatal(err)
	}
	if err := dst.Set(NewDirective("/mount", graftTestCountProc(&updatedRuns, "-updated"))); err != nil {
		t.Fatal(err)
	}
	if err := dst.Set(NewDirective("/mount/added", graftTestCountProc(&addedRuns, "-added"))); err != nil {
		t.Fatal(err)
	}
	if parentRuns.Load() != 0 || updatedRuns.Load() != 0 || addedRuns.Load() != 0 {
		t.Fatal("Set eagerly evaluated a standard node before its deferred parent was known")
	}
	graftTestGet(t, dst, "/mount/added", "D-p-updated-added")
	graftTestGet(t, dst, "/mount/leaf", "D-p-updated-l")
	if updatedRuns.Load() != 1 || addedRuns.Load() != 1 {
		t.Fatalf("deferred Set executions = %d/%d; want 1/1", updatedRuns.Load(), addedRuns.Load())
	}
	graftTestGet(t, src, "/leaf", "SOURCE-s-l")
}

func TestGraftAt_EagerFailureDoesNotPublishOrMutateParent(t *testing.T) {
	wantErr := errors.New("failed eager processor")
	src := graftTestTree(t, "standard", "SOURCE", NewDirective("/", &driver.RawProcessor{
		Proc: func(_ *driver.RealizeContext, before []byte) ([]byte, error) {
			if string(before) == "DEST" {
				// A failed processor must not corrupt the destination's slice.
				before[0] = 'X'
				return nil, wantErr
			}
			return before, nil
		},
	}))
	dst := graftTestTree(t, "standard", "DEST",
		NewDirective("/mount", appendProc("-old")),
		NewDirective("/mount/leaf", appendProc("-leaf")))
	old := dst.pickChild("mount")
	if err := GraftAt(dst, "/mount", src); !errors.Is(err, wantErr) {
		t.Fatalf("mount error = %v; want %v", err, wantErr)
	}
	if dst.pickChild("mount") != old {
		t.Fatal("failed eager preparation published its replacement")
	}
	graftTestGet(t, dst, "/", "DEST")
	graftTestGet(t, dst, "/mount/leaf", "DEST-old-leaf")
	graftTestGet(t, src, "/", "SOURCE")
}

func TestGraftAt_InPlaceProcessorCannotMutateParentOrSourceCache(t *testing.T) {
	src := graftTestTree(t, "lazy", "SOURCE", NewDirective("/", &driver.RawProcessor{
		Proc: func(_ *driver.RealizeContext, before []byte) ([]byte, error) {
			if len(before) != 0 {
				before[0] = 'X'
			}
			return before, nil
		},
	}))
	graftTestGet(t, src, "/", "XOURCE")
	dst := graftTestTree(t, "standard", "DEST")
	for _, name := range []string{"one", "two"} {
		if err := GraftAt(dst, "/"+name, src); err != nil {
			t.Fatal(err)
		}
		graftTestGet(t, dst, "/"+name, "XEST")
		graftTestGet(t, dst, "/", "DEST")
		graftTestGet(t, src, "/", "XOURCE")
	}
}

func TestGraftAt_RejectsMalformedSourceTopology(t *testing.T) {
	for _, kind := range []string{"cycle", "typed_nil_child", "custom_child"} {
		t.Run(kind, func(t *testing.T) {
			dst := graftTestTree(t, "standard", "D", NewDirective("/mount", appendProc("-old")))
			src := graftTestTree(t, "lazy", "SOURCE")
			switch kind {
			case "cycle":
				src.Graft(src)
			case "typed_nil_child":
				var nilChild *tree
				src.children["bad"] = nilChild
			case "custom_child":
				src.Graft(&graftTestCustomTree{graftTestTree(t, "lazy", "CUSTOM")})
			}
			old := dst.pickChild("mount")
			if err := GraftAt(dst, "/mount", src); err == nil {
				t.Fatal("expected malformed-source error")
			}
			if dst.pickChild("mount") != old {
				t.Fatal("invalid source replaced the destination")
			}
			graftTestGet(t, dst, "/mount", "D-old")
		})
	}
}

func TestGraftAt_DoesNotChangeLegacyGraft(t *testing.T) {
	dst := graftTestTree(t, "standard", "D")
	src := graftTestTree(t, "lazy", "SOURCE")
	dst.Graft(src)
	if dst.pickChild(src.Name()) != src || src.Path() != "" || src.level != 0 {
		t.Fatal("legacy Graft must remain a raw attachment without copying or rebasing")
	}
}

func TestGraftAt_ConcurrentReplacementAndReads(t *testing.T) {
	dst := graftTestTree(t, "standard", "D")
	a := graftTestTree(t, "lazy", "SOURCE-A", NewDirective("/", appendProc("-A")), NewDirective("/leaf", appendProc("-a")))
	b := graftTestTree(t, "lazy", "SOURCE-B", NewDirective("/", appendProc("-B")), NewDirective("/leaf", appendProc("-b")))
	if err := GraftAt(dst, "/mount", a); err != nil {
		t.Fatal(err)
	}
	const readers, writers = 4, 2
	// Structural writers are caller-serialized; readers intentionally are not.
	var structuralMu sync.Mutex
	start := make(chan struct{})
	errs := make(chan error, readers+writers)
	var wg sync.WaitGroup
	for worker := 0; worker < readers; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for i := 0; i < 300; i++ {
				got, err := dst.Get("/mount/leaf")
				if err != nil {
					errs <- fmt.Errorf("replacement read failed: %w", err)
					return
				}
				if string(got) != "D-A-a" && string(got) != "D-B-b" {
					errs <- fmt.Errorf("torn replacement read = %q", got)
					return
				}
			}
		}()
	}
	for worker := 0; worker < writers; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for i := 0; i < 100; i++ {
				source := a
				if i%2 == 1 {
					source = b
				}
				structuralMu.Lock()
				err := GraftAt(dst, "/mount", source)
				structuralMu.Unlock()
				if err != nil {
					errs <- fmt.Errorf("concurrent mount: %w", err)
					return
				}
			}
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	graftTestGet(t, a, "/leaf", "SOURCE-A-A-a")
	graftTestGet(t, b, "/leaf", "SOURCE-B-B-b")
}
