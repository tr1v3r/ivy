package ivy

import (
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// This file supplements the graft race-window tests already on this branch
// (TestNativeTopology_GraftRejectsOwnershipChangedAfterPrecheck,
// TestGraft_PublicQueryRetriesAncestorDetach and friends in
// graft_lifecycle_test.go / topology_test.go) with the descent-side windows
// they leave open: the getChild double-check adoption and the recursive
// deep-name rejection. Every assertion is scheduling-independent: any sleep
// below only raises the probability of executing a particular branch and is
// never a correctness dependency.

// TestGraftRace_GetChildRecheckAdoptsInjectedChild pins the double-check hit in
// getChild (tree.go:609-610): after pickChild misses (tree.go:597), getChild
// blocks on lockTopologyRoots before re-reading children under t.mu. The test
// holds the root's topologyMu so a concurrent Set parks exactly there, then
// injects a ready-made "x" subtree directly into the root's children and
// releases the lock. Set's recheck observes the injected node at tree.go:610
// and returns it, so the shared branch is adopted instead of re-created.
//
// The interleaving is directed: Set cannot pass getChild("x") while the
// topology lock is held, and the injection happens strictly before the
// release. The short sleep after Set started only raises the chance that Set
// already executed its pickChild miss before the injection (coverage of
// tree.go:610); every assertion below also holds when Set reads the map after
// the injection and takes the pickChild fast path at tree.go:597-598.
func TestGraftRace_GetChildRecheckAdoptsInjectedChild(t *testing.T) {
	var setRuns atomic.Int32
	root := graftTestTree(t, "standard", "ROOT")

	root.topologyMu.Lock()

	setTask := startTopologyTestTask(func() error {
		return root.Set(NewDirective("/x/y", graftTestCountProc(&setRuns, "-xy")))
	})
	<-setTask.started

	// Coverage aid only, never a correctness dependency: give Set a moment to
	// run its pickChild miss and park on the topology lock held above.
	time.Sleep(10 * time.Millisecond)

	injected := root.newSubTree("x")
	root.mu.Lock()
	root.children["x"] = injected
	root.mu.Unlock()

	root.topologyMu.Unlock()

	if err, ok := setTask.await(t, "Set creating /x/y behind the held topology lock"); !ok {
		return
	} else if err != nil {
		t.Fatal(err)
	}

	if got := root.pickChild("x"); got != Tree(injected) {
		t.Fatalf("root child x = %v; want the injected shared branch object", got)
	}

	if injected.pickChild("y") == nil {
		t.Fatal("Set did not create y below the injected branch")
	}

	graftTestGet(t, root, "/x/y", "ROOT-xy")

	// Twice is correct: once from the eager standard-mode realization at
	// Set time, and once more on the first read, when the injected parent
	// publishes for the first time (contentVer 0→1) and the child
	// recomposes from that publication change.
	if setRuns.Load() != 2 {
		t.Fatalf("set processor ran %d times; want 2", setRuns.Load())
	}
}

// TestGraft_DeepInvalidNativeNamePropagatesRecursively pins the recursive
// error propagation in validateNativeNames (graft.go:299-300). First-level
// source names are checked inline at graft.go:294; a name that is only
// illegal deeper in the source must bubble out through the recursive
// `return err`. "/mid//leaf" keeps the first level legal ("mid") and nests
// the illegal empty path segment as mid's child, two levels below the source
// root, so the rejection surfaces from the nested frame and traverses
// graft.go:300 on its way out of the root frame.
func TestGraft_DeepInvalidNativeNamePropagatesRecursively(t *testing.T) {
	dst := graftTestTree(t, "standard", "D")
	source := graftTestTree(t, "lazy", "SOURCE")

	if err := source.Set(NewDirective("/mid//leaf", appendProc("-deep"))); err != nil {
		t.Fatal(err)
	}

	err := dst.Graft("/mount", source)
	if err == nil || !strings.Contains(err.Error(), "incompatible with enclosing path syntax") {
		t.Fatalf("Graft error = %v; want deep illegal-name rejection", err)
	}

	if dst.pickChild("mount") != nil {
		t.Fatal("rejected graft published the mount point")
	}

	if link := source.mount.Load(); link != nil {
		t.Fatalf("rejected graft changed source ownership: %+v", link)
	}

	if source.pickChild("mid") == nil {
		t.Fatal("rejected graft changed source topology")
	}
}
