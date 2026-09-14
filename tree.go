package ivy

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"github.com/tr1v3r/ivy/driver"
)

var _ Tree = (*tree)(nil)

// tree is a rule tree structure.
type tree struct {
	name string // node name
	path string // node path

	mu       sync.RWMutex
	children map[string]Tree

	// for subtree
	driver driver.Driver
	level  int

	// current node rule
	contentMu sync.RWMutex
	content   []byte

	// base is the pre-processor content the chain is applied to: the root
	// template for the root node, the parent's realized content for children.
	// Realization always starts from base — never from a previous output —
	// so re-realization (instant mode, cache TTL expiry) cannot compound
	// non-idempotent processors.
	base []byte

	// procs processor array
	// only set when tree build, only concurrent reads, so no mutex needed
	procs []driver.Processor
	// dynamicFrom is the index of the first param-aware processor in procs.
	// procs[:dynamicFrom] is the cacheable static prefix; procs[dynamicFrom:]
	// is the dynamic layer re-applied per GetWithContext call (never cached).
	dynamicFrom int

	// fallback is called when path resolution cannot find a matching child.
	fallback driver.Processor

	// Lazy Mode:
	// In Lazy Mode, tree nodes are not created or calculated during initialization.
	// Only the root node exists initially, and other nodes are dynamically initialized
	// and calculated when accessed. This mode employs lazy evaluation, saving memory
	// and computation resources, particularly useful in scenarios where only a subset
	// of the nodes are accessed.
	lazyMode bool
	// Instant Mode:
	// In Instant Mode, every time a node is accessed, its data is recalculated
	// and the entire path from the root to the accessed node is refreshed.
	// Even if the nodes have been previously created or calculated, they are
	// forcefully recalculated to ensure up-to-date data. This mode emphasizes
	// real-time computation, ideal for scenarios requiring frequent updates
	// and data consistency.
	instantMode bool

	// Cache TTL:
	// When cacheTTL > 0, the cached result expires after this duration.
	// A zero value means the cache never expires (standard lazy behavior).
	cacheTTL   time.Duration
	realizeMu  sync.RWMutex
	realizedAt time.Time

	rlMu        sync.RWMutex
	rateLimiter *rate.Limiter

	defaultCtx *driver.RealizeContext
}

func (t *tree) lazy() *tree {
	t.lazyMode = true
	return t
}

func (t *tree) instant() *tree {
	t.instantMode = true
	return t
}

func (t *tree) cache(ttl time.Duration) *tree {
	t.cacheTTL = ttl
	return t
}

func (t *tree) build(rules ...Directive) error {
	for _, r := range byLevel(t.driver, rules) {
		if err := t.Set(r); err != nil {
			return fmt.Errorf("apply rule fail: %w", err)
		}
	}
	return nil
}

func (t *tree) Name() string { return t.name }
func (t *tree) Path() string { return t.path }

// allow checks if the rate limiter allows this request.
func (t *tree) allow() bool {
	t.rlMu.RLock()
	limiter := t.rateLimiter
	t.rlMu.RUnlock()
	return limiter == nil || limiter.Allow()
}

// SetRateLimit sets a rate limit for Get calls on this tree.
func (t *tree) SetRateLimit(r rate.Limit, burst int) {
	t.rlMu.Lock()
	defer t.rlMu.Unlock()
	t.rateLimiter = rate.NewLimiter(r, burst)
}

func (t *tree) Set(r Directive) error {
	if level := t.driver.GetLevel(r.Path()); t.level == level { // check if level matched, include root node
		return t.apply(r.Processors()...)
	}
	return t.getChild(t.driver.GetNameByLevel(r.Path(), t.level+1)).Set(r)
}

