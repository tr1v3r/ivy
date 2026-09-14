package driver_test

import (
	"strings"
	"testing"

	"github.com/tr1v3r/ivy/driver"
)

// Regression test for audit finding H3: a "set" processor with an empty
// value used to splice nothing into the document via SetRawBytes and
// return invalid JSON such as {"a":1,"family":} with a nil error.
func TestJSONProcessorSetEmptyValueFails(t *testing.T) {
	for name, value := range map[string][]byte{"nil value": nil, "empty value": {}} {
		out, err := (&driver.JSONProcessor{T: "set", JSONPath: "family", V: value}).Process(nil, []byte(`{"a":1}`))
		if err == nil {
			t.Fatalf("%s: expected an error, got output %s", name, out)
		}
		if !strings.Contains(err.Error(), "empty value") {
			t.Errorf("%s: error should mention the empty value, got: %v", name, err)
		}
		if out != nil {
			t.Errorf("%s: expected nil output on error, got %s", name, out)
		}
	}
}

// Valid raw values keep the documented set semantics: embedded verbatim.
func TestJSONProcessorSetRawValueStillEmbeds(t *testing.T) {
	out, err := (&driver.JSONProcessor{T: "set", JSONPath: "family", V: []byte(`["mom","dad"]`)}).Process(nil, []byte(`{}`))
	if err != nil {
		t.Fatalf("set with raw array: %v", err)
	}
	if got, want := string(out), `{"family":["mom","dad"]}`; got != want {
		t.Errorf("set raw array: got %s, want %s", got, want)
	}

	out, err = (&driver.JSONProcessor{T: "set", JSONPath: "enabled", V: []byte(`true`)}).Process(nil, []byte(`{}`))
	if err != nil {
		t.Fatalf("set with raw bool: %v", err)
	}
	if got, want := string(out), `{"enabled":true}`; got != want {
		t.Errorf("set raw bool: got %s, want %s", got, want)
	}
}
