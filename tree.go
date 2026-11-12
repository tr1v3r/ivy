package ivy

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/time/rate"

	"github.com/tr1v3r/ivy/driver"
)

var _ Tree = (*tree)(nil)

// tree is a rule tree structure.
type tree struct {
	name string // immutable source identity; mount aliases live on edges

	// A single parent link determines the effective path. Moving a subtree
	// updates only its root link, never every descendant's path or level.
	mount atomic.Pointer[treeMount]

	// Structural edits use the current enclosing root's mutex. Every node
	// has its own dormant mutex ready if it later becomes a detached root.
	topologyMu sync.Mutex

	mu       sync.RWMutex
	children map[string]Tree

	// for subtree
	driver driver.Driver

	// current node rule
	contentMu sync.RWMutex
	content   []byte
	// contentVer counts this node's content publications (bumped by every
	// set). Descendants read it together with the content and remember the
	// version they composed from (realizedFrom), so a node whose parent
	// re-realized can detect the staleness of its own cached composition
	// (issue #48) instead of serving it until its own TTL expires.
	contentVer uint64

	// base is the pre-processor content the chain is applied to: the root
	// template for the root node, the parent's realized content for children.
	// Realization always starts from base — never from a previous output —
	// so re-realization (instant mode, cache TTL expiry) cannot compound
	// non-idempotent processors.
	base []byte

	// procs/dynamicFrom form the node's processor chain. They are mutated
	// by Set/apply at runtime, so both the writes (apply) and every read
	// (realizeWithParentCtx's slow path, dynamicRealize) happen under
	// realizeMu (issue #47: apply used to write both fields unlocked while
	// concurrent Gets read them). The cached fast path never touches the
	// pair, so hot reads stay lock-free.
	procs       []driver.Processor
	dynamicFrom int

	// fallback is called when path resolution cannot find a matching
	// child. Swapped atomically by SetFallback for the same reason
	// (issue #47); a nil pointer means no fallback is installed.
	fallback atomic.Pointer[driver.Processor]

	// Lazy Mode: directives create topology during build, but static
	// processor chains are evaluated only when their nodes are accessed.
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
	// realizedFrom is the parent's contentVer this node last composed
	// from; guarded by realizeMu. A descent carrying a NEWER parent
	// version invalidates the cached composition (issue #48); an EQUAL or
	// OLDER version leaves it alone (older = stale carry, the cache is
	// already based on newer parent content than the caller saw).
	realizedFrom uint64

	// basePending defers eager Set on a mounted standard node until its
	// deferred ancestry first supplies a valid base. Atomic so creating a
	// child never needs to acquire its parent's realization lock.
	basePending atomic.Bool

	rlMu        sync.RWMutex
	rateLimiter *rate.Limiter

	// defaultCtx is swapped atomically by SetDefaultContext (issue #47):
	// it is read on every realize without a request context and copied
	// into new subtrees, both previously unsynchronized.
	defaultCtx atomic.Pointer[driver.RealizeContext]
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

// Path derives the effective location from mount edges rather than cached
// absolute metadata. Descendants immediately follow a remounted subtree root.
func (t *tree) Path() string {
	var names []string
	root := t
	for link := root.mount.Load(); link != nil; link = root.mount.Load() {
		names = append(names, link.name)
		root = link.parent
	}

	path := ""
	for _, name := range slices.Backward(names) {
		path = root.driver.AppendPath(path, name)
	}

	return path
}

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
	return t.setQuery(newPathQuery(t.driver, r.Path()), r.Processors())
}

func (t *tree) setQuery(q pathQuery, procs []driver.Processor) error {
	if q.done() {
		return t.apply(procs...)
	}

	child := t.getChild(q.head())
	rest := q.next()
	if nativeChild, ok := child.(*tree); ok {
		return nativeChild.setQuery(rest, procs)
	}

	return child.Set(NewDirective(rest.localPath(), procs...))
}