// Get retrieves the rule data at the given path.
//
// The tree is organized as a hierarchical structure where each node corresponds to a
// path level. Get traverses the tree level by level, from root to the target node:
//
//  1. Realize: Apply this node's processors to compute its content.
//     In standard mode, this happens once during tree building.
//     In lazy/instant/cache mode, this happens on each access (with caching behavior
//     varying by mode). Rate limiting, if configured, is enforced at this step.
//
//  2. Descend: If the target path is deeper than this node's level, look up the child
//     corresponding to the next path segment. Before recursing into the child, inherit
//     passes this node's realized content to the child so it has a base to build upon
//     (relevant for lazy mode where the child may not yet have its own content).
//
//  3. Return: If this node matches the target level, return its content directly.
//
// The recursion forms a chain: root → level1 → level2 → ... → target node.
// Each level realizes its own content before passing control to the next, ensuring
// the content flows correctly down the tree hierarchy.
func (t *tree) Get(path string) ([]byte, error) {
	if t == nil {
		return nil, ErrNotExistsTree
	}
	return t.getWithParent(nil, path)
}

// getWithParent descends towards path carrying the parent's realized
// content. The child's inheritance decision happens atomically inside its
// realization (see realizeWithParentCtx), not as a separate write before
// it — that separation is what allowed the lazy+TTL race where a late
// inheritance write clobbered a concurrently refreshed result.
func (t *tree) getWithParent(parentContent []byte, path string) ([]byte, error) {
	// Only the static prefix of the chain is realized into the cache;
	// the dynamic layer needs request params and is skipped here.
	if err := t.realizeWithParent(parentContent, t.staticProcs()); err != nil {
		return nil, fmt.Errorf("realize rule on %s fail: %w", t.Path(), err)
	}

	if t.driver.GetLevel(path) == t.level {
		return t.get(), nil
	}

	if child := t.pickChild(t.driver.GetNameByLevel(path, t.level+1)); child != nil {
		if ct, ok := child.(*tree); ok {
			return ct.getWithParent(t.get(), path)
		}
		return child.Get(path)
	}
	return t.doFallback(nil, t.get())
}

// GetWithContext retrieves rule data with runtime context for dynamic construction.
//
// The static prefix of each node's processor chain follows the exact same
// realization and caching path as Get (standard/lazy/instant/TTL all keep
// their semantics). At the TARGET node only, the dynamic layer — processors
// from the first param-aware one onwards — is re-applied on top of the
// cached static base with the request context, and the result is returned
// to the caller WITHOUT being written back to the node. The cache therefore
// stays param-independent: different params never pollute each other.
//
// Dynamic processors on INTERMEDIATE nodes of the queried path are not
// applied; descent uses their static content (inherit/ParentContent).
func (t *tree) GetWithContext(rc *driver.RealizeContext, path string) ([]byte, error) {
	if t == nil {
		return nil, ErrNotExistsTree
	}
	return t.getContextWithParent(rc, nil, path)
}

// getContextWithParent is the GetWithContext descent carrying the parent's
// realized content for atomic inheritance (see getWithParent).
func (t *tree) getContextWithParent(rc *driver.RealizeContext, parentContent []byte, path string) ([]byte, error) {
	if rc != nil {
		rc.TreePath = t.path
	}

	if err := t.realizeWithParentCtx(rc, parentContent, t.staticProcs()); err != nil {
		return nil, fmt.Errorf("realize rule on %s fail: %w", t.Path(), err)
	}

	if t.driver.GetLevel(path) == t.level {
		return t.dynamicRealize(rc)
	}

	if child := t.pickChild(t.driver.GetNameByLevel(path, t.level+1)); child != nil {
		if rc != nil {
			rc.ParentContent = t.get()
		}
		if ct, ok := child.(*tree); ok {
			return ct.getContextWithParent(rc, t.get(), path)
		}
		return child.GetWithContext(rc, path)
	}
	return t.doFallback(rc, t.get())
}

// staticProcs returns the cacheable prefix of the processor chain:
// everything before the first param-aware processor.
func (t *tree) staticProcs() []driver.Processor {
	return t.procs[:t.dynamicFrom]
}

// dynamicRealize applies the request-dependent tail of the processor chain
// on top of the cached static base. The result goes to the caller only —
// it is never written into t.content, so the cache stays param-independent
// and no lock is needed beyond reading the base.
func (t *tree) dynamicRealize(rc *driver.RealizeContext) ([]byte, error) {
	content := t.get()
	if t.dynamicFrom >= len(t.procs) {
		return content, nil // fully static chain: nothing to do per request
	}
	content, err := t.driver.Realize(rc, content, t.procs[t.dynamicFrom:]...)
	if err != nil {
		return nil, fmt.Errorf("realize dynamic rule on %s fail: %w", t.Path(), err)
	}
	return content, nil
}

