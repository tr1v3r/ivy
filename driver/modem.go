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
		return nil, fmt.Errorf("invalid type T %s: %w", reflect.TypeFor[T](), err)
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

// checkType validates that Unmarshal's reflect-based instantiation can
// work for T and returns the struct type instances are built from:
// T must be a pointer to a concrete type.
func (m *GeneralModem[T]) checkType() (reflect.Type, error) {
	var t T
	typ := reflect.TypeOf(t)

	switch {
	case typ == nil:
		// T is an interface type: its zero value carries no dynamic
		// type, so there is nothing to reflect.New an instance from.
		// (reflect.TypeOf of a nil interface returns nil — asking it
		// for a Kind would itself panic.)
		return nil, errors.New("type parameter T must be concrete, not an interface")
	case typ.Kind() != reflect.Pointer:
		// reflect.New always yields *T; asserting that back to a
		// value-type T can never succeed (interface conversion
		// panic), so reject the shape up front.
		return nil, errors.New("type parameter T must be a pointer type")
	}
	return typ.Elem(), nil
}