// Get retrieves static content at a path local to this receiver root. Each
// node realizes its static chain according to its own mode, then consumes one
// path segment and carries its (content, version) to the next native node.
// Neither lookup nor termination depends on a stored absolute node level.
// Foreign implementations receive only the remaining local path string.
func (t *tree) Get(path string) ([]byte, error) {
	if t == nil {
		return nil, ErrNotExistsTree
	}

	return t.getLocal(nil, path, false)
}

// getLocal restarts when ownership changes during descent. Mount identities
// are checked under realization locks, so an old parent's version can never
// populate a source that has moved to a different parent with a lower version.
func (t *tree) getLocal(rc *driver.RealizeContext, path string, contextual bool) ([]byte, error) {
	q := newPathQuery(t.driver, path)
	for {
		binding := t.mount.Load()
		content, version, err := t.ancestorContent(rc, binding)
		if errors.Is(err, errMountChanged) {
			continue
		}

		if err != nil {
			return nil, err
		}

		var value []byte
		if contextual {
			if rc != nil && version != 0 {
				rc.ParentContent = content
			}
			value, err = t.getContextWithParent(rc, content, version, q, binding)
		} else {
			value, err = t.getWithParent(content, version, q, binding)
		}

		if !errors.Is(err, errMountChanged) {
			return value, err
		}
	}
}

// ancestorContent makes a retained mounted handle a local view of the same
// inherited content as outer-root queries, even before the first outer query or
// after an ancestor refresh. Ancestor dynamic layers are intentionally skipped.
func (t *tree) ancestorContent(rc *driver.RealizeContext, binding *treeMount) ([]byte, uint64, error) {
	type snapshot struct {
		node    *tree
		binding *treeMount
	}

	var ancestors []snapshot
	for link := binding; link != nil; {
		parent := link.parent
		link = parent.mount.Load()
		ancestors = append(ancestors, snapshot{parent, link})
	}

	var content []byte
	var version uint64
	for _, ancestor := range slices.Backward(ancestors) {
		if err := ancestor.node.realizeWithParentCtx(rc, content, version, ancestor.binding); err != nil {
			return nil, 0, fmt.Errorf("realize ancestor at %q: %w", ancestor.node.Path(), err)
		}

		content, version = ancestor.node.getWithVersion()
		if ancestor.node.mount.Load() != ancestor.binding {
			return nil, 0, errMountChanged
		}
	}

	return content, version, nil
}

// pathQuery parses once when the driver supports SegmentParser and carries
// a consumed-segment cursor through native recursion. Otherwise it uses the
// driver's per-call lookups with the same local-coordinate semantics.
type pathQuery struct {
	path   string
	driver driver.Driver
	segs   []string
	parsed bool
	pos    int // number of consumed segments, independent of node location
}

// newPathQuery parses path once if the driver supports segment parsing.
func newPathQuery(d driver.Driver, path string) pathQuery {
	if sp, ok := d.(driver.SegmentParser); ok {
		if segs, supported := sp.ParseSegments(path); supported {
			return pathQuery{path: path, driver: d, segs: segs, parsed: true}
		}
	}

	return pathQuery{path: path, driver: d}
}

// level returns the level of the queried path.
func (q pathQuery) level() int {
	if q.parsed {
		return len(q.segs)
	}

	return q.driver.GetLevel(q.path)
}

// name returns the segment at the given 1-based level ("" when out of
// range).
func (q pathQuery) name(level int) string {
	if q.parsed {
		if level < 1 || level > len(q.segs) {
			return ""
		}

		return q.segs[level-1]
	}

	return q.driver.GetNameByLevel(q.path, level)
}

func (q pathQuery) done() bool { return q.pos == q.level() }

func (q pathQuery) head() string { return q.name(q.pos + 1) }

func (q pathQuery) next() pathQuery {
	q.pos++
	return q
}

// localPath is only needed at a foreign Tree boundary. Native recursion keeps
// the same parsed segments and advances a cursor without allocating substrings.
func (q pathQuery) localPath() string {
	path := ""
	for i := q.pos + 1; i <= q.level(); i++ {
		path = q.driver.AppendPath(path, q.name(i))
	}

	return path
}

