package driver

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
)

var _ Modem = (*GeneralModem[Processor])(nil)

// GeneralModem json moden
type GeneralModem[T Processor] struct {
	Marshaler   func(in any) (out []byte, err error)
	Unmarshaler func(data []byte, v any) error
}

// Marshal serializes processors into their saved forms.
func (m *GeneralModem[T]) Marshal(ops ...Processor) ([]byte, error) {
	var buf = make([]json.RawMessage, 0, len(ops))
	for _, op := range ops {
		buf = append(buf, op.Save())
	}
	return m.Marshaler(buf)
}

// Unmarshal rebuilds processors of type T from serialized data.
func (m *GeneralModem[T]) Unmarshal(data []byte) ([]Processor, error) {
	var buf = make([]json.RawMessage, 0, 8)
	if err := m.Unmarshaler(data, &buf); err != nil {
		return nil, fmt.Errorf("unmarshal fail: %w", err)
	}

	typ, err := m.checkType()
	if err != nil {
		return nil, fmt.Errorf("invalid type T %s: %w", typ.Name(), err)
	}

	var ops = make([]Processor, 0, len(buf))
	for _, item := range buf {
		op := reflect.New(typ).Interface().(T) // create instance
		if err := op.Load(item); err != nil {
			return nil, fmt.Errorf("load Processor fail: %w", err)
		}
		ops = append(ops, op)
	}
	return ops, nil
}

func (m *GeneralModem[T]) checkType() (reflect.Type, error) {
	var t T
	typ := reflect.TypeOf(t)

	if typ.Kind() == reflect.Interface {
		return nil, errors.New("cannot be Interface")
	}
	if typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	return typ, nil
}
