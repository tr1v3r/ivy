package driver

import (
	"strings"
	"testing"
)

// TestParseSegmentsAgreesWithPerCallLookups pins the SegmentParser
// contract (see export.go): for every path shape, the one-shot segment
// view must derive exactly the same level and per-level names as the
// per-call GetLevel / GetNameByLevel. The engine's single-parse descent
// relies on this equivalence.
func TestParseSegmentsAgreesWithPerCallLookups(t *testing.T) {
	paths := []string{
		"",
		" ",
		"/",
		"//",
		"a",
		"/a",
		"a/",
		"/a/",
		" /a/b/ ",
		"a/b",
		"/a/b/c/d/e",
		"/a//b",   // interior empty segment (anonymous node, audit F9)
		"a//b//c", // multiple interior empties
		"/a/ /b",  // interior space is a real segment
		"/a/b//",
		"//a//b",
		"a/b/c",
		"A/B",
	}

	levels := []int{0, 1, 2, 3, 4, 5}

	for _, delim := range []string{"/", ".", ":"} {
		p := new(DelimiterPathParser).WithDelimiter(delim)
		for _, path := range paths {
			segs, ok := p.ParseSegments(path)
			if !ok {
				t.Fatalf("delimiter %q path %q: ParseSegments reported unsupported", delim, path)
			}
			if got, want := len(segs), p.GetLevel(path); got != want {
				t.Errorf("delimiter %q path %q: len(segments)=%d, GetLevel=%d", delim, path, got, want)
			}
			for _, level := range levels {
				name := ""
				if level >= 1 && level <= len(segs) {
					name = segs[level-1]
				}
				// GetNameByLevel panics for level 0 (audit M4); skip that
				// combination — the engine never asks for level 0.
				if level == 0 {
					continue
				}
				if got, want := name, p.GetNameByLevel(path, level); got != want {
					t.Errorf("delimiter %q path %q level %d: segment=%q, GetNameByLevel=%q", delim, path, level, got, want)
				}
			}
		}
	}
}

// TestParseSegmentsInteriorShape verifies the documented interior
// semantics: empty interior segments survive verbatim.
func TestParseSegmentsInteriorShape(t *testing.T) {
	segs, ok := SlashPathParser.ParseSegments("/a//b")
	if !ok {
		t.Fatal("unexpectedly unsupported")
	}
	if got, want := strings.Join(segs, "|"), "a||b"; got != want {
		t.Errorf("segments = %q, want %q", got, want)
	}

	if segs, ok := SlashPathParser.ParseSegments("  "); !ok || len(segs) != 0 {
		t.Errorf("whitespace-only path: segs=%v ok=%v, want empty/true", segs, ok)
	}
}

// TestSegmentParserDriverForwarding ensures every bundled driver exposes
// the optional SegmentParser API (forwarding to its embedded parser), so
// the engine's single-parse descent benefits all of them, and that a
// driver composed with a plain PathParser (no SegmentParser) reports
// ok=false instead of lying with an empty slice.
func TestSegmentParserDriverForwarding(t *testing.T) {
	drivers := map[string]Driver{
		"json":  NewJSONDriver(),
		"yaml":  NewYAMLDriver(),
		"xml":   NewXMLDriver(),
		"toml":  NewTOMLDriver(),
		"tile":  NewTileDriver(),
		"dummy": NewDummyDriver(),
	}

	for name, d := range drivers {
		sp, ok := d.(SegmentParser)
		if !ok {
			t.Errorf("%s driver does not implement SegmentParser", name)
			continue
		}
		segs, supported := sp.ParseSegments("/a/b/c")
		if !supported || len(segs) != 3 || segs[2] != "c" {
			t.Errorf("%s driver ParseSegments = %v, %v; want [a b c], true", name, segs, supported)
		}
	}

	// a product driver whose embedded parser has no SegmentParser must
	// decline (ok=false) instead of lying with an empty slice — the
	// engine then falls back to the per-call lookups
	jd := NewJSONDriver()
	jd.PathParser = plainPathParser{}
	var custom Driver = jd
	sp, ok := custom.(SegmentParser)
	if !ok {
		t.Fatal("JSONDriver lost SegmentParser with custom inner parser")
	}
	if segs, supported := sp.ParseSegments("/a/b"); supported || segs != nil {
		t.Errorf("driver with plain inner parser: ParseSegments = %v, %v; want nil, false", segs, supported)
	}
}

// plainPathParser is a minimal PathParser with no SegmentParser support.
type plainPathParser struct{}

func (plainPathParser) GetLevel(path string) int {
	if path == "" {
		return 0
	}
	return strings.Count(path, "/")
}

func (plainPathParser) GetNameByLevel(path string, level int) string {
	parts := strings.Split(path, "/")
	if level < 1 || level > len(parts) {
		return ""
	}
	return parts[level-1]
}

func (plainPathParser) AppendPath(path, name string) string { return path + "/" + name }
