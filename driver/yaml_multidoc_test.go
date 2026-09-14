package driver_test

import (
	"strings"
	"testing"

	"github.com/tr1v3r/ivy/driver"
)

// Regression test for audit finding H2: yaml.Unmarshal decodes only the
// first document of a multi-document stream, so any YAMLProcessor silently
// dropped documents 2..N and returned success.
func TestYAMLProcessorRejectsMultiDocument(t *testing.T) {
	for name, input := range map[string]string{
		"two mapping documents": "a: 1\n---\nb: 2\n",
		"three documents":       "a: 1\n---\nb: 2\n---\nc: 3\n",
		"mapping then scalar":   "a: 1\n---\nplain\n",
	} {
		out, err := (&driver.YAMLProcessor{T: "set", YAMLPath: "x", V: []byte("1")}).Process(nil, []byte(input))
		if err == nil {
			t.Fatalf("%s: expected an error, got output %s", name, out)
		}
		if !strings.Contains(err.Error(), "multi-document") {
			t.Errorf("%s: error should mention multi-document content, got: %v", name, err)
		}
		if out != nil {
			t.Errorf("%s: expected nil output on error, got %s", name, out)
		}
	}
}

// Single-document and document-less content keeps working.
func TestYAMLProcessorSingleDocumentUnaffected(t *testing.T) {
	out, err := (&driver.YAMLProcessor{T: "set", YAMLPath: "x", V: []byte("1")}).Process(nil, []byte("a: 1\n"))
	if err != nil {
		t.Fatalf("single document: %v", err)
	}
	if !strings.Contains(string(out), "a: 1") || !strings.Contains(string(out), "x: 1") {
		t.Errorf("single document: unexpected output %s", out)
	}

	// comment-only stream decodes as an empty document
	out, err = (&driver.YAMLProcessor{T: "set", YAMLPath: "x", V: []byte("1")}).Process(nil, []byte("# only a comment\n"))
	if err != nil {
		t.Fatalf("comment-only stream: %v", err)
	}
	if got, want := string(out), "x: 1\n"; got != want {
		t.Errorf("comment-only stream: got %q, want %q", got, want)
	}

	// empty content is unaffected
	out, err = (&driver.YAMLProcessor{T: "set", YAMLPath: "x", V: []byte("1")}).Process(nil, nil)
	if err != nil || string(out) != "x: 1\n" {
		t.Errorf("empty content: out=%q err=%v", out, err)
	}
}
