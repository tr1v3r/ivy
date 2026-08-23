package driver

import (
	"fmt"
	"time"
)

var _ Processor = (*RawProcessor)(nil)

// RawProcessor wraps an arbitrary function as a Processor.
// It cannot be serialized: Load always fails and Save yields nil.
type RawProcessor struct {
	author    string
	createdAt time.Time

	// Proc is the transformation; it receives the current content and
	// returns the next one.
	Proc func(ctx *RealizeContext, before []byte) (after []byte, err error)
}

// Type returns an empty type marker.
func (op *RawProcessor) Type() string { return "" }

// Path returns an empty target path.
func (op *RawProcessor) Path() string { return "" }

// Author returns the processor author.
func (op *RawProcessor) Author() string { return op.author }

// CreatedAt returns the processor creation time.
func (op *RawProcessor) CreatedAt() time.Time { return op.createdAt }

// Load always fails: raw processors cannot be serialized.
func (op *RawProcessor) Load(_ []byte) error { return ErrSerializeNotSupport }

// Save returns no payload.
func (op *RawProcessor) Save() []byte { return nil }

// Process delegates to the wrapped Proc function.
func (op *RawProcessor) Process(ctx *RealizeContext, before []byte) (after []byte, err error) {
	return op.Proc(ctx, before)
}

var _ Processor = (*CombinedProcessor)(nil)

// CombinedProcessor chains multiple processors into a single one.
// Processors are applied sequentially: each one's output becomes the next one's input.
type CombinedProcessor struct {
	procs     []Processor
	author    string
	createdAt time.Time
}

// CombineProcessor creates a processor that chains the given processors sequentially.
func CombineProcessor(procs ...Processor) *CombinedProcessor {
	return &CombinedProcessor{procs: procs}
}

// Type returns "combined".
func (c *CombinedProcessor) Type() string { return "combined" }

// Path returns an empty target path.
func (c *CombinedProcessor) Path() string { return "" }

// Author returns the processor author.
func (c *CombinedProcessor) Author() string { return c.author }

// CreatedAt returns the processor creation time.
func (c *CombinedProcessor) CreatedAt() time.Time { return c.createdAt }

// Load always fails: combined processors cannot be serialized.
func (c *CombinedProcessor) Load([]byte) error { return ErrSerializeNotSupport }

// Save returns no payload.
func (c *CombinedProcessor) Save() []byte { return nil }

// Process applies every chained processor sequentially.
func (c *CombinedProcessor) Process(rc *RealizeContext, before []byte) ([]byte, error) {
	var err error
	for _, proc := range c.procs {
		if proc == nil {
			continue
		}
		if before, err = proc.Process(rc, before); err != nil {
			return nil, fmt.Errorf("combined processors do %s on %s fail: %w", proc.Type(), proc.Path(), err)
		}
	}
	return before, nil
}
