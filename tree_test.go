package ivy

import (
	"context"
	"errors"
	"testing"
	"time"

	"golang.org/x/time/rate"

	"github.com/tr1v3r/ivy/driver"
)

func TestTree_Has(t *testing.T) {
	tree, err := NewTree(newTestDriver(), "has_test", `{}`,
		NewDirective("/a/b", &driver.JSONProcessor{T: "create", JSONPath: "v", V: []byte("1")}),
	)
	if err != nil {
		t.Fatalf("build fail: %s", err)
	}

	var testcases = []struct {
		Path   string
		Expect bool
	}{
		// the root node carries the tree name, so Has("/") is false for named trees
		{"/", false},
		{"/a", true},
		{"/a/b", true},
		{"/a/x", false},
		{"/x", false},
		{"/x/y", false},
	}
	for _, item := range testcases {
		if got := tree.Has(item.Path); got != item.Expect {
			t.Errorf("Has(%q): expect %v, got %v", item.Path, item.Expect, got)
		}
	}
}

func TestTree_Del(t *testing.T) {
	tree, err := NewTree(newTestDriver(), "del_test", `{}`,
		NewDirective("/a/b", &driver.JSONProcessor{T: "create", JSONPath: "v", V: []byte("1")}),
		NewDirective("/x/y", &driver.JSONProcessor{T: "create", JSONPath: "v", V: []byte("2")}),
	)
	if err != nil {
		t.Fatalf("build fail: %s", err)
	}

	// root cannot be deleted
	if err := tree.Del("/"); err == nil {
		t.Error("expected error deleting root")
	}

	if !tree.Has("/a/b") {
		t.Fatal("expected /a/b before delete")
	}
	if err := tree.Del("/a/b"); err != nil {
		t.Fatalf("del /a/b fail: %s", err)
	}
	if tree.Has("/a/b") {
		t.Error("expected /a/b gone after delete")
	}
	if !tree.Has("/a") {
		t.Error("parent /a should remain")
	}

	// deleting a non-existent path is a no-op
	if err := tree.Del("/nope/nope"); err != nil {
		t.Errorf("del non-existent path should not fail: %s", err)
	}
}

func TestTree_SetRateLimit(t *testing.T) {
	tree, err := NewLazyInstantTree(newTestDriver(), "rl_test", `{}`,
		NewDirective("/", &driver.JSONProcessor{T: "create", JSONPath: "v", V: []byte("1")}),
	)
	if err != nil {
		t.Fatalf("build fail: %s", err)
	}

	// limit 0 / burst 0 never allows
	tree.SetRateLimit(rate.Limit(0), 0)

	if _, err := tree.Get("/"); !errors.Is(err, ErrRateLimited) {
		t.Errorf("expected ErrRateLimited, got %v", err)
	}
	if _, err := tree.GetWithContext(nil, "/"); !errors.Is(err, ErrRateLimited) {
		t.Errorf("expected ErrRateLimited (context variant), got %v", err)
	}
}

func TestTree_SetDefaultContext(t *testing.T) {
	var gotRC *driver.RealizeContext
	capture := &contextCapturingProcessor{capture: func(rc *driver.RealizeContext) { gotRC = rc }}

	tree, err := NewLazyTree(newTestDriver(), "default_ctx_test", `{}`,
		NewDirective("/a/b", capture),
	)
	if err != nil {
		t.Fatalf("build fail: %s", err)
	}

	defaultRC := &driver.RealizeContext{
		Context: context.Background(),
		Params:  map[string]string{"who": "default"},
	}
	tree.SetDefaultContext(defaultRC)

	// Get without an explicit context falls back to the default context
	if _, err := tree.Get("/a/b"); err != nil {
		t.Fatalf("get fail: %s", err)
	}
	if gotRC == nil || gotRC.Params["who"] != "default" {
		t.Errorf("expected default context params, got %v", gotRC)
	}
}

