package ivy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

func TestGraft_DirectIdentityAliasAndLocalOperations(t *testing.T) {
	dst := graftTestTree(t, "standard", "D",
		NewDirective("/parent", appendProc("-p")),
		NewDirective("/parent/alias/stale", appendProc("-old")))
	src := graftTestTree(t, "lazy", "SOURCE",
		NewDirective("/", appendProc("-s")),
		NewDirective("/leaf", appendProc("-l")),
		NewDirective("/leaf/tip", appendProc("-t")))
	leaf := src.pickChild("leaf")

	graftTestGet(t, src, "/leaf/tip", "SOURCE-s-l-t")

	if err := dst.Graft("/parent/alias", src); err != nil {
		t.Fatal(err)
	}

	parent := dst.pickChild("parent").(*tree)
	if parent.pickChild("alias") != src || src.pickChild("leaf") != leaf {
		t.Fatal("Graft copied source nodes rather than attaching the same objects")
	}

	if src.Name() != "test" || src.Path() != "/parent/alias" || leaf.Path() != "/parent/alias/leaf" {
		t.Fatalf("identity/path mismatch: name=%q source=%q leaf=%q", src.Name(), src.Path(), leaf.Path())
	}

	graftTestGet(t, dst, "/parent/alias/leaf/tip", "D-p-s-l-t")
	graftTestGet(t, src, "/leaf/tip", "D-p-s-l-t")
	if dst.Has("/parent/alias/stale") {
		t.Fatal("replacement retained an old descendant")
	}

	var shape map[string]map[string]json.RawMessage
	if err := json.Unmarshal(dst.ShowStruct(), &shape); err != nil {
		t.Fatal(err)
	}

	if _, ok := shape["parent"]["alias"]; !ok {
		t.Fatalf("ShowStruct does not use mount alias: %s", dst.ShowStruct())
	}

	if _, ok := shape["parent"][src.Name()]; ok {
		t.Fatal("ShowStruct used source identity instead of mount alias")
	}

	if err := src.Set(NewDirective("/leaf", appendProc("-changed"))); err != nil {
		t.Fatal(err)
	}

	graftTestGet(t, dst, "/parent/alias/leaf/tip", "D-p-s-changed-t")

	if err := dst.Set(NewDirective("/parent/alias/added", appendProc("-added"))); err != nil {
		t.Fatal(err)
	}

	if !src.Has("/added") || !dst.Has("/parent/alias/added") {
		t.Fatal("root Set did not change the attached source object")
	}

	graftTestGet(t, src, "/added", "D-p-s-added")

	if err := dst.Del("/parent/alias/leaf/tip"); err != nil {
		t.Fatal(err)
	}

	if src.Has("/leaf/tip") {
		t.Fatal("root Del did not use the source's local path")
	}

	nested := graftTestTree(t, "lazy", "IGNORED", NewDirective("/", appendProc("-nested")))

	if err := src.Graft("/from-handle", nested); err != nil {
		t.Fatal(err)
	}

	graftTestGet(t, dst, "/parent/alias/from-handle", "D-p-s-nested")
	if nested.Path() != "/parent/alias/from-handle" {
		t.Fatalf("local-handle Graft lost outer mount position: %q", nested.Path())
	}
}

