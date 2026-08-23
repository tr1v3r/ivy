package driver

import (
	"time"
)

var _ Driver = (*DummyDriver)(nil)

var DummyModem = &GeneralModem[Processor]{
	Marshaler:   func(any) ([]byte, error) { return nil, nil },
	Unmarshaler: func([]byte, any) error { return nil },
}

// NewDummyDriver ...
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

func (DummyDriver) Name() string { return "dummy" }

var _ Processor = (*DummyProcessor)(nil)

type DummyProcessor struct{}

func (op *DummyProcessor) Type() string { return "dummy" }
func (op *DummyProcessor) Path() string { return "" }
func (op *DummyProcessor) Process(_ *RealizeContext, before []byte) (after []byte, err error) {
	return nil, nil
}
func (op *DummyProcessor) Author() string       { return "dummy" }
func (op *DummyProcessor) CreatedAt() time.Time { return time.Now() }
func (op *DummyProcessor) Load([]byte) error    { return nil }
func (op *DummyProcessor) Save() []byte         { return nil }