func TestTree_InheritOnLazyGet(t *testing.T) {
	// in lazy mode a fresh subtree inherits its parent's realized content
	tree, err := NewLazyTree(newTestDriver(), "inherit_test", `{"from":"root"}`,
		NewDirective("/a/b", &driver.JSONProcessor{T: "create", JSONPath: "child", V: []byte("true")}),
	)
	if err != nil {
		t.Fatalf("build fail: %s", err)
	}

	val, err := tree.Get("/a/b")
	if err != nil {
		t.Fatalf("get fail: %s", err)
	}
	expect := `{"from":"root","child":"true"}`
	if string(val) != expect {
		t.Errorf("expect %s, got %s", expect, val)
	}

	// intermediate node /a keeps the inherited root content
	mid, err := tree.Get("/a")
	if err != nil {
		t.Fatalf("get /a fail: %s", err)
	}
	if string(mid) != `{"from":"root"}` {
		t.Errorf("unexpected /a content: %s", mid)
	}
}

func TestTree_GetParentContentInContext(t *testing.T) {
	var parentContent []byte
	proc := &driver.RawProcessor{Proc: func(rc *driver.RealizeContext, before []byte) ([]byte, error) {
		parentContent = rc.ParentContent
		return before, nil
	}}

	tree, err := NewLazyTree(newTestDriver(), "parent_ctx_test", `{"root":1}`,
		NewDirective("/a/b", proc),
	)
	if err != nil {
		t.Fatalf("build fail: %s", err)
	}

	rc := &driver.RealizeContext{Context: context.Background()}
	if _, err := tree.GetWithContext(rc, "/a/b"); err != nil {
		t.Fatalf("get fail: %s", err)
	}
	if string(parentContent) != `{"root":1}` {
		t.Errorf("expected parent content in context, got %s", parentContent)
	}
}

func TestTree_NilTreeGuards(t *testing.T) {
	var nilTree *tree

	if _, err := nilTree.Get("/"); !errors.Is(err, ErrNotExistsTree) {
		t.Errorf("expected ErrNotExistsTree from nil Get, got %v", err)
	}
	if _, err := nilTree.GetWithContext(nil, "/"); !errors.Is(err, ErrNotExistsTree) {
		t.Errorf("expected ErrNotExistsTree from nil GetWithContext, got %v", err)
	}
}

// appendProc returns a NON-idempotent processor: output = input + suffix.
// It makes compounding bugs (re-applying a chain on a previous output)
// immediately visible in the returned value.
func appendProc(suffix string) driver.Processor {
	return &driver.RawProcessor{Proc: func(_ *driver.RealizeContext, before []byte) ([]byte, error) {
		out := make([]byte, 0, len(before)+len(suffix))
		out = append(out, before...)
		return append(out, suffix...), nil
	}}
}

// TestTree_InstantRootNotCompounding guards audit F1: in instant mode the
// root node re-realizes on every access, and its chain must be replayed on
// the root template, never on the node's previous output.
func TestTree_InstantRootNotCompounding(t *testing.T) {
	tree, err := NewLazyInstantTree(newTestDriver(), "instant_root", "R",
		NewDirective("/", appendProc("-x")),
	)
	if err != nil {
		t.Fatalf("build fail: %s", err)
	}
	for i := 0; i < 3; i++ {
		val, err := tree.Get("/")
		if err != nil {
			t.Fatalf("get #%d fail: %s", i, err)
		}
		if string(val) != "R-x" {
			t.Fatalf("get #%d: expect %q, got %q (instant root compounded)", i, "R-x", val)
		}
	}
}

// TestTree_CacheTTLRootNotCompounding guards audit F1: after the cache TTL
// expires, the root must re-realize from the template, not compound on its
// previous output.
func TestTree_CacheTTLRootNotCompounding(t *testing.T) {
	tree, err := NewLazyCacheTree(newTestDriver(), "ttl_root", "R", 40*time.Millisecond,
		NewDirective("/", appendProc("-x")),
	)
	if err != nil {
		t.Fatalf("build fail: %s", err)
	}
	first, err := tree.Get("/")
	if err != nil {
		t.Fatalf("first get fail: %s", err)
	}
	if string(first) != "R-x" {
		t.Fatalf("first get: expect %q, got %q", "R-x", first)
	}

	time.Sleep(60 * time.Millisecond) // let the TTL expire

	second, err := tree.Get("/")
	if err != nil {
		t.Fatalf("second get fail: %s", err)
	}
	if string(second) != "R-x" {
		t.Fatalf("after TTL expiry: expect %q, got %q (root compounded)", "R-x", second)
	}
}

