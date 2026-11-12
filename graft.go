package ivy

import (
	"fmt"
	"slices"

	"golang.org/x/time/rate"

	"github.com/tr1v3r/ivy/driver"
)

// GraftAt attaches a copy of src at path in dst, replacing the entire existing
// subtree there, if any. dst must be a root tree; the parent must already
// exist and path must not name the root. Both trees (including their
// descendants) must be built by ivy.
//
// Each copied node keeps its evaluation mode, processors, realizer, fallback,
// default context and rate-limit settings. Paths use dst's parser. The source
// template and caches are discarded: the mounted root inherits its new parent's
// content. Standard nodes are evaluated before publication only when all their
// ancestors are standard; otherwise they wait for access. A changed parent
// publication invalidates its children's caches regardless of their modes.
//
// The source tree is not modified. Node topology, caches and limiters are
// independent, but driver, processor and default-context objects are shared;
// callers must not mutate these objects concurrently. Errors leave the old
// subtree attached (processor side effects cannot be rolled back). Readers see
// either the old or the fully prepared new subtree. Serialize structural writes
// (GraftAt, Graft, Del, and Set creating nodes) with each other; the snapshot is
// not a transaction across concurrent source edits.
func GraftAt(dst Tree, path string, src Tree) error {
	root, ok := dst.(*tree)
	if !ok || root == nil {
		return fmt.Errorf("graft destination must be an ivy tree")
	}
	source, ok := src.(*tree)
	if !ok || source == nil {
		return fmt.Errorf("graft source must be an ivy tree")
	}

	if root.level != 0 {
		return fmt.Errorf("graft destination must be a root tree")
	}
	q := newPathQuery(root.driver, path)
	if q.level() == 0 {
		return fmt.Errorf("graft path must name a descendant, not the root")
	}
	parent := root
	ancestors := []*tree{root}
	for level := root.level + 1; level <= q.level(); level++ {
		if q.name(level) == "" {
			return fmt.Errorf("graft path %q contains an empty segment", path)
		}
		if level == q.level() {
			break
		}
		next, ok := parent.pickChild(q.name(level)).(*tree)
		if !ok || next == nil {
			return fmt.Errorf("graft parent for %q does not exist or is not an ivy tree", path)
		}
		parent = next
		ancestors = append(ancestors, parent)
	}

	replacement, err := cloneGraft(source, root.driver, parent.path, q.name(q.level()), parent.level+1, make(map[*tree]bool))
	if err != nil {
		return err
	}

	// Only realize an eager ancestry. Mounting a child must not trigger a
	// lazy/instant/TTL parent's processors just to obtain its content.
	eager := true
	var content []byte
	var version uint64
	for _, ancestor := range ancestors {
		if ancestor.lazyMode || ancestor.instantMode || ancestor.cacheTTL != 0 {
			eager = false
			break
		}
		if err := ancestor.realizeWithParent(content, version); err != nil {
			return fmt.Errorf("prepare graft parent: %w", err)
		}
		content, version = ancestor.getWithVersion()
	}
	if err := prepareGraft(replacement, eager, content, version); err != nil {
		return err
	}
	parent.mu.Lock()
	parent.children[replacement.name] = replacement
	parent.mu.Unlock()
	return nil
}

// cloneGraft copies nodes without copying mutexes or cached publications. The
// recursion stack rejects cycles from legacy raw Graft calls; repeated nodes in
// a DAG become independent copies.
func cloneGraft(src *tree, parser driver.PathParser, parentPath, name string, level int, visiting map[*tree]bool) (*tree, error) {
	if visiting[src] {
		return nil, fmt.Errorf("graft source contains a cycle")
	}
	visiting[src] = true
	defer delete(visiting, src)

	node := &tree{
		name:        name,
		path:        parser.AppendPath(parentPath, name),
		level:       level,
		driver:      &graftDriver{Driver: src.driver, parser: parser},
		lazyMode:    src.lazyMode,
		instantMode: src.instantMode,
		cacheTTL:    src.cacheTTL,
		grafted:     true,
		children:    make(map[string]Tree),
	}
	node.basePending.Store(true)
	src.realizeMu.RLock()
	node.procs = slices.Clone(src.procs)
	node.dynamicFrom = src.dynamicFrom
	src.realizeMu.RUnlock()
	node.defaultCtx.Store(src.defaultCtx.Load())
	node.fallback.Store(src.fallback.Load())
	src.rlMu.RLock()
	if src.rateLimiter != nil {
		node.rateLimiter = rate.NewLimiter(src.rateLimiter.Limit(), src.rateLimiter.Burst())
	}
	src.rlMu.RUnlock()
	for _, child := range src.getChildren() {
		ct, ok := child.(*tree)
		if !ok || ct == nil {
			return nil, fmt.Errorf("graft source contains a non-ivy child")
		}
		childName := ct.name
		childPath := parser.AppendPath(node.path, childName)
		if childName == "" || parser.GetLevel(childPath) != level+1 || parser.GetNameByLevel(childPath, level+1) != childName {
			return nil, fmt.Errorf("graft child name %q is incompatible with destination paths", childName)
		}
		clone, err := cloneGraft(ct, parser, node.path, childName, level+1, visiting)
		if err != nil {
			return nil, err
		}
		node.children[childName] = clone
	}
	return node, nil
}

func prepareGraft(node *tree, eager bool, parentContent []byte, parentVer uint64) error {
	eager = eager && !node.lazyMode && !node.instantMode && node.cacheTTL == 0
	if eager {
		node.base = parentContent
		if err := node.realizeWithParent(parentContent, parentVer); err != nil {
			return fmt.Errorf("prepare graft at %q: %w", node.path, err)
		}
	}
	content, version := node.getWithVersion()
	for _, child := range node.children {
		if err := prepareGraft(child.(*tree), eager, content, version); err != nil {
			return err
		}
	}
	return nil
}

// graftContext isolates the context fields that processors may write without
// changing a shared source default context or the destination parent's bytes.
func (t *tree) graftContext(rc *driver.RealizeContext) *driver.RealizeContext {
	if rc == nil {
		return nil
	}
	local := *rc
	local.TreePath = t.path
	local.ParentContent = slices.Clone(rc.ParentContent)
	return &local
}

// graftDriver keeps the source's format execution but uses one consistent path
// syntax throughout the destination, including future Set calls and descendants.
type graftDriver struct {
	driver.Driver
	parser driver.PathParser
}

func (d *graftDriver) GetLevel(path string) int { return d.parser.GetLevel(path) }
func (d *graftDriver) GetNameByLevel(path string, level int) string {
	return d.parser.GetNameByLevel(path, level)
}
func (d *graftDriver) AppendPath(path, name string) string {
	return d.parser.AppendPath(path, name)
}
func (d *graftDriver) ParseSegments(path string) ([]string, bool) {
	if parser, ok := d.parser.(driver.SegmentParser); ok {
		return parser.ParseSegments(path)
	}
	return nil, false
}
