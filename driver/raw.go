package driver

import (
	"fmt"
	"time"
)

var _ Processor = (*RawProcessor)(nil)

type RawProcessor struct {
	author    string
	createdAt time.Time

	Proc func(ctx *RealizeContext, before []byte) (after []byte, err error)
}

func (op *RawProcessor) Type() string         { return "" }
func (op *RawProcessor) Path() string         { return "" }
func (op *RawProcessor) Author() string       { return op.author }
func (op *RawProcessor) CreatedAt() time.Time { return op.createdAt }
func (op *RawProcessor) Load(_ []byte) error  { return ErrSerializeNotSupport }
func (op *RawProcessor) Save() []byte         { return nil }
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

func (c *CombinedProcessor) Type() string         { return "combined" }
func (c *CombinedProcessor) Path() string         { return "" }
func (c *CombinedProcessor) Author() string       { return c.author }
func (c *CombinedProcessor) CreatedAt() time.Time { return c.createdAt }
func (c *CombinedProcessor) Load([]byte) error    { return ErrSerializeNotSupport }
func (c *CombinedProcessor) Save() []byte         { return nil }
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
