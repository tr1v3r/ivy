package driver

import (
	"encoding/json"
	"time"
)

var _ Driver = (*YAMLDriver)(nil)

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

func (YAMLDriver) Name() string { return "yaml" }

var _ Processor = (*YAMLProcessor)(nil)

type YAMLProcessor struct {
	T string `json:"type"`
	V []byte `json:"value"`
}

func (op *YAMLProcessor) Type() string           { return op.T }
func (op *YAMLProcessor) Path() string           { return "" }
func (op *YAMLProcessor) Author() string         { return "" }
func (op *YAMLProcessor) CreatedAt() time.Time   { return time.Now() }
func (op *YAMLProcessor) Load(data []byte) error { return json.Unmarshal(data, op) }
func (op *YAMLProcessor) Save() []byte {
	data, _ := json.Marshal(op)
	return data
}
func (op *YAMLProcessor) Process(_ *RealizeContext, before []byte) (after []byte, err error) {
	// var result any
	// if err := yaml.Unmarshal([]byte(before), &result); err != nil {
	// 	return "", fmt.Errorf("unmarshal yaml fail: %w", err)
	// }
	if op.T == "append" {
		return append(before, op.T...), nil
	}
	return before, nil
}
