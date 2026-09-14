package driver

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
)

// check interface
var _ Driver = (*TOMLDriver)(nil)

var _ SegmentParser = (*TOMLDriver)(nil)

// ParseSegments forwards to the embedded parser when it supports segment
// parsing, letting the engine split a queried path once per descent
// instead of once per tree level. Custom embedded parsers without
// SegmentParser support report ok=false and keep the per-call path.
func (d TOMLDriver) ParseSegments(path string) ([]string, bool) {
	if sp, ok := d.PathParser.(SegmentParser); ok {
		return sp.ParseSegments(path)
	}
	return nil, false
}

// NewTOMLDriver create a new toml driver
func NewTOMLDriver() *TOMLDriver {
	return &TOMLDriver{
		PathParser: SlashPathParser,
		Realizer:   new(StdRealizer),
		Modem: &GeneralModem[*TOMLProcessor]{
			Marshaler:   json.Marshal,
			Unmarshaler: json.Unmarshal,
		},
	}
}

// TOMLDriver is a driver for TOML type rule tree
type TOMLDriver struct {
	PathParser
	Realizer
	Modem
}

// Name return driver name
func (TOMLDriver) Name() string { return "toml" }

var _ Processor = (*TOMLProcessor)(nil)

// TOMLProcessor is a Processor for TOML type rule tree
type TOMLProcessor struct {
	// P is the target path of the Processor
	P string `json:"path"`

	// T is the type of the Processor
	T string `json:"type"`
	// TOMLPath is the toml key path of the Processor (dot-separated)
	TOMLPath string `json:"toml_path"`
	// V is the value of the Processor
	V []byte `json:"value"`

	// A is the author of the Processor
	A string `json:"author"`
	// C is the create time of the Processor
	C time.Time `json:"created_at"`
}

// Type returns the operation type (create/append/set/replace/delete).
func (op *TOMLProcessor) Type() string { return op.T }

// Path returns the target tree path of the Processor.
func (op *TOMLProcessor) Path() string { return op.P }

// Author returns the processor author.
func (op *TOMLProcessor) Author() string { return op.A }

// CreatedAt returns the processor creation time.
func (op *TOMLProcessor) CreatedAt() time.Time { return op.C }

// Load populates the processor from its JSON serialization.
func (op *TOMLProcessor) Load(data []byte) error {
	if err := json.Unmarshal(data, op); err != nil {
		return fmt.Errorf("unmarshal fail: %w", err)
	}
	return nil
}

// Save returns the JSON serialization of the processor.
func (op *TOMLProcessor) Save() []byte {
	data, _ := json.Marshal(op)
	return data
}

// Process applies the typed operation to the TOML document.
func (op *TOMLProcessor) Process(_ *RealizeContext, before []byte) (after []byte, err error) {
	m := make(map[string]any)
	if len(before) > 0 {
		if err := toml.Unmarshal(before, &m); err != nil {
			return nil, fmt.Errorf("unmarshal toml fail: %w", err)
		}
	}

	segments := splitTOMLPath(op.TOMLPath)

	switch op.T {
	case "create", "append":
		err = tomlCreate(m, segments, op.V)
	case "set":
		err = tomlSet(m, segments, op.V)
	case "replace":
		err = tomlReplace(m, segments, op.V)
	case "delete":
		err = tomlDelete(m, segments)
	default:
		return nil, fmt.Errorf("unknown Processor type: %s", op.T)
	}
	if err != nil {
		return nil, err
	}

	result, err := toml.Marshal(&m)
	if err != nil {
		return nil, fmt.Errorf("marshal toml fail: %w", err)
	}
	return result, nil
}

// --- path helpers ---

func splitTOMLPath(path string) []string {
	path = strings.Trim(path, ".")
	if path == "" {
		return nil
	}
	return strings.Split(path, ".")
}

// tomlNavigate navigates to the parent map and returns it along with the final key name.
func tomlNavigate(m map[string]any, segments []string) (map[string]any, string, error) {
	if len(segments) == 0 {
		return nil, "", fmt.Errorf("empty toml path")
	}
	cur := m
	for _, seg := range segments[:len(segments)-1] {
		v, ok := cur[seg]
		if !ok {
			return nil, "", fmt.Errorf("key not found: %s", seg)
		}
		sub, ok := v.(map[string]any)
		if !ok {
			return nil, "", fmt.Errorf("key %s is not a table", seg)
		}
		cur = sub
	}
	return cur, segments[len(segments)-1], nil
}

// tomlNavigateOrCreate navigates to the parent map, creating intermediate tables as needed.
func tomlNavigateOrCreate(m map[string]any, segments []string) (map[string]any, string, error) {
	if len(segments) == 0 {
		return nil, "", fmt.Errorf("empty toml path")
	}
	cur := m
	for _, seg := range segments[:len(segments)-1] {
		v, ok := cur[seg]
		if !ok {
			sub := make(map[string]any)
			cur[seg] = sub
			cur = sub
			continue
		}
		sub, ok := v.(map[string]any)
		if !ok {
			return nil, "", fmt.Errorf("key %s is not a table", seg)
		}
		cur = sub
	}
	return cur, segments[len(segments)-1], nil
}

// parseTOMLValue unmarshals a TOML value from bytes into a Go value.
// Wraps the value in "v = " to form valid TOML for parsing.
func parseTOMLValue(data []byte) (any, error) {
	if len(data) == 0 {
		return nil, nil
	}
	wrapped := append([]byte("v = "), data...)
	var m map[string]any
	if err := toml.Unmarshal(wrapped, &m); err != nil {
		return nil, fmt.Errorf("parse toml value fail: %w", err)
	}
	return m["v"], nil
}

// --- operations ---

func tomlCreate(m map[string]any, segments []string, value []byte) error {
	parent, lastKey, err := tomlNavigateOrCreate(m, segments)
	if err != nil {
		return err
	}

	v, err := parseTOMLValue(value)
	if err != nil {
		return err
	}

	existing, exists := parent[lastKey]
	if !exists {
		parent[lastKey] = v
		return nil
	}

	// If existing value is a slice, append to it
	if slice, ok := existing.([]any); ok {
		if vs, ok := v.([]any); ok {
			parent[lastKey] = append(slice, vs...)
		} else {
			parent[lastKey] = append(slice, v)
		}
		return nil
	}

	parent[lastKey] = v
	return nil
}

func tomlSet(m map[string]any, segments []string, value []byte) error {
	parent, lastKey, err := tomlNavigateOrCreate(m, segments)
	if err != nil {
		return err
	}

	v, err := parseTOMLValue(value)
	if err != nil {
		return err
	}

	parent[lastKey] = v
	return nil
}

func tomlReplace(m map[string]any, segments []string, value []byte) error {
	parent, lastKey, err := tomlNavigate(m, segments)
	if err != nil {
		return err
	}

	v, err := parseTOMLValue(value)
	if err != nil {
		return err
	}

	parent[lastKey] = v
	return nil
}

func tomlDelete(m map[string]any, segments []string) error {
	parent, lastKey, err := tomlNavigate(m, segments)
	if err != nil {
		return err
	}

	if _, ok := parent[lastKey]; !ok {
		return fmt.Errorf("key not found: %s", lastKey)
	}
	delete(parent, lastKey)
	return nil
}
