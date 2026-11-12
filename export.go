// Package ivy is a hierarchical content-construction engine.
//
// Content starts from a root template and flows down a tree addressed by
// paths. Each node carries a directive — a chain of driver.Processor
// transformations — applied on top of the content inherited from its
// parent, so querying a path returns the template progressively shaped by
// every level above it.
//
// Trees come in four evaluation modes (standard, lazy, instant, cache TTL)
// and are managed by a Forest, which owns tree builders so trees can be
// rebuilt on refresh. Processors implementing driver.ParamAware form a
// per-request dynamic layer whose output is computed per query and never
// cached, enabling param-differentiated queries without polluting the
// static cache.
package ivy

import (
	"time"

	"golang.org/x/time/rate"

	"github.com/tr1v3r/ivy/driver"
)

// Forest manages a collection of trees.
type Forest interface {
	Register(...TreeBuilder)
	Append(...TreeBuilder) Forest

	Build() Forest
	// Refresh refreshes all trees.
	// interval is optional and only first value is useful when set.
	// Blocks when the interval is set.
	Refresh(interval ...time.Duration)
	// RefreshTree refreshes the specified tree.
	RefreshTree(name string)

	Get(name string) Tree
	Set(tree Tree)
	// GetVal retrieves a value from the named tree at the given path.
	GetVal(treeName, path string) (val []byte, err error)
	// GetValWithContext retrieves a value with runtime context.
	GetValWithContext(rc *driver.RealizeContext, treeName, path string) (val []byte, err error)

	Info() string

	// SetRateLimit sets a global rate limit for all GetVal calls as a fallback.
	SetRateLimit(r rate.Limit, burst int)

	// SetDefaultContext sets the default RealizeContext on all trees in the forest.
	SetDefaultContext(rc *driver.RealizeContext)
}

// Tree is a hierarchical node structure with path-based access. All operation
// paths are local to the receiver root (a leading delimiter does not make them
// global). Each implementation is responsible for its own concurrency safety.
type Tree interface {
	// Name returns the tree name.
	Name() string

	// Path returns the effective mount location in the enclosing tree.
	// Returns "" for an unattached root. Operation paths are local to the
	// receiver root, even when Path reports a nonempty mount location.
	Path() string

	// Set adds or updates a directive on the tree.
	Set(Directive) error

	// Get retrieves the value at the given path.
	Get(path string) (val []byte, err error)

	// GetWithContext retrieves a value with runtime context for dynamic construction.
	GetWithContext(rc *driver.RealizeContext, path string) (val []byte, err error)

	// Has checks if a node exists at the given path.
	Has(path string) bool

	// Del deletes a node at the given path.
	Del(path string) error

	// Graft directly attaches source at a path local to this tree root,
	// replacing the entire target subtree. The native implementation creates
	// missing intermediate native nodes without running processors.
	// Replacing the receiver root is not allowed. Source is not copied.
	// Implementations own positioning, inheritance, cache and concurrency
	// behavior. For native sources, the native implementation requires
	// detached ownership and invalidates caches without running processors.
	// A nil source wraps ErrNotExistsTree in that implementation.
	Graft(path string, source Tree) error

	// ShowStruct returns the tree structure as JSON.
	ShowStruct() []byte

	// SetRateLimit sets a rate limit for Get calls on this tree.
	SetRateLimit(r rate.Limit, burst int)

	// SetFallback sets a processor to handle cases where path resolution
	// cannot find a matching child node.
	SetFallback(proc driver.Processor)

	// SetDefaultContext sets the default RealizeContext used by realize when
	// no request-scoped context is provided (e.g. via Get or during build).
	SetDefaultContext(rc *driver.RealizeContext)
}

// Directive defines a path and the processors to apply at that path.
type Directive interface {
	// Path returns the path this directive applies to.
	Path() string
	// Processors returns the processors for this directive.
	Processors() []driver.Processor
}