func TestGraft_AllModePairs(t *testing.T) {
	modes := []string{"standard", "lazy", "instant", "ttl"}

	for _, parentMode := range modes {
		for _, childMode := range modes {
			t.Run(parentMode+"/"+childMode, func(t *testing.T) {
				var parentRuns, childRuns atomic.Int32
				dst := graftTestTree(t, parentMode, "D", NewDirective("/", graftTestCountProc(&parentRuns, "-p")))
				src := graftTestTree(t, childMode, "SOURCE", NewDirective("/", graftTestCountProc(&childRuns, "-c")))

				graftTestGet(t, src, "/", "SOURCE-c")
				childRuns.Store(0)
				parentBefore := parentRuns.Load()

				if err := dst.Graft("/mount", src); err != nil {
					t.Fatal(err)
				}

				if dst.pickChild("mount") != src {
					t.Fatal("mount did not retain source identity")
				}

				if childRuns.Load() != 0 || parentRuns.Load() != parentBefore {
					t.Fatal("Graft executed a processor; even standard mounts must remain cold")
				}

				graftTestGet(t, dst, "/mount", "D-p-c")
				if parentRuns.Load() != 1 || childRuns.Load() != 1 {
					t.Fatalf("after first query runs = %d/%d; want 1/1", parentRuns.Load(), childRuns.Load())
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
					t.Fatalf("after second query runs = %d/%d; want %d/%d", parentRuns.Load(), childRuns.Load(), wantParent, wantChild)
				}

				if parentMode == "ttl" {
					graftTestExpire(dst)
				}

				if childMode == "ttl" {
					graftTestExpire(src)
				}

				graftTestGet(t, dst, "/mount", "D-p-c")
				if parentMode == "instant" || parentMode == "ttl" {
					wantParent++
				}

				if parentMode == "instant" || parentMode == "ttl" || childMode == "instant" || childMode == "ttl" {
					wantChild++
				}

				if parentRuns.Load() != wantParent || childRuns.Load() != wantChild {
					t.Fatalf("after expiry runs = %d/%d; want %d/%d", parentRuns.Load(), childRuns.Load(), wantParent, wantChild)
				}
			})
		}
	}
}

func TestGraft_DeferredParentRefreshesStandardDescendants(t *testing.T) {
	for _, mode := range []string{"lazy", "instant", "ttl"} {
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

			if err := dst.Graft("/mount", src); err != nil {
				t.Fatal(err)
			}

			if parentRuns.Load() != 0 || childRuns.Load() != 0 || leafRuns.Load() != 0 {
				t.Fatal("Graft evaluated a deferred ancestry")
			}

			graftTestGet(t, dst, "/mount/leaf", "P1-s-l")

			switch mode {
			case "ttl":
				graftTestExpire(dst)
			case "lazy":
				if err := dst.Set(NewDirective("/", parentProc)); err != nil {
					t.Fatal(err)
				}
			}

			graftTestGet(t, dst, "/mount/leaf", "P2-s-l")
			if childRuns.Load() != 2 || leafRuns.Load() != 2 {
				t.Fatalf("standard descendants did not recompose: %d/%d", childRuns.Load(), leafRuns.Load())
			}
		})
	}
}

func TestGraft_NilParentContentIsInherited(t *testing.T) {
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
	src := graftTestTree(t, "standard", "SOURCE", NewDirective("/", &driver.RawProcessor{
		Proc: func(rc *driver.RealizeContext, before []byte) ([]byte, error) {
			if before == nil {
				nilInputs.Add(1)
			}

			return appendProc("-s").Process(rc, before)
		},
	}), NewDirective("/leaf", appendProc("-l")))

	if err := dst.Graft("/mount", src); err != nil {
		t.Fatal(err)
	}

	graftTestGet(t, dst, "/mount/leaf", "BEFORE-s-l")
	returnNil.Store(true)

	graftTestGet(t, dst, "/mount/leaf", "-s-l")
	if nilInputs.Load() != 1 {
		t.Fatalf("nil parent was not inherited: %d nil inputs", nilInputs.Load())
	}
}

func TestGraft_StandardSetBeforeFirstQueryIsDeferred(t *testing.T) {
	var parentRuns, updatedRuns, addedRuns atomic.Int32
	dst := graftTestTree(t, "lazy", "D", NewDirective("/", graftTestCountProc(&parentRuns, "-p")))
	src := graftTestTree(t, "standard", "SOURCE", NewDirective("/", appendProc("-s")), NewDirective("/leaf", appendProc("-l")))

	if err := dst.Graft("/mount", src); err != nil {
		t.Fatal(err)
	}

	if err := dst.Set(NewDirective("/mount", graftTestCountProc(&updatedRuns, "-updated"))); err != nil {
		t.Fatal(err)
	}

	if err := dst.Set(NewDirective("/mount/added", graftTestCountProc(&addedRuns, "-added"))); err != nil {
		t.Fatal(err)
	}

	if parentRuns.Load() != 0 || updatedRuns.Load() != 0 || addedRuns.Load() != 0 {
		t.Fatal("Set evaluated a standard node whose mounted base is not ready")
	}

	graftTestGet(t, dst, "/mount/added", "D-p-updated-added")
	graftTestGet(t, dst, "/mount/leaf", "D-p-updated-l")
	if updatedRuns.Load() != 1 || addedRuns.Load() != 1 {
		t.Fatalf("deferred Set ran %d/%d times", updatedRuns.Load(), addedRuns.Load())
	}
}

