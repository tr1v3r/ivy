package driver_test

import (
	"encoding/xml"
	"strings"
	"testing"

	"github.com/tr1v3r/ivy/driver"
)

// Regression tests for audit finding H1: re-encoding through
// encoding/xml re-declared namespaces — default-namespace elements came
// back with a duplicate xmlns attribute and prefixed declarations came
// back as synthesized _xmlns attributes.

func roundTripXML(t *testing.T, input, opPath, opValue string) string {
	t.Helper()
	var op *driver.XMLProcessor
	if opPath == "" {
		// no-op round trip: create an invisible sibling then strip it is not
		// possible, so exercise create which appends a marker element
		op = &driver.XMLProcessor{T: "create", XMLPath: "extra", V: []byte("e")}
	} else {
		op = &driver.XMLProcessor{T: "set", XMLPath: opPath, V: []byte(opValue)}
	}
	out, err := op.Process(nil, []byte(input))
	if err != nil {
		t.Fatalf("process fail: %v", err)
	}
	return string(out)
}

func TestXMLNamespacePrefixedRoundtrip(t *testing.T) {
	input := `<ns:root xmlns:ns="http://x"><ns:kid>1</ns:kid></ns:root>`
	out := roundTripXML(t, input, "root", "v")

	want := xml.Header + `<ns:root xmlns:ns="http://x">v<ns:kid>1</ns:kid></ns:root>`
	if out != want {
		t.Fatalf("prefixed ns round-trip:\n got %s\nwant %s", out, want)
	}
	if strings.Contains(out, "_xmlns") {
		t.Errorf("synthesized _xmlns attribute leaked into output: %s", out)
	}
	if got := strings.Count(out, `xmlns:ns=`); got != 1 {
		t.Errorf("expected exactly one xmlns:ns declaration, got %d in %s", got, out)
	}
}

func TestXMLNamespaceDefaultRoundtrip(t *testing.T) {
	input := `<a xmlns="http://d"><b x="1"/></a>`
	out := roundTripXML(t, input, "a", "V")

	want := xml.Header + `<a xmlns="http://d">V<b x="1"></b></a>`
	if out != want {
		t.Fatalf("default ns round-trip:\n got %s\nwant %s", out, want)
	}
	if got := strings.Count(out, `xmlns=`); got != 1 {
		t.Errorf("default ns declared %d times (duplicate xmlns means ill-formed XML): %s", got, out)
	}
}

func TestXMLNamespacePrefixedAttribute(t *testing.T) {
	input := `<a xmlns:l="http://l" l:href="u&amp;v">t</a>`
	out := roundTripXML(t, input, "a", "V")

	want := xml.Header + `<a xmlns:l="http://l" l:href="u&amp;v">V</a>`
	if out != want {
		t.Fatalf("prefixed attribute round-trip:\n got %s\nwant %s", out, want)
	}
}

func TestXMLNamespaceShadowedPrefix(t *testing.T) {
	input := `<a xmlns:n="http://1"><b xmlns:n="http://2"><n:c>x</n:c></b></a>`
	out := roundTripXML(t, input, "a", "V")

	want := xml.Header + `<a xmlns:n="http://1">V<b xmlns:n="http://2"><n:c>x</n:c></b></a>`
	if out != want {
		t.Fatalf("shadowed prefix round-trip:\n got %s\nwant %s", out, want)
	}
}

func TestXMLNamespaceRoundtripReParses(t *testing.T) {
	for _, input := range []string{
		`<ns:root xmlns:ns="http://x"><ns:kid>1</ns:kid></ns:root>`,
		`<a xmlns="http://d"><b x="1"/></a>`,
		`<a xmlns:l="http://l" l:href="u">t</a>`,
	} {
		out := roundTripXML(t, input, "", "")
		var probe any
		if err := xml.Unmarshal([]byte(out), &probe); err != nil {
			t.Errorf("round-trip output does not re-parse for %s: %v\noutput: %s", input, err, out)
		}
	}
}

// Namespace-blind documents (the common case) keep the existing output shape.
func TestXMLNoNamespaceUnaffected(t *testing.T) {
	out, err := (&driver.XMLProcessor{T: "set", XMLPath: "root/name", V: []byte("new")}).Process(nil,
		[]byte(`<root><name>old</name></root>`))
	if err != nil {
		t.Fatalf("process fail: %v", err)
	}
	if want := xml.Header + `<root><name>new</name></root>`; string(out) != want {
		t.Fatalf("plain document:\n got %s\nwant %s", out, want)
	}
}