// getWithParent descends towards the queried path carrying the parent's
// realized content. The child's inheritance decision happens atomically
// inside its realization (see realizeWithParentCtx), not as a separate
// write before it — that separation is what allowed the lazy+TTL race
// where a late inheritance write clobbered a concurrently refreshed
// result.
func (t *tree) getWithParent(parentContent []byte, parentVer uint64, q pathQuery, binding *treeMount) (value []byte, err error) {
	defer func() {
		if t.mount.Load() != binding {
			value, err = nil, errMountChanged
		}
	}()

	// Only the static prefix of the chain is realized into the cache;
	// the dynamic layer needs request params and is skipped here.
	if err := t.realizeWithParent(parentContent, parentVer, binding); err != nil {
		return nil, fmt.Errorf("realize rule on %s fail: %w", t.Path(), err)
	}

	if q.done() {
		return t.get(), nil
	}

	if child := t.pickChild(q.head()); child != nil {
		// carry this node's freshly published (content, version) pair so
		// the child can detect that its cached composition is based on an
		// outdated parent realization (issue #48)
		content, ver := t.getWithVersion()
		rest := q.next()
		if nativeChild, ok := child.(*tree); ok {
			childBinding, err := queryMount(nativeChild, t, q.head())
			if err != nil {
				return nil, err
			}
			return nativeChild.getWithParent(content, ver, rest, childBinding)
		}

		return child.Get(rest.localPath())
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

	return t.getLocal(rc, path, true)
}

// getContextWithParent is the GetWithContext descent carrying the parent's
// realized content for atomic inheritance (see getWithParent).
func (t *tree) getContextWithParent(rc *driver.RealizeContext, parentContent []byte, parentVer uint64, q pathQuery, binding *treeMount) (value []byte, err error) {
	defer func() {
		if t.mount.Load() != binding {
			value, err = nil, errMountChanged
		}
	}()

	if rc != nil {
		rc.TreePath = t.Path()
	}

	if err := t.realizeWithParentCtx(rc, parentContent, parentVer, binding); err != nil {
		return nil, fmt.Errorf("realize rule on %s fail: %w", t.Path(), err)
	}

	if q.done() {
		return t.dynamicRealize(rc)
	}

	if child := t.pickChild(q.head()); child != nil {
		// one (content, version) read serves the context leak
		// (ParentContent), the descent's inheritance argument and the
		// child's staleness check (issue #48)
		content, ver := t.getWithVersion()
		if rc != nil {
			rc.ParentContent = content
		}

		rest := q.next()
		if nativeChild, ok := child.(*tree); ok {
			childBinding, err := queryMount(nativeChild, t, q.head())
			if err != nil {
				return nil, err
			}
			return nativeChild.getContextWithParent(rc, content, ver, rest, childBinding)
		}

		if rc != nil {
			rc.TreePath = q.driver.AppendPath(t.Path(), q.head())
		}

		return child.GetWithContext(rc, rest.localPath())
	}

	return t.doFallback(rc, t.get())
}

// staticProcs returns the cacheable prefix of the processor chain:
// everything before the first param-aware processor.
//
// Callers must hold realizeMu (read or write): the (procs, dynamicFrom)
// pair is only consistent under the lock since Set/apply replaces it
// in place (issue #47).
func (t *tree) staticProcs() []driver.Processor {
	return t.procs[:t.dynamicFrom]
}

// dynamicRealize applies the request-dependent tail of the processor chain
// on top of the cached static base. The result goes to the caller only —
// it is never written into t.content, so the cache stays param-independent
// and no lock is needed beyond reading the base.
func (t *tree) dynamicRealize(rc *driver.RealizeContext) ([]byte, error) {
	content := t.get()
	// snapshot the dynamic layer under the read lock so a concurrent
	// Set/apply cannot tear the (procs, dynamicFrom) pair (issue #47)
	t.realizeMu.RLock()
	dynamic := t.procs[t.dynamicFrom:]
	t.realizeMu.RUnlock()
	if len(dynamic) == 0 {
		return content, nil // fully static chain: nothing to do per request
	}

	content = slices.Clone(content)
	rc = t.processorContext(rc)
	content, err := t.driver.Realize(rc, content, dynamic...)
	if err != nil {
		return nil, fmt.Errorf("realize dynamic rule on %s fail: %w", t.Path(), err)
	}

	return content, nil
}

// doFallback calls the fallback processor if set, otherwise returns content unchanged.
func (t *tree) doFallback(rc *driver.RealizeContext, content []byte) ([]byte, error) {
	if fb := t.fallback.Load(); fb != nil {
		return (*fb).Process(t.processorContext(rc), slices.Clone(content))
	}

	return content, nil
}

// SetFallback sets a processor to handle cases where path resolution
// cannot find a matching child node, and propagates it to all subtrees.
// A nil processor CLEARS the fallback (missing paths then return the
// node content unchanged), matching the pre-atomic behavior.
func (t *tree) SetFallback(proc driver.Processor) {
	if proc == nil {
		t.fallback.Store(nil) // clear, not a pointer to a nil interface
	} else {
		t.fallback.Store(&proc)
	}

	for _, child := range t.getChildren() {
		child.SetFallback(proc)
	}
}

// SetDefaultContext sets the default RealizeContext for this tree and all subtrees.
func (t *tree) SetDefaultContext(rc *driver.RealizeContext) {
	t.defaultCtx.Store(rc)
	for _, child := range t.getChildren() {
		child.SetDefaultContext(rc)
	}
}

// Has checks for an exact local path, without invoking fallback processors.
func (t *tree) Has(path string) bool {
	return t.hasQuery(newPathQuery(t.driver, path))
}

func (t *tree) hasQuery(q pathQuery) bool {
	if q.done() {
		// Preserve the existing named-root Has("/") behavior.
		return q.pos > 0 || t.name == ""
	}

	child := t.pickChild(q.head())
	if child == nil {
		return false
	}

	rest := q.next()
	if rest.done() {
		return true
	}

	if nativeChild, ok := child.(*tree); ok {
		return nativeChild.hasQuery(rest)
	}

	return child.Has(rest.localPath())
}

// Del deletes a node at a local path. Missing paths are a no-op.
func (t *tree) Del(path string) error {
	return t.delQuery(newPathQuery(t.driver, path))
}

func (t *tree) delQuery(q pathQuery) error {
	if q.done() {
		return fmt.Errorf("root node can not be deleted")
	}

	rest := q.next()
	if rest.done() {
		return t.deleteNode(q.head())
	}

	if child := t.pickChild(q.head()); child != nil {
		if nativeChild, ok := child.(*tree); ok {
			return nativeChild.delQuery(rest)
		}

		return child.Del(rest.localPath())
	}

	return nil
}

// ShowStruct returns the tree structure as JSON.
func (t *tree) ShowStruct() []byte {
	t.mu.RLock()
	children := make(map[string]Tree, len(t.children))
	maps.Copy(children, t.children)

	t.mu.RUnlock()
	m := make(map[string]json.RawMessage, len(children))
	for name, child := range children {
		m[name] = child.ShowStruct()
	}

	d, _ := json.Marshal(m)
	return d
}

// deleteNode delete a node from tree.
func (t *tree) deleteNode(name string) error {
	unlock := lockTopologyRoots(t)
	defer unlock()

	t.mu.Lock()
	defer t.mu.Unlock()

	if old, ok := t.children[name].(*tree); ok {
		old.mount.Store(nil)
	}

	delete(t.children, name)
	return nil
}

// getChild get a child tree.
// if not found, create a new sub tree and return it
func (t *tree) getChild(name string) Tree {
	if child := t.pickChild(name); child != nil {
		return child
	}

	// Recheck while inserting so concurrent Set calls cannot replace each
	// other's newly created intermediate nodes. No processor runs here.
	unlock := lockTopologyRoots(t)
	defer unlock()

	t.mu.Lock()
	defer t.mu.Unlock()

	if child := t.children[name]; child != nil {
		return child
	}

	child := t.newSubTree(name)
	t.children[name] = child
	return child
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

// newSubTree creates a child with inherited configuration.
func (t *tree) newSubTree(name string) Tree {
	// one pair read pins (content, version) together: three separate
	// locked reads could observe a parent set() in between and hand the
	// child an inherited snapshot labeled with a version it never came
	// from (review finding on #80; near-zero impact since build is
	// single-threaded and lazy children recompose on first access, but
	// the pair read closes the window for free)
	basePending := t.pendingBase()
	snapshot, parentVer := t.getWithVersion()

	child := &tree{
		name: name,

		driver:      t.driver,
		lazyMode:    t.lazyMode,
		instantMode: t.instantMode,
		cacheTTL:    t.cacheTTL,

		base: snapshot,
		// content starts as the inherited snapshot so never-realized
		// intermediate nodes still carry the parent content down the chain
		// (standard-mode build); realize overwrites it from base.
		content: snapshot,
		// the snapshot derives from the parent's CURRENT publication, so
		// descents can compare against it until the first own realization
		// stores what it actually composed from (issue #48)
		realizedFrom: parentVer,
		children:     make(map[string]Tree),
	}

	// inherit the management fields through their atomic slots (issue #47:
	// plain field copies raced SetFallback/SetDefaultContext)
	child.mount.Store(&treeMount{parent: t, name: name})
	child.basePending.Store(basePending)
	child.defaultCtx.Store(t.defaultCtx.Load())
	child.fallback.Store(t.fallback.Load())
	return child
}

// updateDirective parse raw rule Processor to tree node.
//
// The field writes, the cache invalidation and (in standard mode) the
// re-realization all happen inside ONE realizeMu write-locked critical
// section (issue #47: the writes used to be unsynchronized, racing
// concurrent Gets; keeping them in a single section also means no reader
// can pass the fast path between the chain swap and the invalidation and
// then pin the OLD chain's output as fresh). Standard-mode realize under
// the write lock matches the previous effective behavior — the slow path
// of realizeWithParentCtx always ran driver.Realize under this lock.
func (t *tree) apply(procs ...driver.Processor) error {
	t.realizeMu.Lock()
	defer t.realizeMu.Unlock()

	t.procs = procs
	t.dynamicFrom = dynamicSplit(procs)
	// invalidateCache, inlined to stay in the same critical section:
	// mark the cached realization stale so the next realize re-runs the
	// chain (without this, the fast path in realizeWithParentCtx keeps
	// serving the old content forever when cacheTTL == 0).
	t.realizedAt = time.Time{}
	if t.lazyMode || t.pendingBase() {
		return nil
	}

	// standard mode: realize only the static prefix at build time; the
	// dynamic layer is applied per query in GetWithContext. The chain is
	// realized from the node's own base, which captures the parent
	// publication the node was created from — keep realizedFrom pinned to
	// that version so descents do not spuriously invalidate.
	return t.realizeLocked(t.defaultCtx.Load(), t.getBase(), t.realizedFrom)
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

func (t *tree) realizeWithParent(parentContent []byte, parentVer uint64, binding *treeMount) error {
	return t.realizeWithParentCtx(t.defaultCtx.Load(), parentContent, parentVer, binding)
}

// realizeWithParentCtx realizes the node's chain, deciding the lazy-mode
// inheritance from parentContent INSIDE the write-locked critical section,
// atomically with the realization decision itself. With the check and the
// write separated (the old inherit), a TTL could expire between them, or a
// concurrent re-realization could complete in between: the node then either
// composed from its own previous output (compounding) or had its fresh
// result clobbered with raw parent content while realizedAt stayed fresh.
func (t *tree) realizeWithParentCtx(rc *driver.RealizeContext, parentContent []byte, parentVer uint64, binding *treeMount) error {
	if rc == nil {
		rc = t.defaultCtx.Load()
	}

	// Fast path: read lock checks whether realization can be skipped.
	// The processor chain is NOT read here — it lives under the write
	// lock only, so the hot cached path stays free of chain access.
	//
	// Freshness is min(own TTL, parent publication) (issue #48): the
	// cached composition is only served while it is based on the parent
	// version the caller carries. parentVer < realizedFrom means the
	// caller's carry is OLDER than what the cache already composed from —
	// keep the newer cache.
	t.realizeMu.RLock()
	if t.mount.Load() != binding {
		t.realizeMu.RUnlock()
		return errMountChanged
	}

	if t.fresh(parentVer) {
		t.realizeMu.RUnlock()
		return nil
	}

	t.realizeMu.RUnlock()

	// Slow path: write lock performs the actual realization.
	t.realizeMu.Lock()
	defer t.realizeMu.Unlock()

	if t.mount.Load() != binding {
		return errMountChanged
	}

	// Double-check after acquiring the write lock, so concurrent goroutines
	// that passed the fast path do not realize twice.
	if t.fresh(parentVer) {
		return nil
	}

	return t.realizeLocked(rc, parentContent, parentVer)
}

// fresh reports whether the cached realization may be served for a
// descent carrying parentVer. Callers must hold realizeMu (read or
// write). It encodes the min(own TTL, parent publication) freshness rule
// of issue #48: an own-TTL-fresh cache composed from an outdated parent
// version is stale and must recompose.
func (t *tree) fresh(parentVer uint64) bool {
	if t.instantMode || t.realizedAt.IsZero() {
		return false
	}

	if t.cacheTTL != 0 && time.Since(t.realizedAt) >= t.cacheTTL {
		return false
	}

	return parentVer <= t.realizedFrom
}

// realizeLocked performs the realization itself. Callers must hold
// realizeMu for writing. It reads the processor chain here, where the
// (procs, dynamicFrom) pair is consistent (issue #47).
func (t *tree) realizeLocked(rc *driver.RealizeContext, parentContent []byte, parentVer uint64) error {
	// Rate limiting applies to lazy/instant/cache modes only; standard mode
	// realizes during build and is never limited.
	if (t.lazyMode || t.instantMode || t.cacheTTL > 0) && !t.allow() {
		return ErrRateLimited
	}

	// Realize from the pre-processor base, never from the node's previous
	// output: re-realization must be idempotent for non-idempotent chains.
	// Every child, including a standard child below a deferred parent,
	// composes from the carried parent publication. A nonzero version means
	// a parent is present even when its content is nil. Retain the new base
	// so a subsequent standard-mode Set cannot reuse a stale snapshot.
	base := t.getBase()
	if parentVer != 0 {
		base = parentContent
		t.contentMu.Lock()
		t.base = base
		t.contentMu.Unlock()
	}

	base = slices.Clone(base)
	if rc != nil && parentVer != 0 {
		local := *rc
		local.ParentContent = parentContent
		rc = &local
	}
	rc = t.processorContext(rc)

	rule, err := t.driver.Realize(rc, base, t.staticProcs()...)
	if err != nil {
		return fmt.Errorf("realize rule fail: %w", err)
	}

	t.set(rule)
	t.realizedFrom = parentVer

	t.realizedAt = time.Now()
	t.basePending.Store(false)
	return nil
}

func (t *tree) set(rule []byte) {
	t.contentMu.Lock()
	defer t.contentMu.Unlock()

	t.content = rule
	t.contentVer++ // a new publication for descendants' staleness checks
}

// getWithVersion returns the node's current content and its publication
// version as one pair (both under contentMu), so a parent refresh between
// two separate reads can never hand a child a torn combination.
func (t *tree) getWithVersion() (content []byte, version uint64) {
	t.contentMu.RLock()
	defer t.contentMu.RUnlock()

	return t.content, t.contentVer
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
