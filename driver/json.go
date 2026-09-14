package driver

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/tidwall/sjson"
)

// check interface
var _ Driver = (*JSONDriver)(nil)

var _ SegmentParser = (*JSONDriver)(nil)

// ParseSegments forwards to the embedded parser when it supports segment
// parsing, letting the engine split a queried path once per descent
// instead of once per tree level. Custom embedded parsers without
// SegmentParser support report ok=false and keep the per-call path.
func (d JSONDriver) ParseSegments(path string) ([]string, bool) {
	if sp, ok := d.PathParser.(SegmentParser); ok {
		return sp.ParseSegments(path)
	}
	return nil, false
}

// NewJSONDriver create a new json driver
func NewJSONDriver() *JSONDriver {
	return &JSONDriver{
		PathParser: SlashPathParser,
		Realizer:   new(StdRealizer),
		Modem: &GeneralModem[*JSONProcessor]{
			Marshaler:   json.Marshal,
			Unmarshaler: json.Unmarshal,
		},
	}
}

// JSONDriver is a driver for JSON type rule tree
type JSONDriver struct {
	PathParser
	Realizer
	Modem
}

// Name return driver name
func (JSONDriver) Name() string { return "json" }

var _ Processor = (*JSONProcessor)(nil)

// JSONProcessor is a Processor for JSON type rule tree
type JSONProcessor struct {
	// P is the target path of the Processor
	P string `json:"path"`

	// T is the type of the Processor
	T string `json:"type"`
	// JSONPath is the json path of the Processor
	JSONPath string `json:"json_path"`
	// V is the value of the Processor
	V []byte `json:"value"`

	// A is the author of the Processor
	A string `json:"author"`
	// C is the create time of the Processor
	C time.Time `json:"created_at"`
}

// Type returns the operation type (create/append/set/replace/delete).
func (op *JSONProcessor) Type() string { return op.T }

// Path returns the target tree path of the Processor.
func (op *JSONProcessor) Path() string { return op.P }

// Author returns the processor author.
func (op *JSONProcessor) Author() string { return op.A }

// CreatedAt returns the processor creation time.
func (op *JSONProcessor) CreatedAt() time.Time { return op.C }

// Load populates the processor from its JSON serialization.
func (op *JSONProcessor) Load(data []byte) error {
	if err := json.Unmarshal(data, op); err != nil {
		return fmt.Errorf("unmarshal fail: %w", err)
	}
	return nil
}

// Save returns the JSON serialization of the processor.
func (op *JSONProcessor) Save() []byte {
	data, _ := json.Marshal(op)
	return data
}

// Process applies the typed operation to the JSON content via sjson.
func (op *JSONProcessor) Process(_ *RealizeContext, before []byte) (after []byte, err error) {
	switch op.T {
	case "create", "append", "replace":
		return sjson.SetBytes(before, op.JSONPath, op.V)
	case "set":
		// SetRawBytes embeds V verbatim; an empty V would splice nothing into
		// the document and emit invalid JSON such as {"a":1,"family":}.
		if len(op.V) == 0 {
			return nil, fmt.Errorf("json processor set on %s with empty value would produce invalid JSON", op.JSONPath)
		}
		return sjson.SetRawBytes(before, op.JSONPath, op.V)
	case "delete":
		return sjson.DeleteBytes(before, op.JSONPath)
	default:
		return nil, fmt.Errorf("unknown Processor type: %s", op.T)
	}
}
