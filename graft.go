package ivy

import (
	"cmp"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"time"
	"unsafe"

	"github.com/tr1v3r/ivy/driver"
)

var errMountChanged = errors.New("tree moved during query; retry from the root")

// A native edge must still belong to the parent whose content the query carries.
func queryMount(child, parent *tree, name string) (*treeMount, error) {
	link := child.mount.Load()
	if link == nil || link.parent != parent || link.name != name {
		return nil, errMountChanged
	}

	return link, nil
}

type treeMount struct {
	parent *tree
	name   string // target edge alias, independent of the source's Name
}

// Graft directly attaches source at a local path, replacing the entire target
// subtree. Missing native parents are created; the receiver root cannot be replaced.
// No tree is copied and no processors run during grafting. Native source caches
// are invalidated so the next access composes from the new parent's content.
func (t *tree) Graft(path string, source Tree) error {
	if t == nil {
		return ErrNotExistsTree
	}

	if source == nil || isNilTree(source) {
		return fmt.Errorf("graft source: %w", ErrNotExistsTree)
	}

	q := newPathQuery(t.driver, path)
	if q.done() {
		return fmt.Errorf("graft cannot replace the receiver root")
	}

	var (
		targetTree    Tree
		remainingPath string
		err           error
	)

	switch source := source.(type) {
	case *tree:
		targetTree, remainingPath, err = t.graftNativeTree(q, source)
	default:
		targetTree, remainingPath, err = t.graftTree(q, source)
	}

	if err != nil {
		return err
	}

	// Both source-specific helpers release their locks before returning a
	// custom target. Never invoke another implementation under native locks.
	if targetTree != nil {
		return targetTree.Graft(remainingPath, source)
	}

	return nil
}

func isNilTree(source Tree) bool {
	v := reflect.ValueOf(source)

	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return v.IsNil()
	default:
		return false
	}
}

// cycleCheck checks whether source is parent itself or an ancestor
// of parent. Attaching it below parent would form a tree-structure cycle; this
// function does not inspect locks or detect mutex deadlocks.
func cycleCheck(source, parent *tree) bool {
	for {
		if source == parent {
			return true
		}

		link := parent.mount.Load()
		if link == nil {
			return false
		}

		parent = link.parent
	}
}

// graftNativeTree handles a concrete native source: its realization lock, ownership,
// inherited cache and mount link. Source has already passed the nil check.
func (t *tree) graftNativeTree(q pathQuery, source *tree) (Tree, string, error) {
	// Fail before taking the source lock, including a processor attempting
	// to graft its own tree or an already attached source back into the root.
	if source.mount.Load() != nil {
		return nil, "", fmt.Errorf("graft source is already attached; detach it before remounting")
	}
	if cycleCheck(source, t) {
		return nil, "", fmt.Errorf("graft would create a cycle")
	}

	source.realizeMu.Lock()
	defer source.realizeMu.Unlock()

	unlock := lockTopologyRoots(t, source)
	defer unlock()

	targetTree, remainingQuery := t.locateGraftTarget(q)

	parent, isNativeTarget := targetTree.(*tree)
	if !isNativeTarget {
		return targetTree, remainingQuery.localPath(), nil
	}

	// Recheck ownership with both topology roots locked: the source could
	// have moved while this operation waited to acquire its locks.
	if source.mount.Load() != nil {
		return nil, "", fmt.Errorf("graft source is already attached; detach it before remounting")
	}
	if cycleCheck(source, parent) {
		return nil, "", fmt.Errorf("graft would create a cycle")
	}

	name := remainingQuery.name(remainingQuery.level())
	if !validNativeName(parent, name) {
		return nil, "", fmt.Errorf("graft name %q is incompatible with enclosing path syntax", name)
	}
	if err := validateNativeNames(source, nativeParsers(parent)); err != nil {
		return nil, "", err
	}

	parent.mu.Lock()
	defer parent.mu.Unlock()

	mountParent := parent.createGraftParents(remainingQuery)
	source.prepareForGraft()

	if previousTree, ok := mountParent.children[name].(*tree); ok {
		previousTree.mount.Store(nil)
	}

	mountParent.children[name] = source
	source.mount.Store(&treeMount{parent: mountParent, name: name})

	return nil, "", nil
}

// prepareForGraft invalidates cached output without running processors. The
// current base is retained until a read supplies the new parent's content.
// Callers must hold its realizeMu and enclosing-root topology lock.
func (t *tree) prepareForGraft() {
	// A newer root contentVer transitively invalidates descendant caches.
	// pendingBase keeps standard descendant Set from using the old template.
	t.basePending.Store(true)
	t.realizedAt = time.Time{}
	t.realizedFrom = 0

	t.contentMu.Lock()
	t.content = nil
	t.contentVer++
	t.contentMu.Unlock()
}

