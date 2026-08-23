package ivy

import (
	"context"
	"errors"
	"testing"

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
