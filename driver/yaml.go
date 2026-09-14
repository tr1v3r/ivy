package driver

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

var _ Driver = (*YAMLDriver)(nil)

// NewYAMLDriver creates a driver for YAML content with slash-separated
// paths and JSON-encoded processor serialization.
func NewYAMLDriver() *YAMLDriver {
	return &YAMLDriver{
		PathParser: SlashPathParser,
		Realizer:   new(StdRealizer),
		Modem: &GeneralModem[*YAMLProcessor]{
			Marshaler:   json.Marshal,
			Unmarshaler: json.Unmarshal,
		},
	}
}

// YAMLDriver is a driver for YAML type rule tree
type YAMLDriver struct {
	PathParser
	Realizer
	Modem
}

// Name returns "yaml".
func (YAMLDriver) Name() string { return "yaml" }

var _ Processor = (*YAMLProcessor)(nil)

// YAMLProcessor is a Processor for YAML type rule tree.
// YAMLPath is dot-separated; V holds a YAML-syntax value.
type YAMLProcessor struct {
	// P is the target path of the Processor
	P string `json:"path"`

	// T is the type of the Processor
	T string `json:"type"`
	// YAMLPath is the yaml key path of the Processor (dot-separated)
	YAMLPath string `json:"yaml_path"`
	// V is the value of the Processor, in YAML syntax
	V []byte `json:"value"`

	// A is the author of the Processor
	A string `json:"author"`
	// C is the create time of the Processor
	C time.Time `json:"created_at"`
}

// Type returns the operation type (create/append/set/replace/delete).
func (op *YAMLProcessor) Type() string { return op.T }

// Path returns the target tree path of the Processor.
func (op *YAMLProcessor) Path() string { return op.P }

// Author returns the processor author.
func (op *YAMLProcessor) Author() string { return op.A }

// CreatedAt returns the processor creation time.
func (op *YAMLProcessor) CreatedAt() time.Time { return op.C }

// Load populates the processor from its JSON serialization.
func (op *YAMLProcessor) Load(data []byte) error {
	if err := json.Unmarshal(data, op); err != nil {
		return fmt.Errorf("unmarshal fail: %w", err)
	}
	return nil
}

// Save returns the JSON serialization of the processor.
func (op *YAMLProcessor) Save() []byte {
	data, _ := json.Marshal(op)
	return data
}

// Process applies the typed operation to the YAML document.
func (op *YAMLProcessor) Process(_ *RealizeContext, before []byte) (after []byte, err error) {
	m := make(map[string]any)
	if len(before) > 0 {
		dec := yaml.NewDecoder(bytes.NewReader(before))
		if err := dec.Decode(&m); err != nil && !errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("unmarshal yaml fail: %w", err)
		}
		// yaml.Unmarshal-style single-shot decoding silently keeps only the
		// first document; refuse multi-document content instead of dropping
		// documents 2..N without a trace.
		var extra any
		if err := dec.Decode(&extra); err == nil {
			return nil, fmt.Errorf("multi-document yaml content is not supported: documents beyond the first would be dropped")
		} else if !errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("unmarshal yaml fail: %w", err)
		}
	}

	segments := splitYAMLPath(op.YAMLPath)

	switch op.T {
	case "create", "append":
		err = yamlCreate(m, segments, op.V)
	case "set":
		err = yamlSet(m, segments, op.V)
	case "replace":
		err = yamlReplace(m, segments, op.V)
	case "delete":
		err = yamlDelete(m, segments)
	default:
		return nil, fmt.Errorf("unknown Processor type: %s", op.T)
	}
	if err != nil {
		return nil, err
	}

	result, err := yaml.Marshal(&m)
	if err != nil {
		return nil, fmt.Errorf("marshal yaml fail: %w", err)
	}
	return result, nil
}

// --- path helpers ---

func splitYAMLPath(path string) []string {
	path = strings.Trim(path, ".")
	if path == "" {
		return nil
	}
	return strings.Split(path, ".")
}

// yamlNavigate navigates to the parent map and returns it along with the final key name.
func yamlNavigate(m map[string]any, segments []string) (map[string]any, string, error) {
	if len(segments) == 0 {
		return nil, "", fmt.Errorf("empty yaml path")
	}
	cur := m
	for _, seg := range segments[:len(segments)-1] {
		v, ok := cur[seg]
		if !ok {
			return nil, "", fmt.Errorf("key not found: %s", seg)
		}
		sub, ok := v.(map[string]any)
		if !ok {
			return nil, "", fmt.Errorf("key %s is not a map", seg)
		}
		cur = sub
	}
	return cur, segments[len(segments)-1], nil
}

// yamlNavigateOrCreate navigates to the parent map, creating intermediate maps as needed.
func yamlNavigateOrCreate(m map[string]any, segments []string) (map[string]any, string, error) {
	if len(segments) == 0 {
		return nil, "", fmt.Errorf("empty yaml path")
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
			return nil, "", fmt.Errorf("key %s is not a map", seg)
		}
		cur = sub
	}
	return cur, segments[len(segments)-1], nil
}

// parseYAMLValue unmarshals a YAML value from bytes into a Go value.
func parseYAMLValue(data []byte) (any, error) {
	if len(data) == 0 {
		return nil, nil
	}
	var v any
	if err := yaml.Unmarshal(data, &v); err != nil {
		return nil, fmt.Errorf("parse yaml value fail: %w", err)
	}
	return v, nil
}

// --- operations ---

func yamlCreate(m map[string]any, segments []string, value []byte) error {
	parent, lastKey, err := yamlNavigateOrCreate(m, segments)
	if err != nil {
		return err
	}

	v, err := parseYAMLValue(value)
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

func yamlSet(m map[string]any, segments []string, value []byte) error {
	parent, lastKey, err := yamlNavigateOrCreate(m, segments)
	if err != nil {
		return err
	}

	v, err := parseYAMLValue(value)
	if err != nil {
		return err
	}

	parent[lastKey] = v
	return nil
}

func yamlReplace(m map[string]any, segments []string, value []byte) error {
	parent, lastKey, err := yamlNavigate(m, segments)
	if err != nil {
		return err
	}

	v, err := parseYAMLValue(value)
	if err != nil {
		return err
	}

	parent[lastKey] = v
	return nil
}

func yamlDelete(m map[string]any, segments []string) error {
	parent, lastKey, err := yamlNavigate(m, segments)
	if err != nil {
		return err
	}

	if _, ok := parent[lastKey]; !ok {
		return fmt.Errorf("key not found: %s", lastKey)
	}
	delete(parent, lastKey)
	return nil
}
