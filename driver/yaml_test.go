package driver_test

import (
	"strings"
	"testing"

	"github.com/tr1v3r/ivy"
	"github.com/tr1v3r/ivy/driver"
)

func TestYAMLDriver(t *testing.T) {
	d := driver.NewYAMLDriver()

	data, err := d.Marshal([]driver.Processor{
		&driver.YAMLProcessor{T: "create", YAMLPath: "server.host", V: []byte(`localhost`)},
		&driver.YAMLProcessor{T: "create", YAMLPath: "server.port", V: []byte(`8080`)},
		&driver.YAMLProcessor{T: "set", YAMLPath: "server.port", V: []byte(`9090`)},
		&driver.YAMLProcessor{T: "create", YAMLPath: "server.tags", V: []byte("- a\n- b\n")},
		&driver.YAMLProcessor{T: "append", YAMLPath: "server.tags", V: []byte(`c`)},
		&driver.YAMLProcessor{T: "delete", YAMLPath: "server.host"},
		&driver.YAMLProcessor{T: "replace", YAMLPath: "server.tags", V: []byte("- x\n")},
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
	if strings.Contains(result, "- a") {
		t.Errorf("expected tags replaced, got: %s", result)
	}
}

func TestYAMLProcessor_Create(t *testing.T) {
	op := &driver.YAMLProcessor{T: "create", YAMLPath: "server.host", V: []byte(`localhost`)}
	result, err := op.Process(nil, nil)
	if err != nil {
		t.Errorf("Process fail: %s", err)
		return
	}
	if !strings.Contains(string(result), "host: localhost") {
		t.Errorf("expected host: localhost, got: %s", result)
	}

	// create on existing scalar overwrites it
	result, err = op.Process(nil, []byte("server:\n  host: old\n"))
	if err != nil {
		t.Errorf("Process fail: %s", err)
		return
	}
	if !strings.Contains(string(result), "host: localhost") {
		t.Errorf("expected host replaced, got: %s", result)
	}
}

func TestYAMLProcessor_CreateNested(t *testing.T) {
	op := &driver.YAMLProcessor{T: "create", YAMLPath: "a.b.c", V: []byte(`deep`)}
	result, err := op.Process(nil, nil)
	if err != nil {
		t.Errorf("Process fail: %s", err)
		return
	}
	for _, want := range []string{"a:", "b:", "c: deep"} {
		if !strings.Contains(string(result), want) {
			t.Errorf("expected %q in nested result, got: %s", want, result)
		}
	}
}

func TestYAMLProcessor_Append(t *testing.T) {
	before := []byte("tags:\n  - a\n  - b\n")

	// append single value
	op := &driver.YAMLProcessor{T: "append", YAMLPath: "tags", V: []byte(`c`)}
	result, err := op.Process(nil, before)
	if err != nil {
		t.Errorf("Process fail: %s", err)
		return
	}
	if !strings.Contains(string(result), "- c") {
		t.Errorf("expected - c appended, got: %s", result)
	}

	// append a list of values
	op = &driver.YAMLProcessor{T: "append", YAMLPath: "tags", V: []byte("- d\n- e\n")}
	result, err = op.Process(nil, before)
	if err != nil {
		t.Errorf("Process fail: %s", err)
		return
	}
	if !strings.Contains(string(result), "- d") || !strings.Contains(string(result), "- e") {
		t.Errorf("expected - d and - e appended, got: %s", result)
	}
}

func TestYAMLProcessor_Set(t *testing.T) {
	before := []byte("server:\n  port: 8080\n")

	op := &driver.YAMLProcessor{T: "set", YAMLPath: "server.port", V: []byte(`9090`)}
	result, err := op.Process(nil, before)
	if err != nil {
		t.Errorf("Process fail: %s", err)
		return
	}
	if !strings.Contains(string(result), "port: 9090") {
		t.Errorf("expected port: 9090, got: %s", result)
	}
	if strings.Contains(string(result), "8080") {
		t.Errorf("expected old port gone, got: %s", result)
	}
}

func TestYAMLProcessor_SetOverwriteMap(t *testing.T) {
	before := []byte("a:\n  b: 1\n  c: 2\n")

	// set replaces the whole value at the path
	op := &driver.YAMLProcessor{T: "set", YAMLPath: "a", V: []byte("b: 9\n")}
	result, err := op.Process(nil, before)
	if err != nil {
		t.Errorf("Process fail: %s", err)
		return
	}
	if !strings.Contains(string(result), "b: 9") {
		t.Errorf("expected b: 9, got: %s", result)
	}
	if strings.Contains(string(result), "c: 2") {
		t.Errorf("expected map replaced not merged, got: %s", result)
	}
}

func TestYAMLProcessor_Replace(t *testing.T) {
	before := []byte("name: old\n")

	op := &driver.YAMLProcessor{T: "replace", YAMLPath: "name", V: []byte(`new`)}
	result, err := op.Process(nil, before)
	if err != nil {
		t.Errorf("Process fail: %s", err)
		return
	}
	if !strings.Contains(string(result), "name: new") {
		t.Errorf("expected name: new, got: %s", result)
	}

	// replace upserts when the final key is missing (intermediate maps must exist)
	op = &driver.YAMLProcessor{T: "replace", YAMLPath: "missing", V: []byte(`x`)}
	result, err = op.Process(nil, before)
	if err != nil {
		t.Errorf("Process fail: %s", err)
		return
	}
	if !strings.Contains(string(result), "missing: x") {
		t.Errorf("expected missing: x upserted, got: %s", result)
	}
}

func TestYAMLProcessor_Delete(t *testing.T) {
	before := []byte("a: 1\nb: 2\n")

	op := &driver.YAMLProcessor{T: "delete", YAMLPath: "b"}
	result, err := op.Process(nil, before)
	if err != nil {
		t.Errorf("Process fail: %s", err)
		return
	}
	if strings.Contains(string(result), "b: 2") {
		t.Errorf("expected b deleted, got: %s", result)
	}
	if !strings.Contains(string(result), "a: 1") {
		t.Errorf("expected a to remain, got: %s", result)
	}

	// delete requires the key to exist
	op = &driver.YAMLProcessor{T: "delete", YAMLPath: "missing"}
	if _, err := op.Process(nil, before); err == nil {
		t.Error("expected error deleting missing key")
	}
}

func TestYAMLProcessor_NavigateErrors(t *testing.T) {
	before := []byte("a: plain\n")

	// walking through a non-map key fails
	op := &driver.YAMLProcessor{T: "set", YAMLPath: "a.b", V: []byte(`x`)}
	if _, err := op.Process(nil, before); err == nil {
		t.Error("expected error walking through scalar")
	}

	// replace into missing intermediate map fails
	op = &driver.YAMLProcessor{T: "replace", YAMLPath: "x.y", V: []byte(`1`)}
	if _, err := op.Process(nil, before); err == nil {
		t.Error("expected error replacing under missing parent")
	}
}

func TestYAMLProcessor_EmptyBefore(t *testing.T) {
	// operations on empty content start from an empty document
	op := &driver.YAMLProcessor{T: "create", YAMLPath: "k", V: []byte(`v`)}
	result, err := op.Process(nil, nil)
	if err != nil {
		t.Errorf("Process fail: %s", err)
		return
	}
	if strings.TrimSpace(string(result)) != "k: v" {
		t.Errorf("expected 'k: v', got: %q", result)
	}
}

func TestYAMLProcessor_UnknownType(t *testing.T) {
	op := &driver.YAMLProcessor{T: "wat", YAMLPath: "a"}
	if _, err := op.Process(nil, []byte("a: 1\n")); err == nil {
		t.Error("expected error for unknown type")
	}
}

func TestYAMLProcessor_ValueTypes(t *testing.T) {
	var testcases = []struct {
		Name  string
		Value string
		Want  string
	}{
		{"string", `hello`, "k: hello"},
		{"number", `42`, "k: 42"},
		{"bool", `true`, "k: true"},
		{"list", "- a\n- b\n", "- a"},
		{"nested map", "n:\n  m: 1\n", "m: 1"},
	}
	for _, item := range testcases {
		t.Run(item.Name, func(t *testing.T) {
			op := &driver.YAMLProcessor{T: "create", YAMLPath: "k", V: []byte(item.Value)}
			result, err := op.Process(nil, nil)
			if err != nil {
				t.Fatalf("Process fail: %s", err)
			}
			if !strings.Contains(string(result), item.Want) {
				t.Errorf("expected %q in result, got: %s", item.Want, result)
			}
		})
	}
}

func TestYAMLProcessor_TreeIntegration(t *testing.T) {
	// the yaml driver end-to-end through a lazy tree with content inheritance
	tree, err := ivy.NewLazyYAMLTree("yaml_it", "root: base\n",
		ivy.NewDirective("/a/b", &driver.YAMLProcessor{T: "create", YAMLPath: "child", V: []byte(`true`)}),
	)
	if err != nil {
		t.Fatalf("build tree fail: %s", err)
	}

	val, err := tree.Get("/a/b")
	if err != nil {
		t.Fatalf("get fail: %s", err)
	}
	s := string(val)
	if !strings.Contains(s, "root: base") {
		t.Errorf("expected inherited root content, got: %s", s)
	}
	if !strings.Contains(s, "child: true") {
		t.Errorf("expected child key, got: %s", s)
	}
}
