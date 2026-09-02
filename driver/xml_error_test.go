package driver_test

import (
	"strings"
	"testing"

	"github.com/tr1v3r/ivy/driver"
)

// Table-driven error-path and shape coverage for XMLProcessor.Process.
func TestXMLProcessor_ProcessErrors(t *testing.T) {
	const validBefore = `<root><name><first>river</first><last>chu</last></name></root>`

	tests := []struct {
		name    string
		op      *driver.XMLProcessor
		before  string
		wantErr string
	}{
		{
			name:    "invalid xml before",
			op:      &driver.XMLProcessor{T: "set", XMLPath: "root/a", V: []byte("x")},
			before:  "<root><unclosed></root>",
			wantErr: "parse xml fail",
		},
		{
			name:    "unknown type",
			op:      &driver.XMLProcessor{T: "merge", XMLPath: "root"},
			before:  validBefore,
			wantErr: "unknown Processor type: merge",
		},
		{
			name:    "empty path on create",
			op:      &driver.XMLProcessor{T: "create", XMLPath: "/", V: []byte("x")},
			wantErr: "empty xml path",
		},
		{
			name:    "empty path on set",
			op:      &driver.XMLProcessor{T: "set", XMLPath: ""},
			wantErr: "empty xml path",
		},
		{
			name:    "empty path on delete",
			op:      &driver.XMLProcessor{T: "delete", XMLPath: "//"},
			wantErr: "empty xml path",
		},
		{
			name:    "replace nonexistent element",
			op:      &driver.XMLProcessor{T: "replace", XMLPath: "root/missing", V: []byte("x")},
			before:  validBefore,
			wantErr: "element not found: missing",
		},
		{
			name:    "set navigates through missing parents via navigate",
			op:      &driver.XMLProcessor{T: "set", XMLPath: "missing/leaf", V: []byte("x")},
			before:  validBefore,
			wantErr: "",
		},
		{
			name:    "delete with missing parent",
			op:      &driver.XMLProcessor{T: "delete", XMLPath: "missing/leaf"},
			before:  validBefore,
			wantErr: "element not found: missing",
		},
		{
			name:    "delete leaf that does not exist under existing parent",
			op:      &driver.XMLProcessor{T: "delete", XMLPath: "root/absent"},
			before:  validBefore,
			wantErr: "element not found: absent",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := tt.op.Process(nil, []byte(tt.before))
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("Process() error = %v, want nil", err)
				}
				t.Logf("output: %s", out)
				return
			}
			if err == nil {
				t.Fatalf("Process() error = nil, want containing %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("Process() error = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestXMLProcessor_Shapes(t *testing.T) {
	t.Run("mixed text and children merge chardata", func(t *testing.T) {
		// two chardata runs around a child element join with a space;
		// the extra create op leaves node root/a untouched
		out, err := (&driver.XMLProcessor{T: "create", XMLPath: "root/probe", V: nil}).
			Process(nil, []byte("<root><a>one<b/>two</a></root>"))
		if err != nil {
			t.Fatalf("Process() error = %v, want nil", err)
		}
		if !strings.Contains(string(out), "one two") {
			t.Errorf("chardata runs should join with space, got %q", out)
		}
	})

	t.Run("replace with plain text falls back to text content", func(t *testing.T) {
		before := `<root><items><item>a</item><item>b</item></items></root>`
		out, err := (&driver.XMLProcessor{T: "replace", XMLPath: "root/items", V: []byte("plain text")}).
			Process(nil, []byte(before))
		if err != nil {
			t.Fatalf("Process() error = %v, want nil", err)
		}
		if !strings.Contains(string(out), "plain text") {
			t.Errorf("replaced text missing in %q", out)
		}
		if strings.Contains(string(out), "<item>") {
			t.Errorf("children should be cleared on replace, got %q", out)
		}
	})

	t.Run("replace with empty value clears node", func(t *testing.T) {
		before := `<root><a>text</a></root>`
		out, err := (&driver.XMLProcessor{T: "replace", XMLPath: "root/a", V: nil}).
			Process(nil, []byte(before))
		if err != nil {
			t.Fatalf("Process() error = %v, want nil", err)
		}
		if strings.Contains(string(out), "text") {
			t.Errorf("replace with empty value should clear content, got %q", out)
		}
	})

	t.Run("create with non-xml value uses it as text", func(t *testing.T) {
		out, err := (&driver.XMLProcessor{T: "create", XMLPath: "root/note", V: []byte("just text")}).
			Process(nil, []byte(`<root/>`))
		if err != nil {
			t.Fatalf("Process() error = %v, want nil", err)
		}
		if !strings.Contains(string(out), "just text") {
			t.Errorf("text content missing in %q", out)
		}
	})

	t.Run("delete root-level element", func(t *testing.T) {
		before := `<root><a>1</a><b>2</b></root>`
		out, err := (&driver.XMLProcessor{T: "delete", XMLPath: "root/a"}).
			Process(nil, []byte(before))
		if err != nil {
			t.Fatalf("Process() error = %v, want nil", err)
		}
		if strings.Contains(string(out), "<a>") {
			t.Errorf("element a should be deleted, got %q", out)
		}
		if !strings.Contains(string(out), "<b>") {
			t.Errorf("element b should survive, got %q", out)
		}
	})
}