// graftTree attaches a custom source by reference. Its implementation owns
// its internal state; native code only manages the destination child edge.
func (t *tree) graftTree(q pathQuery, source Tree) (Tree, string, error) {
	unlock := lockTopologyRoots(t)
	defer unlock()

	targetTree, remainingQuery := t.locateGraftTarget(q)

	parent, isNativeTarget := targetTree.(*tree)
	if !isNativeTarget {
		return targetTree, remainingQuery.localPath(), nil
	}

	name := remainingQuery.name(remainingQuery.level())
	if !validNativeName(parent, name) {
		return nil, "", fmt.Errorf("graft name %q is incompatible with enclosing path syntax", name)
	}

	parent.mu.Lock()
	defer parent.mu.Unlock()

	mountParent := parent.createGraftParents(remainingQuery)
	if previousTree, ok := mountParent.children[name].(*tree); ok {
		previousTree.mount.Store(nil)
	}

	mountParent.children[name] = source

	return nil, "", nil
}

// locateGraftTarget follows existing native parents until the final edge, the
// first missing edge, or a custom target. It does not mutate either tree.
// Callers must hold the receiver's enclosing-root topology lock.
func (t *tree) locateGraftTarget(q pathQuery) (Tree, pathQuery) {
	parent := t
	for !q.next().done() {
		child := parent.pickChild(q.head())
		if child == nil {
			return parent, q
		}

		q = q.next()
		if nativeChild, ok := child.(*tree); ok {
			parent = nativeChild
		} else {
			return child, q
		}
	}

	return parent, q
}

// createGraftParents fills the missing native chain after validation. Callers
// hold t.mu and its topology root until the source is attached, so readers
// cannot enter the new branch before it is complete.
func (t *tree) createGraftParents(q pathQuery) *tree {
	parent := t
	for !q.next().done() {
		child := parent.newSubTree(q.head())
		parent.children[q.head()] = child
		parent = child
		q = q.next()
	}

	return parent
}

func nativeParsers(node *tree) []driver.PathParser {
	var parsers []driver.PathParser
	for {
		parsers = append(parsers, node.driver)

		link := node.mount.Load()
		if link == nil {
			return parsers
		}

		node = link.parent
	}
}

func validPathName(parser driver.PathParser, name string) bool {
	path := parser.AppendPath("", name)

	return name != "" && parser.GetLevel(path) == 1 && parser.GetNameByLevel(path, 1) == name
}

func validNativeName(node *tree, name string) bool {
	for {
		if !validPathName(node.driver, name) {
			return false
		}

		link := node.mount.Load()
		if link == nil {
			return true
		}

		node = link.parent
	}
}

// validateNativeNames runs while native topology is stable. Native aliases
// must be addressable through every enclosing native receiver's path grammar;
// opaque foreign trees own their own path interpretation at the boundary.
func validateNativeNames(node *tree, parsers []driver.PathParser) error {
	parsers = append(parsers, node.driver)

	node.mu.RLock()
	defer node.mu.RUnlock()

	for name, child := range node.children {
		for _, parser := range parsers {
			if !validPathName(parser, name) {
				return fmt.Errorf("source node name %q is incompatible with enclosing path syntax", name)
			}
		}

		if nativeChild, ok := child.(*tree); ok {
			if err := validateNativeNames(nativeChild, parsers); err != nil {
				return err
			}
		}
	}

	return nil
}

func (t *tree) pendingBase() bool {
	for {
		if t.basePending.Load() {
			return true
		}

		link := t.mount.Load()
		if link == nil {
			return false
		}

		t = link.parent
	}
}

// processorContext isolates mutable path/parent fields and parent bytes from
// cached data and the configured default context. Params remain caller-owned.
func (t *tree) processorContext(rc *driver.RealizeContext) *driver.RealizeContext {
	if rc == nil {
		return nil
	}

	local := *rc
	local.TreePath = t.Path()
	local.ParentContent = slices.Clone(rc.ParentContent)

	return &local
}

func (t *tree) topologyRoot() *tree {
	for {
		link := t.mount.Load()
		if link == nil {
			return t
		}

		t = link.parent
	}
}

// lockTopologyRoots locks only the enclosing roots of the involved nodes.
// Root addresses impose a consistent lock order without a global mutex or ID
// allocator; references remain live throughout acquisition and release.
//
// A node can move while we wait for its old root. Rechecking the current roots
// after acquisition prevents editing that node under a stale ownership domain.
// On detach, the subtree root's own mutex becomes active again; there is no
// shared lock pointer to recursively rewrite in its descendants. Callers must
// not invoke processors or foreign Tree methods while these locks are held.
func lockTopologyRoots(nodes ...*tree) func() {
retry:
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
			return cmp.Compare(uintptr(unsafe.Pointer(a)), uintptr(unsafe.Pointer(b)))
		})

		for _, root := range roots {
			root.topologyMu.Lock()
		}

		unlock := func() {
			for _, root := range slices.Backward(roots) {
				root.topologyMu.Unlock()
			}
		}

		for _, node := range nodes {
			if node != nil && !slices.Contains(roots, node.topologyRoot()) {
				unlock()
				continue retry
			}
		}

		return unlock
	}
}