// doFallback calls the fallback processor if set, otherwise returns content unchanged.
func (t *tree) doFallback(rc *driver.RealizeContext, content []byte) ([]byte, error) {
	if t.fallback == nil {
		return content, nil
	}
	return t.fallback.Process(rc, content)
}

// SetFallback sets a processor to handle cases where path resolution
// cannot find a matching child node, and propagates it to all subtrees.
func (t *tree) SetFallback(proc driver.Processor) {
	t.fallback = proc
	t.mu.RLock()
	defer t.mu.RUnlock()
	for _, child := range t.children {
		if ct, ok := child.(*tree); ok {
			ct.SetFallback(proc)
		}
	}
}

// SetDefaultContext sets the default RealizeContext for this tree and all subtrees.
func (t *tree) SetDefaultContext(rc *driver.RealizeContext) {
	t.defaultCtx = rc
	t.mu.RLock()
	defer t.mu.RUnlock()
	for _, child := range t.children {
		if ct, ok := child.(*tree); ok {
			ct.SetDefaultContext(rc)
		}
	}
}

// Has check if has node in path
func (t *tree) Has(path string) bool {
	if t.driver.GetLevel(path) == t.level { // check level
		return t.Name() == t.driver.GetNameByLevel(path, t.level)
	}
	if tree := t.pickChild(t.driver.GetNameByLevel(path, t.level+1)); tree != nil {
		return tree.Has(path)
	}
	return false
}

// Del delete a node from tree.
func (t *tree) Del(path string) error {
	if level := t.driver.GetLevel(path); level == 0 {
		return fmt.Errorf("root node can not be deleted")
	} else if t.level+1 == level {
		return t.deleteNode(t.driver.GetNameByLevel(path, level))
	}

	if child := t.pickChild(t.driver.GetNameByLevel(path, t.level+1)); child != nil {
		return child.Del(path)
	}
	return nil
}

// ShowStruct return tree struct.
func (t *tree) ShowStruct() []byte {
	m := make(map[string]json.RawMessage)
	for _, v := range t.getChildren() {
		m[v.Name()] = v.ShowStruct()
	}
	d, _ := json.Marshal(m)
	return d
}

// deleteNode delete a node from tree.
func (t *tree) deleteNode(name string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.children, name)

	return nil
}

// getChild get a child tree.
// if not found, create a new sub tree and return it
func (t *tree) getChild(name string) (tree Tree) {
	if tree = t.pickChild(name); tree != nil {
		return tree
	}
	tree = t.newSubTree(name)
	t.Graft(tree)
	return tree
}

func (t *tree) getChildren() (children []Tree) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	for _, v := range t.children {
		children = append(children, v)
	}
	return children
}

// pickChild get a child tree.
// if not found, return nil
func (t *tree) pickChild(name string) Tree {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.children[name]
}

// Graft graft a sub tree
func (t *tree) Graft(child Tree) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.children[child.Name()] = child
}

// newSubTree create a new sub tree.
// name cannot be empty
func (t *tree) newSubTree(name string) Tree {
	return &tree{
		name: name,
		path: t.driver.AppendPath(t.path, name),

		defaultCtx: t.defaultCtx,
		fallback:   t.fallback,

		driver:      t.driver,
		lazyMode:    t.lazyMode,
		instantMode: t.instantMode,
		cacheTTL:    t.cacheTTL,

		level: t.level + 1,
		base:  t.get(),
		// content starts as the inherited snapshot so never-realized
		// intermediate nodes still carry the parent content down the chain
		// (standard-mode build); realize overwrites it from base.
		content:  t.get(),
		children: make(map[string]Tree),
	}
}

// updateDirective parse raw rule Processor to tree node.
func (t *tree) apply(procs ...driver.Processor) error {
	t.procs = procs
	t.dynamicFrom = dynamicSplit(procs)
	t.invalidateCache()
	if t.lazyMode {
		return nil
	}
	// standard mode: realize only the static prefix at build time;
	// the dynamic layer is applied per query in GetWithContext.
	return t.realize(t.staticProcs())
}

