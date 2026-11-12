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
		return fmt.Errorf("graft source must not be nil")
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
	var native *tree
	if ct, ok := source.(*tree); ok {
		native = ct
		// Reject an ancestor before taking its realization lock, including
		// when a processor attempts to graft its own currently realizing tree.
		if nativeAncestor(native, t) {
			return fmt.Errorf("graft would create a cycle")
		}
		// Reject ownership before the realization lock, so even a callback
		// on an attached source can fail fast rather than lock itself again.
		if native.mount.Load() != nil {
			return fmt.Errorf("graft source is already attached; detach it before remounting")
		}
	}
	foreign, rest, err := t.graftNative(q, source, native)
	if err != nil {
		return err
	}
	if foreign != nil {
		return foreign.Graft(rest, source)
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

func nativeAncestor(ancestor, node *tree) bool {
	for {
		if ancestor == node {
			return true
		}
		link := node.mount.Load()
		if link == nil {
			return false
		}
		node = link.parent
	}
}

// graftNative holds topology stable while resolving native parents. At a
// foreign boundary it returns the remaining local path and releases all locks
// before the caller invokes the foreign implementation's public Graft method.
func (t *tree) graftNative(q pathQuery, source Tree, native *tree) (Tree, string, error) {
	if native != nil {
		native.realizeMu.Lock()
		defer native.realizeMu.Unlock()
	}
	unlock := lockTopologyRoots(t, native)
	defer unlock()
	parent := t
	for !q.next().done() {
		child := parent.pickChild(q.head())
		if child == nil {
			return nil, "", fmt.Errorf("graft parent does not exist at %q", q.localPath())
		}
		q = q.next()
		if ct, ok := child.(*tree); ok {
			parent = ct
		} else {
			return child, q.localPath(), nil
		}
	}
	if native != nil {
		if nativeAncestor(native, parent) {
			return nil, "", fmt.Errorf("graft would create a cycle")
		}
		if native.mount.Load() != nil {
			return nil, "", fmt.Errorf("graft source is already attached; detach it before remounting")
		}
	}
	if !validNativeName(parent, q.head()) {
		return nil, "", fmt.Errorf("graft name %q is incompatible with enclosing path syntax", q.head())
	}
	if native != nil {
		if err := validateNativeNames(native, nativeParsers(parent)); err != nil {
			return nil, "", err
		}
	}
	parent.mu.Lock()
	defer parent.mu.Unlock()
	if old, ok := parent.children[q.head()].(*tree); ok {
		old.mount.Store(nil)
	}
	if native != nil {
		// Only the source root needs invalidation: its next publication has
		// a newer contentVer, which transitively invalidates all descendants.
		// pendingBase also prevents standard descendant Set from eagerly
		// evaluating against the old source template before that first read.
		native.basePending.Store(true)
		native.realizedAt = time.Time{}
		native.realizedFrom = 0
		native.contentMu.Lock()
		native.base = nil
		native.content = nil
		native.contentVer++
		native.contentMu.Unlock()
		native.mount.Store(&treeMount{parent: parent, name: q.head()})
	}
	parent.children[q.head()] = source
	return nil, "", nil
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
		if ct, ok := child.(*tree); ok {
			if err := validateNativeNames(ct, parsers); err != nil {
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
