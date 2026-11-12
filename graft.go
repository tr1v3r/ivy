package ivy

import (
	"errors"
	"fmt"
	"reflect"
	"slices"
	"time"

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
// subtree. The parent must exist and the receiver root cannot be replaced.
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

	for i := 1; i <= q.level(); i++ {
		if q.name(i) == "" {
			return fmt.Errorf("graft path %q contains an empty segment", path)
		}
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

	targetTree, remainingQuery, err := t.locateGraftTarget(q)
	if err != nil {
		return nil, "", err
	}

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

	name := remainingQuery.head()
	if !validNativeName(parent, name) {
		return nil, "", fmt.Errorf("graft name %q is incompatible with enclosing path syntax", name)
	}

	if err := validateNativeNames(source, nativeParsers(parent)); err != nil {
		return nil, "", err
	}

	parent.mu.Lock()
	defer parent.mu.Unlock()

	source.prepareForGraft()

	if previousTree, ok := parent.children[name].(*tree); ok {
		previousTree.mount.Store(nil)
	}

	source.mount.Store(&treeMount{parent: parent, name: name})
	parent.children[name] = source

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

	targetTree, remainingQuery, err := t.locateGraftTarget(q)
	if err != nil {
		return nil, "", err
	}

	parent, isNativeTarget := targetTree.(*tree)
	if !isNativeTarget {
		return targetTree, remainingQuery.localPath(), nil
	}

	name := remainingQuery.head()
	if !validNativeName(parent, name) {
		return nil, "", fmt.Errorf("graft name %q is incompatible with enclosing path syntax", name)
	}

	parent.mu.Lock()
	defer parent.mu.Unlock()

	if previousTree, ok := parent.children[name].(*tree); ok {
		previousTree.mount.Store(nil)
	}

	parent.children[name] = source

	return nil, "", nil
}

// locateGraftTarget finds the native parent of the final edge, or the custom
// target that must handle the remaining local query. It does not process source
// state. Callers must hold the receiver's enclosing-root topology lock.
func (t *tree) locateGraftTarget(q pathQuery) (Tree, pathQuery, error) {
	parent := t
	for !q.next().done() {
		child := parent.pickChild(q.head())
		if child == nil {
			return nil, q, fmt.Errorf("graft parent at %q: %w", q.localPath(), ErrNotExistsTree)
		}

		q = q.next()
		if nativeChild, ok := child.(*tree); ok {
			parent = nativeChild
		} else {
			return child, q, nil
		}
	}

	return parent, q, nil
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
	parsers = append(slices.Clone(parsers), node.driver)

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