// TestTree_CacheTTLLeafRecomposesFromParent guards the base/inherit split:
// after TTL expiry a leaf must recompose from its parent's content exactly
// once (its own chain applied a single time).
func TestTree_CacheTTLLeafRecomposesFromParent(t *testing.T) {
	tree, err := NewLazyCacheTree(newTestDriver(), "ttl_leaf", "R", 40*time.Millisecond,
		NewDirective("/a/b", appendProc("-b")),
	)
	if err != nil {
		t.Fatalf("build fail: %s", err)
	}
	first, err := tree.Get("/a/b")
	if err != nil {
		t.Fatalf("first get fail: %s", err)
	}
	if string(first) != "R-b" {
		t.Fatalf("first get: expect %q, got %q", "R-b", first)
	}

	time.Sleep(60 * time.Millisecond) // let the TTL expire

	second, err := tree.Get("/a/b")
	if err != nil {
		t.Fatalf("second get fail: %s", err)
	}
	if string(second) != "R-b" {
		t.Fatalf("after TTL expiry: expect %q, got %q (leaf compounded)", "R-b", second)
	}
}

// TestTree_SetReRealizes guards audit F2: Set is documented as "adds or
// updates" (export.go). Updating the processors of an already-realized
// standard-mode node must re-realize; the cached result must not pin the
// old content forever.
func TestTree_SetReRealizes(t *testing.T) {
	tree, err := NewTree(newTestDriver(), "set_update", `{}`,
		NewDirective("/", replaceProc("OLD")),
	)
	if err != nil {
		t.Fatalf("build fail: %s", err)
	}
	if val, err := tree.Get("/"); err != nil || string(val) != "OLD" {
		t.Fatalf("first get: val=%q err=%v", val, err)
	}

	if err := tree.Set(NewDirective("/", replaceProc("NEW"))); err != nil {
		t.Fatalf("set fail: %s", err)
	}
	val, err := tree.Get("/")
	if err != nil {
		t.Fatalf("get after set fail: %s", err)
	}
	if string(val) != "NEW" {
		t.Fatalf("Set update not effective: expect %q, got %q (stale cache)", "NEW", val)
	}
}

// TestTree_SetReRealizesLazy is the lazy-mode variant: with cacheTTL == 0 a
// realized lazy node caches forever, so a Set() replacement must also
// invalidate that cache.
func TestTree_SetReRealizesLazy(t *testing.T) {
	tree, err := NewLazyTree(newTestDriver(), "set_update_lazy", `{}`,
		NewDirective("/a/b", replaceProc("OLD")),
	)
	if err != nil {
		t.Fatalf("build fail: %s", err)
	}
	if val, err := tree.Get("/a/b"); err != nil || string(val) != "OLD" {
		t.Fatalf("first get: val=%q err=%v", val, err)
	}

	if err := tree.Set(NewDirective("/a/b", replaceProc("NEW"))); err != nil {
		t.Fatalf("set fail: %s", err)
	}
	val, err := tree.Get("/a/b")
	if err != nil {
		t.Fatalf("get after set fail: %s", err)
	}
	if string(val) != "NEW" {
		t.Fatalf("Set update not effective (lazy): expect %q, got %q (stale cache)", "NEW", val)
	}
}

// TestTree_DuplicateDirectivesLastWins guards the build-time face of the
// same bug: two directives on the same path, the last one must win.
func TestTree_DuplicateDirectivesLastWins(t *testing.T) {
	tree, err := NewTree(newTestDriver(), "dup_directives", `{}`,
		NewDirective("/", replaceProc("first")),
		NewDirective("/", replaceProc("second")),
	)
	if err != nil {
		t.Fatalf("build fail: %s", err)
	}
	val, err := tree.Get("/")
	if err != nil {
		t.Fatalf("get fail: %s", err)
	}
	if string(val) != "second" {
		t.Fatalf("duplicate directives: expect last one %q to win, got %q", "second", val)
	}
}
