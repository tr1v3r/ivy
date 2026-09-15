package driver_test

import (
	"strings"
	"testing"

	"github.com/tr1v3r/ivy/driver"
)

// Regression #57: sjson silently rebuilt non-JSON bases (plain text,
// YAML, HTML — anything an earlier processor such as curl can return)
// into {"k":"v"} instead of applying the operation, destroying the
// original content with no error. The mutating types must reject such
// bases with an explicit error.
func TestJSONProcessorNonJSONBaseRejected(t *testing.T) {
	bases := map[string][]byte{
		"plain text": []byte(`hello world`),
		"yaml":       []byte("a: 1\nb: 2\n"),
		"html":       []byte("<html><body>hi</body></html>"),
	}
	for name, base := range bases {
		for _, typ := range []string{"create", "append", "replace", "set"} {
			op := &driver.JSONProcessor{T: typ, JSONPath: "k", V: []byte(`"v"`)}
			out, err := op.Process(nil, base)
			if err == nil {
				t.Errorf("%s/%s: Process() error = nil, want non-JSON-base error (silently rebuilt to %q)", name, typ, out)
			} else if !strings.Contains(err.Error(), "not valid JSON") {
				t.Errorf("%s/%s: Process() error = %v, want not-valid-JSON diagnostic", name, typ, err)
			}
		}
	}
}

// Legitimate bases keep their exact previous behavior: fresh-document
// initialization on empty bases, normal merge on valid JSON, and
// delete's non-corrupting passthrough on non-JSON content.
func TestJSONProcessorValidBaseUnchanged(t *testing.T) {
	t.Run("empty and whitespace bases initialize a fresh document", func(t *testing.T) {
		for name, base := range map[string][]byte{"empty": nil, "whitespace": []byte("  \n ")} {
			op := &driver.JSONProcessor{T: "create", JSONPath: "k", V: []byte(`v`)}
			out, err := op.Process(nil, base)
			if err != nil || string(out) != `{"k":"v"}` {
				t.Errorf("%s: Process() = %q, %v; want %q", name, out, err, `{"k":"v"}`)
			}
		}
	})

	t.Run("valid object base merges for every mutating type", func(t *testing.T) {
		// SetBytes takes V as a plain value; SetRawBytes embeds V as raw
		// JSON — both end up as {"a":1,"k":"v"} here.
		for _, item := range []struct {
			typ string
			v   []byte
		}{
			{"create", []byte(`v`)},
			{"append", []byte(`v`)},
			{"replace", []byte(`v`)},
			{"set", []byte(`"v"`)},
		} {
			op := &driver.JSONProcessor{T: item.typ, JSONPath: "k", V: item.v}
			out, err := op.Process(nil, []byte(`{"a":1}`))
			if err != nil || string(out) != `{"a":1,"k":"v"}` {
				t.Errorf("%s: Process() = %q, %v; want %q", item.typ, out, err, `{"a":1,"k":"v"}`)
			}
		}
	})

	t.Run("delete on a non-JSON base passes content through untouched", func(t *testing.T) {
		out, err := (&driver.JSONProcessor{T: "delete", JSONPath: "k"}).Process(nil, []byte(`hello world`))
		if err != nil || string(out) != `hello world` {
			t.Errorf("delete passthrough: Process() = %q, %v; want hello world, nil", out, err)
		}
	})
}