func TestGraft_ProcessorFailureIsDeferredToQuery(t *testing.T) {
	wantErr := errors.New("mounted runtime failure")
	var active atomic.Bool
	var runs atomic.Int32
	src := graftTestTree(t, "standard", "SOURCE", NewDirective("/", &driver.RawProcessor{
		Proc: func(_ *driver.RealizeContext, before []byte) ([]byte, error) {
			runs.Add(1)
			if active.Load() {
				return nil, wantErr
			}

			return before, nil
		},
	}))

	active.Store(true)
	runs.Store(0)
	dst := graftTestTree(t, "standard", "D", NewDirective("/mount/old"))

	if err := dst.Graft("/mount", src); err != nil {
		t.Fatalf("Graft must not execute the failing processor: %v", err)
	}

	if runs.Load() != 0 || dst.pickChild("mount") != src || dst.Has("/mount/old") {
		t.Fatal("Graft did not directly publish the cold replacement")
	}

	if _, err := dst.Get("/mount"); !errors.Is(err, wantErr) {
		t.Fatalf("Get error = %v; want %v", err, wantErr)
	}
}

func TestGraft_DynamicParamsAndMountedPaths(t *testing.T) {
	var staticRuns atomic.Int32
	var gotPaths []string
	capture := &driver.RawProcessor{Proc: func(rc *driver.RealizeContext, before []byte) ([]byte, error) {
		gotPaths = append(gotPaths, rc.TreePath)

		return before, nil
	}}
	src := graftTestTree(t, "lazy", "SOURCE", NewDirective("/leaf",
		graftTestCountProc(&staticRuns, "!"), &driver.TemplateProcessor{}, capture))
	dst := graftTestTree(t, "standard", "hello ${user}", NewDirective("/parent"))

	if err := dst.Graft("/parent/alias", src); err != nil {
		t.Fatal(err)
	}

	for _, user := range []string{"alice", "bob", "alice"} {
		got, err := dst.GetWithContext(paramRC(map[string]string{"user": user}), "/parent/alias/leaf")
		if err != nil || string(got) != "hello "+user+"!" {
			t.Fatalf("query %s = %q, %v", user, got, err)
		}
	}

	got, err := src.GetWithContext(paramRC(map[string]string{"user": "local"}), "/leaf")
	if err != nil || string(got) != "hello local!" {
		t.Fatalf("local mounted-handle context query = %q, %v", got, err)
	}

	graftTestGet(t, dst, "/parent/alias/leaf", "hello ${user}!")
	if staticRuns.Load() != 1 || len(gotPaths) != 4 {
		t.Fatalf("static/dynamic runs = %d/%d; want 1/4", staticRuns.Load(), len(gotPaths))
	}

	for _, path := range gotPaths {
		if path != "/parent/alias/leaf" {
			t.Fatalf("dynamic TreePath = %q", path)
		}
	}
}

