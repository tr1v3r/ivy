package driver_test

import (
	"strings"
	"testing"

	"github.com/tr1v3r/ivy/driver"
)

// Table-driven error-path coverage for YAMLProcessor.Process.
func TestYAMLProcessor_ProcessErrors(t *testing.T) {
	const validBefore = "server:\n  host: localhost\n  port: 8080\n"

	tests := []struct {
		name    string
		op      *driver.YAMLProcessor
		before  string
		wantErr string
	}{
		{
			name:    "empty path on create",
			op:      &driver.YAMLProcessor{T: "create", YAMLPath: "", V: []byte(`x`)},
			wantErr: "empty yaml path",
		},
		{
			name:    "empty path on delete",
			op:      &driver.YAMLProcessor{T: "delete", YAMLPath: "."},
			wantErr: "empty yaml path",
		},
		{
			name:    "replace missing intermediate key",
			op:      &driver.YAMLProcessor{T: "replace", YAMLPath: "missing.key", V: []byte(`1`)},
			before:  validBefore,
			wantErr: "key not found: missing",
		},
		{
			name:    "replace through non-map value",
			op:      &driver.YAMLProcessor{T: "replace", YAMLPath: "server.host.nested", V: []byte(`1`)},
			before:  validBefore,
			wantErr: "key host is not a map",
		},
		{
			name:    "create through non-map value",
			op:      &driver.YAMLProcessor{T: "create", YAMLPath: "server.host.nested", V: []byte(`1`)},
			before:  validBefore,
			wantErr: "key host is not a map",
		},
		{
			name:    "delete missing final key",
			op:      &driver.YAMLProcessor{T: "delete", YAMLPath: "server.absent"},
			before:  validBefore,
			wantErr: "key not found: absent",
		},
		{
			name:    "set with invalid yaml value",
			op:      &driver.YAMLProcessor{T: "set", YAMLPath: "server.port", V: []byte(`"unterminated`)},
			before:  validBefore,
			wantErr: "parse yaml value fail",
		},
		{
			name:    "create with invalid yaml value",
			op:      &driver.YAMLProcessor{T: "create", YAMLPath: "a", V: []byte(`"unterminated`)},
			wantErr: "parse yaml value fail",
		},
		{
			name:    "replace with invalid yaml value",
			op:      &driver.YAMLProcessor{T: "replace", YAMLPath: "server.port", V: []byte(`"unterminated`)},
			before:  validBefore,
			wantErr: "parse yaml value fail",
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

func TestYAMLProcessor_EmptyValue(t *testing.T) {
	// empty value parses to nil; the key is created and marshals as null
	op := &driver.YAMLProcessor{T: "create", YAMLPath: "flag", V: nil}
	out, err := op.Process(nil, nil)
	if err != nil {
		t.Fatalf("Process() error = %v, want nil", err)
	}
	if !strings.Contains(string(out), "flag: null") {
		t.Errorf("expected flag: null in output, got %q", out)
	}
}
