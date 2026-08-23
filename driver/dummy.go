package driver

import (
	"time"
)

var _ Driver = (*DummyDriver)(nil)

// DummyModem is a no-op Modem for drivers whose processors never
// serialize: Marshal returns nil and Unmarshal succeeds without reading.
var DummyModem = &GeneralModem[Processor]{
	Marshaler:   func(any) ([]byte, error) { return nil, nil },
	Unmarshaler: func([]byte, any) error { return nil },
}

// NewDummyDriver creates a driver with slash paths, standard realization
// and the no-op DummyModem; useful in tests and as a struct embedding base.
func NewDummyDriver() *DummyDriver {
	return &DummyDriver{
		PathParser: SlashPathParser,
		Realizer:   new(StdRealizer),
		Modem:      DummyModem,
	}
}

// DummyDriver return a dummy driver
type DummyDriver struct {
	PathParser
	Realizer
	Modem
}

// Name returns "dummy".
func (DummyDriver) Name() string { return "dummy" }

var _ Processor = (*DummyProcessor)(nil)

// DummyProcessor is a no-op Processor returning nil content without error.
type DummyProcessor struct{}

// Type returns "dummy".
func (op *DummyProcessor) Type() string { return "dummy" }

// Path returns an empty target path.
func (op *DummyProcessor) Path() string { return "" }

// Process returns nil content without error.
func (op *DummyProcessor) Process(_ *RealizeContext, _ []byte) (after []byte, err error) {
	return nil, nil
}

// Author returns the fixed author "dummy".
func (op *DummyProcessor) Author() string { return "dummy" }

// CreatedAt returns the current time.
func (op *DummyProcessor) CreatedAt() time.Time { return time.Now() }

// Load is a no-op that always succeeds.
func (op *DummyProcessor) Load([]byte) error { return nil }

// Save returns no payload.
func (op *DummyProcessor) Save() []byte { return nil }