func TestGraft_PreservesDefaultContextFallbackAndLimiter(t *testing.T) {
	type contextKey struct{}
	defaultRC := &driver.RealizeContext{
		Context:  context.WithValue(context.Background(), contextKey{}, "source-context"),
		TreePath: "source-sentinel", ParentContent: []byte("source-parent"),
	}

	var paths []string
	src := graftTestTree(t, "lazy", "SOURCE", NewDirective("/", &driver.RawProcessor{
		Proc: func(rc *driver.RealizeContext, before []byte) ([]byte, error) {
			paths = append(paths, rc.TreePath)
			value, _ := rc.Value(contextKey{}).(string)

			return appendProc("-"+value).Process(rc, before)
		},
	}))

	src.SetDefaultContext(defaultRC)
	src.SetFallback(appendProc("-fallback"))
	src.SetRateLimit(rate.Limit(0), 1)
	originalDriver, originalLimiter := src.driver, src.rateLimiter
	dst := graftTestTree(t, "standard", "D")

	if err := dst.Graft("/alias", src); err != nil {
		t.Fatal(err)
	}

	if src.driver != originalDriver || src.rateLimiter != originalLimiter {
		t.Fatal("direct mount replaced the source driver or rate limiter")
	}

	graftTestGet(t, dst, "/alias", "D-source-context")
	graftTestGet(t, dst, "/alias/missing", "D-source-context-fallback")
	if len(paths) != 1 || paths[0] != "/alias" {
		t.Fatalf("default-context TreePath = %v", paths)
	}

	if defaultRC.TreePath != "source-sentinel" || string(defaultRC.ParentContent) != "source-parent" {
		t.Fatal("mounted query mutated source default context")
	}

	src.realizeMu.Lock()
	src.realizedAt = time.Time{}
	src.realizeMu.Unlock()
	if _, err := dst.Get("/alias"); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("mounted source lost limiter token state: %v", err)
	}
}

func TestGraft_DefaultContextTracksRemountWithoutMutation(t *testing.T) {
	defaultRC := &driver.RealizeContext{
		Context: context.Background(), TreePath: "source-sentinel", ParentContent: []byte("source-parent"),
	}

	var paths, parents []string
	source := graftTestTree(t, "instant", "SOURCE", NewDirective("/", &driver.RawProcessor{
		Proc: func(rc *driver.RealizeContext, before []byte) ([]byte, error) {
			paths = append(paths, rc.TreePath)
			parents = append(parents, string(rc.ParentContent))

			return before, nil
		},
	}))

	source.SetDefaultContext(defaultRC)
	a := graftTestTree(t, "standard", "A")

	if err := a.Graft("/alias", source); err != nil {
		t.Fatal(err)
	}

	graftTestGet(t, a, "/alias", "A")

	if err := a.Del("/alias"); err != nil {
		t.Fatal(err)
	}

	b := graftTestTree(t, "standard", "B", NewDirective("/parent"))

	if err := b.Graft("/parent/renamed", source); err != nil {
		t.Fatal(err)
	}

	graftTestGet(t, b, "/parent/renamed", "B")
	if len(paths) != 2 || paths[0] != "/alias" || paths[1] != "/parent/renamed" {
		t.Fatalf("default-context mounted paths = %v", paths)
	}

	if len(parents) != 2 || parents[0] != "A" || parents[1] != "B" {
		t.Fatalf("default-context inherited parents = %v", parents)
	}

	if defaultRC.TreePath != "source-sentinel" || string(defaultRC.ParentContent) != "source-parent" {
		t.Fatal("query or remount mutated the retained source default context")
	}
}

func TestGraft_RejectsInvalidPathsAndSources(t *testing.T) {
	for _, kind := range []string{"root", "empty", "empty_segment", "missing_parent", "nil", "typed_nil", "self"} {
		t.Run(kind, func(t *testing.T) {
			dst := graftTestTree(t, "standard", "D", NewDirective("/keep", appendProc("-old")))
			var source Tree = graftTestTree(t, "lazy", "SOURCE")
			path := "/keep"

			switch kind {
			case "root":
				path = "/"
			case "empty":
				path = ""
			case "empty_segment":
				path = "/a//b"
			case "missing_parent":
				path = "/missing/child"
			case "nil":
				source = nil
			case "typed_nil":
				var native *tree
				source = native
			case "self":
				source = dst
			}

			old := dst.pickChild("keep")

			if err := dst.Graft(path, source); err == nil {
				t.Fatal("expected invalid graft error")
			}

			if dst.pickChild("keep") != old {
				t.Fatal("invalid graft replaced an existing node")
			}

			graftTestGet(t, dst, "/keep", "D-old")
		})
	}
}

