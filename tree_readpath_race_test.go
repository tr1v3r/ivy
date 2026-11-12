package ivy

import (
	"errors"
	"testing"
)

// This file supplements the read-path race-window tests already on this
// branch (TestGraft_PublicQueryRetriesAncestorDetach and
// TestGraft_PublicQueryPropagatesAncestorError in graft_lifecycle_test.go)
// with the descent-boundary window they leave open. The single test below is
// fully deterministic: no goroutines, no sleeps.

// TestTreeReadPathDescentSeesClearedMount pins the descent-boundary window
// between the children lookup and queryMount (tree.go:351-360 for Get,
// tree.go:412-425 for GetWithContext, failing inside graft.go:18-21): a
// child edge that resolves in the children map while its mount link is
// already cleared. graftNativeTree publishes exactly this transient state
// between previousTree.mount.Store(nil) (graft.go:154) and the
// mountParent.children[name] = source swap (graft.go:157), and again for
// the incoming source between that swap and source.mount.Store (graft.go:
// 158) — a concurrent descent must restart from the root instead of
// descending into a subtree whose ownership is gone. The white-box store
// reproduces the mid-graft state deterministically; the trees are
// throwaway and never restored.
func TestTreeReadPathDescentSeesClearedMount(t *testing.T) {
	R := graftTestTree(t, "standard", "R", NewDirective("/c", appendProc("-c")))
	C := R.pickChild("c").(*tree)

	C.mount.Store(nil) // children hit, mount cleared: mid-graft transient

	q := newPathQuery(R.driver, "/c")

	if _, err := R.getWithParent(nil, 0, q, R.mount.Load()); !errors.Is(err, errMountChanged) {
		t.Fatalf("getWithParent error = %v; want errMountChanged", err)
	}
	if _, err := R.getContextWithParent(nil, nil, 0, q, R.mount.Load()); !errors.Is(err, errMountChanged) {
		t.Fatalf("getContextWithParent error = %v; want errMountChanged", err)
	}
}
