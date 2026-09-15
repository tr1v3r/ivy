package driver_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/tr1v3r/ivy/driver"
)

// Table-driven error-path coverage for TOMLProcessor.Process: every case
// exercises a navigate/parse branch that the happy-path suite never hits.
func TestTOMLProcessor_ProcessErrors(t *testing.T) {
	const validBefore = "server = {host = \"localhost\", port = 8080}\n"

	tests := []struct {
		name    string
		op      *driver.TOMLProcessor
		before  string
		wantErr string
	}{
		{
			name:    "invalid toml before",
			op:      &driver.TOMLProcessor{T: "create", TOMLPath: "a", V: []byte(`1`)},
			before:  "not [valid toml",
			wantErr: "unmarshal toml fail",
		},
		{
			name:    "empty path on create",
			op:      &driver.TOMLProcessor{T: "create", TOMLPath: "", V: []byte(`1`)},
			wantErr: "empty toml path",
		},
		{
			name:    "empty path on delete",
			op:      &driver.TOMLProcessor{T: "delete", TOMLPath: "..."},
			wantErr: "empty toml path",
		},
		{
			name:    "replace missing intermediate key",
			op:      &driver.TOMLProcessor{T: "replace", TOMLPath: "missing.key", V: []byte(`1`)},
			before:  validBefore,
			wantErr: "key not found: missing",
		},
		{
			name:    "replace through non-table value",
			op:      &driver.TOMLProcessor{T: "replace", TOMLPath: "server.host.nested", V: []byte(`1`)},
			before:  validBefore,
			wantErr: "key host is not a table",
		},
		{
			name:    "create through non-table value",
			op:      &driver.TOMLProcessor{T: "create", TOMLPath: "server.host.nested", V: []byte(`1`)},
			before:  validBefore,
			wantErr: "key host is not a table",
		},
		{
			name:    "delete missing final key",
			op:      &driver.TOMLProcessor{T: "delete", TOMLPath: "server.absent"},
			before:  validBefore,
			wantErr: "key not found: absent",
		},
		{
			name:    "set through non-table value",
			op:      &driver.TOMLProcessor{T: "set", TOMLPath: "server.host.nested", V: []byte(`1`)},
			before:  validBefore,
			wantErr: "key host is not a table",
		},
		{
			name:    "set with invalid toml value",
			op:      &driver.TOMLProcessor{T: "set", TOMLPath: "server.port", V: []byte(`[unclosed`)},
			before:  validBefore,
			wantErr: "parse toml value fail",
		},
		{
			name:    "create with invalid toml value",
			op:      &driver.TOMLProcessor{T: "create", TOMLPath: "a", V: []byte(`= nope`)},
			wantErr: "parse toml value fail",
		},
		{
			name:    "replace with invalid toml value",
			op:      &driver.TOMLProcessor{T: "replace", TOMLPath: "server.port", V: []byte(`bad value`)},
			before:  validBefore,
			wantErr: "parse toml value fail",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := tt.op.Process(nil, []byte(tt.before))
			if err == nil {
				t.Fatalf("Process() error = nil, want containing %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("Process() error = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestTOMLProcessor_ValueShapes(t *testing.T) {
	t.Run("empty value creates key with nil and marshals without error", func(t *testing.T) {
		op := &driver.TOMLProcessor{T: "create", TOMLPath: "flag", V: nil}
		out, err := op.Process(nil, nil)
		if err != nil {
			t.Fatalf("Process() error = %v, want nil", err)
		}
		if strings.Contains(string(out), "flag") {
			t.Errorf("nil value should not serialize a key, got %q", out)
		}
	})

	t.Run("append list to existing list concatenates", func(t *testing.T) {
		before := "tags = [\"a\"]\n"
		op := &driver.TOMLProcessor{T: "append", TOMLPath: "tags", V: []byte(`["b", "c"]`)}
		out, err := op.Process(nil, []byte(before))
		if err != nil {
			t.Fatalf("Process() error = %v, want nil", err)
		}
		for _, want := range []string{`'a'`, `'b'`, `'c'`} {
			if !strings.Contains(string(out), want) {
				t.Errorf("output %q missing %s", out, want)
			}
		}
	})

	t.Run("create over existing scalar overwrites", func(t *testing.T) {
		before := "port = 8080\n"
		op := &driver.TOMLProcessor{T: "create", TOMLPath: "port", V: []byte(`9090`)}
		out, err := op.Process(nil, []byte(before))
		if err != nil {
			t.Fatalf("Process() error = %v, want nil", err)
		}
		if !strings.Contains(string(out), "9090") || strings.Contains(string(out), "8080") {
			t.Errorf("scalar create should overwrite, got %q", out)
		}
	})
}

// GeneralModem error paths reachable without crashing: Load failures,
// invalid outer JSON, and invalid type-parameter shapes (interface T and
// value-type T used to panic inside checkType/instantiation — see #55
// and #35; both now return explicit errors).
func TestGeneralModem_Errors(t *testing.T) {
	t.Run("item failing Load aborts Unmarshal", func(t *testing.T) {
		m := &driver.GeneralModem[*driver.TOMLProcessor]{
			Marshaler:   json.Marshal,
			Unmarshaler: json.Unmarshal,
		}
		if _, err := m.Unmarshal([]byte(`[123]`)); err == nil {
			t.Fatal("Unmarshal() error = nil, want load failure")
		} else if !strings.Contains(err.Error(), "load Processor fail") {
			t.Errorf("Unmarshal() error = %v, want load failure", err)
		}
	})

	t.Run("invalid outer json fails unmarshal", func(t *testing.T) {
		m := &driver.GeneralModem[*driver.TOMLProcessor]{
			Marshaler:   json.Marshal,
			Unmarshaler: json.Unmarshal,
		}
		if _, err := m.Unmarshal([]byte(`not-json`)); err == nil {
			t.Fatal("Unmarshal() error = nil, want unmarshal failure")
		}
	})

	// Regression #55: GeneralModem instantiated with an interface T
	// (DummyModem is GeneralModem[Processor]) panicked with a nil
	// dereference in checkType — reflect.TypeOf of a nil interface
	// returns nil, and typ.Kind() dereferenced it.
	t.Run("interface type T returns error, not panic", func(t *testing.T) {
		for name, data := range map[string][]byte{
			"empty array":     []byte(`[]`),
			"type mismatch":   []byte(`[123]`),
			"garbage payload": []byte(`[{}]`),
		} {
			if _, err := driver.DummyModem.Unmarshal(data); err == nil {
				t.Errorf("%s: DummyModem.Unmarshal() error = nil, want invalid-type-T error", name)
			} else if !strings.Contains(err.Error(), "not an interface") {
				t.Errorf("%s: DummyModem.Unmarshal() error = %v, want 'not an interface' diagnostic", name, err)
			}
		}
	})

	// Regression #35 (same root as #55): a value-type T passed checkType
	// but the instantiation assert reflect.New(typ).Interface().(T)
	// panicked with an interface-conversion failure.
	t.Run("value type T returns error, not panic", func(t *testing.T) {
		m := &driver.GeneralModem[valueProc]{
			Marshaler:   json.Marshal,
			Unmarshaler: json.Unmarshal,
		}
		if _, err := m.Unmarshal([]byte(`[{}]`)); err == nil {
			t.Fatal("Unmarshal() error = nil, want invalid-type-T error")
		} else if !strings.Contains(err.Error(), "must be a pointer type") {
			t.Errorf("Unmarshal() error = %v, want pointer-type diagnostic", err)
		}
	})
}

// valueProc implements driver.Processor with value receivers only, to
// exercise GeneralModem's rejection of value-type type parameters.
type valueProc struct{}

func (valueProc) Path() string { return "" }
func (valueProc) Type() string { return "value" }
func (valueProc) Process(_ *driver.RealizeContext, b []byte) ([]byte, error) {
	return b, nil
}
func (valueProc) Author() string       { return "test" }
func (valueProc) CreatedAt() time.Time { return time.Time{} }
func (valueProc) Load([]byte) error    { return nil }
func (valueProc) Save() []byte         { return nil }