func TestGraft_SingleParentOwnershipAndCycles(t *testing.T) {
	a := graftTestTree(t, "standard", "A")
	b := graftTestTree(t, "standard", "B")
	src := graftTestTree(t, "lazy", "SOURCE", NewDirective("/leaf"))

	if err := a.Graft("/mount", src); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name   string
		dst    Tree
		path   string
		source Tree
	}{
		{"second_parent", b, "/other", src},
		{"same_position", a, "/mount", src},
		{"second_alias", a, "/other", src},
		{"descendant_source", b, "/other", src.pickChild("leaf")},
		{"ancestor_cycle", a, "/mount/leaf/cycle", a},
		{"self_cycle", src, "/self", src},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.dst.Graft(tc.path, tc.source); err == nil {
				t.Fatal("accepted multiple ownership or a cycle")
			}
		})
	}

	if a.pickChild("mount") != src || b.Has("/other") || a.Has("/other") {
		t.Fatal("failed ownership check changed topology")
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

func TestGraft_NativeSourceKeepsDriverWithRootPathSyntax(t *testing.T) {
	var calls atomic.Int32
	sourceDriver := &graftTestSourceDriver{
		testPathDriver: &testPathDriver{Modem: driver.DummyModem, DelimiterPathParser: new(driver.DelimiterPathParser).WithDelimiter(".")},
		calls:          &calls,
	}

	src, err := NewLazyTree(sourceDriver, "source", "SOURCE", NewDirective(".", appendProc("-s")), NewDirective(".leaf", appendProc("-l")))
	if err != nil {
		t.Fatal(err)
	}

	dst := graftTestTree(t, "standard", "D", NewDirective("/parent", appendProc("-p")))

	if err := dst.Graft("/parent/alias", src); err != nil {
		t.Fatal(err)
	}

	if src.(*tree).driver != sourceDriver {
		t.Fatal("Graft replaced source driver instead of retaining format execution")
	}

	graftTestGet(t, dst, "/parent/alias/leaf", "D-p-s-l")
	if calls.Load() != 2 {
		t.Fatalf("source realizer calls = %d; want 2", calls.Load())
	}

	if err := dst.Set(NewDirective("/parent/alias/added", appendProc("-a"))); err != nil {
		t.Fatal(err)
	}

	graftTestGet(t, dst, "/parent/alias/added", "D-p-s-a")
	if !dst.Has("/parent/alias/added") {
		t.Fatal("Has switched from root parser to source parser")
	}

	if err := dst.Del("/parent/alias/added"); err != nil {
		t.Fatal(err)
	}

	if dst.Has("/parent/alias/added") {
		t.Fatal("Del switched from root parser to source parser")
	}

	other := graftTestTree(t, "lazy", "IGNORED", NewDirective("/", appendProc("-other")))

	if err := dst.Graft("/parent/alias/leaf/other", other); err != nil {
		t.Fatal(err)
	}

	graftTestGet(t, dst, "/parent/alias/leaf/other", "D-p-s-l-other")
	if other.Path() != "/parent/alias/leaf/other" {
		t.Fatalf("Path did not use root syntax: %q", other.Path())
	}
}

func TestGraft_CustomRootParserControlsNativeTraversal(t *testing.T) {
	rootDriver := &testPathDriver{Modem: driver.DummyModem, DelimiterPathParser: new(driver.DelimiterPathParser).WithDelimiter(".")}
	root, err := NewLazyTree(rootDriver, "root", "R", NewDirective(".parent", appendProc("-p")))
	if err != nil {
		t.Fatal(err)
	}

	source := graftTestTree(t, "lazy", "IGNORED", NewDirective("/", appendProc("-s")), NewDirective("/leaf", appendProc("-l")))

	if err := root.Graft(".parent.alias", source); err != nil {
		t.Fatal(err)
	}

	graftTestGet(t, root, ".parent.alias.leaf", "R-p-s-l")
	if source.Path() != ".parent.alias" || source.pickChild("leaf").Path() != ".parent.alias.leaf" {
		t.Fatalf("mounted paths ignored root parser: %q/%q", source.Path(), source.pickChild("leaf").Path())
	}

	if err := root.Set(NewDirective(".parent.alias.added", appendProc("-a"))); err != nil {
		t.Fatal(err)
	}

	graftTestGet(t, root, ".parent.alias.added", "R-p-s-a")
	if !root.Has(".parent.alias.added") {
		t.Fatal("custom root Has did not consume local segments")
	}

	if err := root.Del(".parent.alias.added"); err != nil {
		t.Fatal(err)
	}

	if root.Has(".parent.alias.added") {
		t.Fatal("custom root Del did not consume local segments")
	}
}

// This intentionally does not embed a native tree: every public operation is
// overridden so a promoted Get/Set cannot conceal an incorrect full-path call.
type graftTestForeignTree struct {
	calls       map[string]string
	setProcs    []driver.Processor
	graftSource Tree
	params      map[string]string
}

func newGraftTestForeignTree() *graftTestForeignTree {
	return &graftTestForeignTree{calls: make(map[string]string)}
}

func (*graftTestForeignTree) Name() string { return "foreign-identity" }

func (*graftTestForeignTree) Path() string { return "" }

func (f *graftTestForeignTree) Get(path string) ([]byte, error) {
	f.calls["Get"] = path

	return []byte("foreign:" + path), nil
}

func (f *graftTestForeignTree) GetWithContext(rc *driver.RealizeContext, path string) ([]byte, error) {
	f.calls["GetWithContext"] = path

	if rc != nil {
		f.params = rc.Params
	}

	return []byte("foreign-context:" + path), nil
}

func (f *graftTestForeignTree) Set(d Directive) error {
	f.calls["Set"] = d.Path()
	f.setProcs = d.Processors()

	return nil
}

func (f *graftTestForeignTree) Has(path string) bool {
	f.calls["Has"] = path

	return true
}

func (f *graftTestForeignTree) Del(path string) error {
	f.calls["Del"] = path

	return nil
}

func (f *graftTestForeignTree) Graft(path string, source Tree) error {
	f.calls["Graft"] = path
	f.graftSource = source

	return nil
}

func (*graftTestForeignTree) ShowStruct() []byte { return []byte(`{}`) }

func (*graftTestForeignTree) SetRateLimit(rate.Limit, int) {}

func (*graftTestForeignTree) SetFallback(driver.Processor) {}

func (*graftTestForeignTree) SetDefaultContext(*driver.RealizeContext) {}

func TestGraft_ForeignTreeReceivesOnlyRemainingLocalPaths(t *testing.T) {
	dst := graftTestTree(t, "standard", "D", NewDirective("/parent"))
	foreign := newGraftTestForeignTree()

	if err := dst.Graft("/parent/alias", foreign); err != nil {
		t.Fatal(err)
	}

	graftTestGet(t, dst, "/parent/alias/leaf/child", "foreign:/leaf/child")
	got, err := dst.GetWithContext(paramRC(map[string]string{"user": "alice"}), "/parent/alias/leaf/child")
	if err != nil || string(got) != "foreign-context:/leaf/child" || foreign.params["user"] != "alice" {
		t.Fatalf("foreign context query = %q, %v, params=%v", got, err, foreign.params)
	}

	proc := appendProc("-set")

	if err := dst.Set(NewDirective("/parent/alias/leaf/child", proc)); err != nil {
		t.Fatal(err)
	}

	if len(foreign.setProcs) != 1 || foreign.setProcs[0] != proc {
		t.Fatal("local directive lost processors")
	}

	if !dst.Has("/parent/alias/leaf/child") {
		t.Fatal("foreign Has result was lost")
	}

	if err := dst.Del("/parent/alias/leaf/child"); err != nil {
		t.Fatal(err)
	}

	other := graftTestTree(t, "lazy", "OTHER")

	if err := dst.Graft("/parent/alias/leaf/child", other); err != nil {
		t.Fatal(err)
	}

	for _, method := range []string{"Get", "GetWithContext", "Set", "Has", "Del", "Graft"} {
		if got := foreign.calls[method]; got != "/leaf/child" {
			t.Errorf("foreign %s received %q; want local /leaf/child", method, got)
		}
	}

	if foreign.graftSource != other {
		t.Fatal("foreign Graft did not receive original source object")
	}
}