// invalidateCache marks the cached realization as stale so the next
// realize re-runs the chain. Set() may replace a node's processors after
// the node has already been realized; without invalidation the fast path
// in realizeWithContext keeps serving the old content forever when
// cacheTTL == 0 (which caches indefinitely).
func (t *tree) invalidateCache() {
	t.realizeMu.Lock()
	defer t.realizeMu.Unlock()
	t.realizedAt = time.Time{}
}

// dynamicSplit returns the index of the first param-aware processor.
// Everything from that index on produces request-dependent output.
func dynamicSplit(procs []driver.Processor) int {
	for i, proc := range procs {
		if proc == nil {
			continue
		}
		// IsParamAware also looks through *CombinedProcessor chains, so a
		// combined chain containing a param-aware processor stays in the
		// dynamic layer instead of caching request-specific output.
		if driver.IsParamAware(proc) {
			return i
		}
	}
	return len(procs)
}

func (t *tree) realize(procs []driver.Processor) error {
	return t.realizeWithParentCtx(t.defaultCtx, nil, procs)
}

func (t *tree) realizeWithParent(parentContent []byte, procs []driver.Processor) error {
	return t.realizeWithParentCtx(t.defaultCtx, parentContent, procs)
}

// realizeWithParentCtx realizes the node's chain, deciding the lazy-mode
// inheritance from parentContent INSIDE the write-locked critical section,
// atomically with the realization decision itself. With the check and the
// write separated (the old inherit), a TTL could expire between them, or a
// concurrent re-realization could complete in between: the node then either
// composed from its own previous output (compounding) or had its fresh
// result clobbered with raw parent content while realizedAt stayed fresh.
func (t *tree) realizeWithParentCtx(rc *driver.RealizeContext, parentContent []byte, procs []driver.Processor) error {
	if rc == nil {
		rc = t.defaultCtx
	}
	// Fast path: read lock checks whether realization can be skipped.
	t.realizeMu.RLock()
	if !t.instantMode && !t.realizedAt.IsZero() && (t.cacheTTL == 0 || time.Since(t.realizedAt) < t.cacheTTL) {
		t.realizeMu.RUnlock()
		return nil
	}
	t.realizeMu.RUnlock()

	// Slow path: write lock performs the actual realization.
	t.realizeMu.Lock()
	defer t.realizeMu.Unlock()
	// Double-check after acquiring the write lock, so concurrent goroutines
	// that passed the fast path do not realize twice.
	if !t.instantMode && !t.realizedAt.IsZero() && (t.cacheTTL == 0 || time.Since(t.realizedAt) < t.cacheTTL) {
		return nil
	}

	// Rate limiting applies to lazy/instant/cache modes only; standard mode
	// realizes during build and is never limited.
	if (t.lazyMode || t.instantMode || t.cacheTTL > 0) && !t.allow() {
		return ErrRateLimited
	}

	// Realize from the pre-processor base, never from the node's previous
	// output: re-realization must be idempotent for non-idempotent chains.
	// Lazy nodes compose from the parent's realized content carried down
	// by getWithParent; the decision is made here, under the write lock,
	// together with the realization it feeds.
	base := t.getBase()
	if t.lazyMode && parentContent != nil {
		base = parentContent
	}

	rule, err := t.driver.Realize(rc, base, procs...)
	if err != nil {
		return fmt.Errorf("realize rule fail: %w", err)
	}
	t.set(rule)

	t.realizedAt = time.Now()
	return nil
}

func (t *tree) set(rule []byte) {
	t.contentMu.Lock()
	defer t.contentMu.Unlock()
	t.content = rule
}

// get return current node rule.
func (t *tree) get() (rule []byte) {
	t.contentMu.RLock()
	defer t.contentMu.RUnlock()
	return t.content
}

// getBase return the pre-processor base content.
func (t *tree) getBase() (base []byte) {
	t.contentMu.RLock()
	defer t.contentMu.RUnlock()
	return t.base
}

// byLevel sort rules by path level
func byLevel[R Directive](driver driver.Driver, rules []R) []R {
	by[R](func(x, y R) bool { return driver.GetLevel(x.Path()) < driver.GetLevel(y.Path()) }).Sort(rules)
	return rules
}
