package driver

import (
	"context"
	"time"
)

// Driver driver interface
type Driver interface {
	Name() string

	PathParser
	Realizer
	Modem
}

// PathParser path parser
type PathParser interface {
	// GetLevel get level from path
	GetLevel(path string) (level int)
	// GetNameByLevel get node name from path by level
	// return empty string if level is out of range
	GetNameByLevel(path string, level int) (name string)
	// AppendPath append path
	AppendPath(path, name string) (newPath string)
}

// Realizer realize rule
type Realizer interface {
	// Realize realize rule
	Realize(rc *RealizeContext, rule []byte, ops ...Processor) ([]byte, error)
}

// Modem Processors modem
type Modem interface {
	// ProcessorsForSave get Processors data for save
	Marshal(...Processor) ([]byte, error)

	// LoadProcessors load Processors from data
	Unmarshal(data []byte) ([]Processor, error)
}

// Processor rule processor
type Processor interface {
	// Path return target tree path, not necessary
	Path() string

	// Type return processor type
	Type() string
	// Process do process rule
	Process(rc *RealizeContext, before []byte) (after []byte, err error)

	// informatin
	Author() string
	CreatedAt() time.Time

	// Load load Processor from data
	Load([]byte) error
	// Save ...
	Save() []byte
}

// ParamAware is an OPTIONAL interface for processors whose output depends
// on request parameters carried by RealizeContext.Params.
//
// The tree splits a processor chain at the first ParamAware processor:
// everything before it forms the cacheable static base (realized and
// cached exactly as before), while from it on the processors form a
// dynamic layer that is re-applied on every GetWithContext call and whose
// result is returned to the caller only — it never lands in the node
// cache. This keeps lazy/instant/TTL semantics intact for the static
// base while allowing per-query differentiation.
type ParamAware interface {
	// ParamKeys returns the parameter names this processor depends on.
	// It may be empty when the keys cannot be determined statically;
	// implementing the interface alone already marks the processor dynamic.
	ParamKeys() []string
}

// RealizeContext carries runtime information for dynamic rule construction.
type RealizeContext struct {
	context.Context
	// TreePath is the path of the current tree node being processed.
	TreePath string
	// Params holds key-value pairs from the request, for template interpolation etc.
	Params map[string]string
	// ParentContent holds the realized content of the parent node.
	ParentContent []byte
}
