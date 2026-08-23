package driver_test

import (
	"strings"
	"testing"

	"github.com/tr1v3r/ivy/driver"
)

func TestTOMLDriver(t *testing.T) {
	d := driver.NewTOMLDriver()

	data, err := d.Marshal([]driver.Processor{
		&driver.TOMLProcessor{T: "create", TOMLPath: "server.host", V: []byte(`"localhost"`)},
		&driver.TOMLProcessor{T: "create", TOMLPath: "server.port", V: []byte("8080")},
		&driver.TOMLProcessor{T: "set", TOMLPath: "server.port", V: []byte("9090")},
		&driver.TOMLProcessor{T: "create", TOMLPath: "server.tags", V: []byte(`["a", "b"]`)},
		&driver.TOMLProcessor{T: "append", TOMLPath: "server.tags", V: []byte(`"c"`)},
		&driver.TOMLProcessor{T: "delete", TOMLPath: "server.host"},
		&driver.TOMLProcessor{T: "replace", TOMLPath: "server.tags", V: []byte(`["x"]`)},
	}...)
	if err != nil {
		t.Errorf("marshal fail: %s", err)
		return
	}

	ops, err := d.Unmarshal(data)
	if err != nil {
		t.Errorf("unmarshal fail: %s", err)
		return
	}

	var rule []byte
	for _, op := range ops {
		rule, err = op.Process(nil, rule)
		if err != nil {
			t.Errorf("Process fail: %s", err)
			return
		}
	}
	t.Logf("got result:\n%s", rule)

	result := string(rule)
	if strings.Contains(result, "localhost") {
		t.Errorf("expected host to be deleted, got: %s", result)
	}
	if !strings.Contains(result, "9090") {
		t.Errorf("expected port 9090, got: %s", result)
	}
	if !strings.Contains(result, "x") {
		t.Errorf("expected tag x after replace, got: %s", result)
	}
}

func TestTOMLProcessor_Create(t *testing.T) {
	op := &driver.TOMLProcessor{T: "create", TOMLPath: "server.host", V: []byte(`"localhost"`)}
	result, err := op.Process(nil, nil)
	if err != nil {
		t.Errorf("Process fail: %s", err)
		return
	}
	s := string(result)
	if !strings.Contains(s, "host =") || !strings.Contains(s, "localhost") {
		t.Errorf("expected host with localhost, got: %s", s)
	}
}

func TestTOMLProcessor_Set(t *testing.T) {
	before := []byte(`host = "old"`)
	op := &driver.TOMLProcessor{T: "set", TOMLPath: "host", V: []byte(`"new"`)}
	result, err := op.Process(nil, before)
	if err != nil {
		t.Errorf("Process fail: %s", err)
		return
	}
	s := string(result)
	if !strings.Contains(s, "host =") || !strings.Contains(s, "new") {
		t.Errorf("expected host with new, got: %s", s)
	}
}

func TestTOMLProcessor_Delete(t *testing.T) {
	before := []byte(`a = 1
b = 2`)
	op := &driver.TOMLProcessor{T: "delete", TOMLPath: "a"}
	result, err := op.Process(nil, before)
	if err != nil {
		t.Errorf("Process fail: %s", err)
		return
	}
	s := string(result)
	if strings.Contains(s, "a =") {
		t.Errorf("expected a to be deleted, got: %s", s)
	}
	if !strings.Contains(s, "b = 2") {
		t.Errorf("expected b = 2 to remain, got: %s", s)
	}
}

func TestTOMLProcessor_Replace(t *testing.T) {
	before := []byte(`items = ["old"]`)
	op := &driver.TOMLProcessor{T: "replace", TOMLPath: "items", V: []byte(`["new1", "new2"]`)}
	result, err := op.Process(nil, before)
	if err != nil {
		t.Errorf("Process fail: %s", err)
		return
	}
	s := string(result)
	if strings.Contains(s, "old") {
		t.Errorf("expected 'old' to be replaced, got: %s", s)
	}
	if !strings.Contains(s, "new1") {
		t.Errorf("expected new1 in result, got: %s", s)
	}
}

func TestTOMLProcessor_EmptyBefore(t *testing.T) {
	op := &driver.TOMLProcessor{T: "create", TOMLPath: "hello", V: []byte(`"world"`)}
	result, err := op.Process(nil, nil)
	if err != nil {
		t.Errorf("Process fail: %s", err)
		return
	}
	s := string(result)
	if !strings.Contains(s, "hello =") || !strings.Contains(s, "world") {
		t.Errorf("expected hello with world, got: %s", s)
	}
}

func TestTOMLProcessor_UnknownType(t *testing.T) {
	op := &driver.TOMLProcessor{T: "invalid", TOMLPath: "a"}
	_, err := op.Process(nil, nil)
	if err == nil {
		t.Error("expected error for unknown type")
		return
	}
	if !strings.Contains(err.Error(), "unknown Processor type: invalid") {
		t.Errorf("unexpected error: %s", err)
	}
}

func TestTOMLProcessor_MarshalUnmarshal(t *testing.T) {
	d := driver.NewTOMLDriver()

	original := &driver.TOMLProcessor{
		T:        "create",
		TOMLPath: "server.host",
		V:        []byte(`"localhost"`),
		A:        "tester",
	}

	data := original.Save()

	var ops []driver.Processor
	ops, err := d.Unmarshal([]byte("[" + string(data) + "]"))
	if err != nil {
		t.Errorf("unmarshal fail: %s", err)
		return
	}
	if len(ops) != 1 {
		t.Errorf("expected 1 processor, got %d", len(ops))
		return
	}
	restored := ops[0].(*driver.TOMLProcessor)
	if restored.T != original.T {
		t.Errorf("expected type %s, got %s", original.T, restored.T)
	}
	if restored.TOMLPath != original.TOMLPath {
		t.Errorf("expected toml_path %s, got %s", original.TOMLPath, restored.TOMLPath)
	}
	if string(restored.V) != string(original.V) {
		t.Errorf("expected value %s, got %s", original.V, restored.V)
	}
}
