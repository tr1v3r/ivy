package ivy

import (
	"cmp"
	"reflect"
	"slices"
)

func (t *tree) topologyRoot() *tree {
	for {
		link := t.mount.Load()
		if link == nil {
			return t
		}
		t = link.parent
	}
}

// lockNativeTopology locks only the enclosing roots of the involved nodes.
// Root addresses impose a consistent lock order without a global mutex or ID
// allocator; references remain live throughout acquisition and release.
//
// A node can move while we wait for its old root. Rechecking the current roots
// after acquisition prevents editing that node under a stale ownership domain.
// On detach, the subtree root's own mutex becomes active again; there is no
// shared lock pointer to recursively rewrite in its descendants. Callers must
// not invoke processors or foreign Tree methods while these locks are held.
func lockNativeTopology(nodes ...*tree) func() {
	for {
		roots := make([]*tree, 0, len(nodes))
		for _, node := range nodes {
			if node != nil {
				root := node.topologyRoot()
				if !slices.Contains(roots, root) {
					roots = append(roots, root)
				}
			}
		}
		slices.SortFunc(roots, func(a, b *tree) int {
			return cmp.Compare(reflect.ValueOf(a).Pointer(), reflect.ValueOf(b).Pointer())
		})
		for _, root := range roots {
			root.topologyMu.Lock()
		}
		valid := true
		for _, node := range nodes {
			if node != nil && !slices.Contains(roots, node.topologyRoot()) {
				valid = false
				break
			}
		}
		if valid {
			return func() { unlockNativeRoots(roots) }
		}
		unlockNativeRoots(roots)
	}
}

func unlockNativeRoots(roots []*tree) {
	for i := len(roots) - 1; i >= 0; i-- {
		roots[i].topologyMu.Unlock()
	}
}
