package ivy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
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
	for _, tc := range []struct {
		name, path string
		missing    bool
	}{
		{"root", "/", false},
		{"empty", "", false},
		{"missing_parent", "/missing/child", true},
		{"nil_source", "/keep", true},
		{"typed_nil_native", "/keep", true},
		{"typed_nil_custom", "/keep", true},
		{"nil_receiver", "/keep", true},
		{"self", "/keep", false},
		{"ancestor_cycle", "/keep/cycle", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dst := graftTestTree(t, "standard", "D", NewDirective("/keep", appendProc("-old")))
			var source Tree = graftTestTree(t, "lazy", "SOURCE")
			receiver := dst

			switch tc.name {
			case "nil_source":
				source = nil
			case "typed_nil_native":
				var native *tree
				source = native
			case "typed_nil_custom":
				var custom *graftTestForeignTree
				source = custom
			case "nil_receiver":
				receiver = nil
			case "self", "ancestor_cycle":
				source = dst
			}

			old := dst.pickChild("keep")
			err := receiver.Graft(tc.path, source)

			if err == nil || errors.Is(err, ErrNotExistsTree) != tc.missing {
				t.Fatalf("Graft error = %v; want rejection, missing=%v", err, tc.missing)
			}

			if tc.missing && receiver != nil && errors.Unwrap(err) == nil {
				t.Fatal("Graft discarded source/parent context instead of wrapping the sentinel")
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

func TestGraft_EmptySegmentUsesParentLookup(t *testing.T) {
	for _, sourceKind := range []string{"native", "custom"} {
		t.Run(sourceKind, func(t *testing.T) {
			root := graftTestTree(t, "standard", "ROOT", NewDirective("/a/b", appendProc("-keep")))
			source := graftDispatchSource(t, sourceKind == "native")
			parent := root.pickChild("a").(*tree)
			previous := parent.pickChild("b")

			err := root.Graft("/a//b", source)

			if !errors.Is(err, ErrNotExistsTree) {
				t.Fatalf("Graft error = %v; want missing empty-named parent", err)
			}

			if parent.pickChild("") != nil || parent.pickChild("b") != previous {
				t.Fatal("failed lookup created an empty parent or skipped it to replace /a/b")
			}

			graftTestGet(t, root, "/a/b", "ROOT-keep")
		})
	}
}

func TestGraft_OwnershipCheckPrecedesCycleCheck(t *testing.T) {
	root := graftTestTree(t, "lazy", "ROOT", NewDirective("/branch/leaf"))
	branch := root.pickChild("branch").(*tree)
	leaf := branch.pickChild("leaf").(*tree)

	for _, tc := range []struct {
		name     string
		receiver *tree
		source   *tree
		want     string
	}{
		{"attached_self", branch, branch, "already attached"},
		{"attached_ancestor", leaf, branch, "already attached"},
		{"detached_self", root, root, "cycle"},
		{"detached_ancestor", leaf, root, "cycle"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.receiver.Graft("/slot", tc.source)

			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Graft error = %v; want %q rejection", err, tc.want)
			}
		})
	}
}

func TestCycleCheck(t *testing.T) {
	root := graftTestTree(t, "lazy", "ROOT", NewDirective("/child/leaf"))
	child := root.pickChild("child").(*tree)
	leaf := child.pickChild("leaf").(*tree)
	other := graftTestTree(t, "lazy", "OTHER")

	for _, tc := range []struct {
		name   string
		source *tree
		parent *tree
		want   bool
	}{
		{"self", root, root, true},
		{"direct_ancestor", root, child, true},
		{"deep_ancestor", root, leaf, true},
		{"descendant", leaf, root, false},
		{"unrelated", other, leaf, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := cycleCheck(tc.source, tc.parent); got != tc.want {
				t.Fatalf("cycleCheck = %v; want %v", got, tc.want)
			}
		})
	}
}
