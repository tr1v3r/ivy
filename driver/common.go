package driver

import (
	"fmt"
	"strings"
)

var _ PathParser = (*DelimiterPathParser)(nil)
var _ SegmentParser = (*DelimiterPathParser)(nil)

// SlashPathParser slash path parser
// equal to DelimiterPathParser{delimiter: "/"}
var SlashPathParser = new(DelimiterPathParser).WithDelimiter("/")

// DelimiterPathParser delimiter path parser
type DelimiterPathParser struct {
	delimiter string
}

// WithDelimiter returns a copy of the parser using the given delimiter.
func (d DelimiterPathParser) WithDelimiter(delimiter string) *DelimiterPathParser {
	d.delimiter = delimiter
	return &d
}

// GetLevel returns the number of path segments; empty paths are level 0.
func (d *DelimiterPathParser) GetLevel(path string) int {
	if path = strings.Trim(strings.TrimSpace(path), d.delimiter); path != "" {
		return len(strings.Split(path, d.delimiter))
	}
	return 0
}

// GetNameByLevel returns the segment at the given 1-based level, or "" when out of range.
func (d *DelimiterPathParser) GetNameByLevel(path string, level int) string {
	if path = strings.Trim(strings.TrimSpace(path), d.delimiter); path != "" {
		return d.getName(strings.Split(path, d.delimiter), level-1)
	}
	return ""
}

// ParseSegments splits path into its segments in one pass, exactly as the
// per-call lookups see them: surrounding whitespace and delimiter runs at
// both ends are trimmed (Trim cutset semantics — for multi-character
// delimiters the whole delimiter acts as a cutset, matching GetLevel),
// while interior segments are returned verbatim, including empty ones
// ("/a//b" → ["a", "", "b"]).
//
// The returned slice agrees with GetLevel/GetNameByLevel by construction
// (both derive from the same trim+split), so callers may parse once per
// query and index the slice per tree level. ok is always true for
// delimiter parsers.
func (d *DelimiterPathParser) ParseSegments(path string) ([]string, bool) {
	if path = strings.Trim(strings.TrimSpace(path), d.delimiter); path != "" {
		return strings.Split(path, d.delimiter), true
	}
	return nil, true
}

// AppendPath joins a name onto a path with the parser delimiter.
func (d *DelimiterPathParser) AppendPath(path, name string) string { return path + d.delimiter + name }
func (d *DelimiterPathParser) getName(paths []string, index int) string {
	if index >= len(paths) {
		return ""
	}
	return paths[index]
}

var _ Realizer = (*StdRealizer)(nil)

// StdRealizer standard rule driver
type StdRealizer struct{}

// Realize applies the processor chain to the rule content sequentially.
func (r *StdRealizer) Realize(rc *RealizeContext, rule []byte, procs ...Processor) ([]byte, error) {
	var err error
	for _, proc := range procs {
		if proc == nil {
			continue
		}
		if rule, err = proc.Process(rc, rule); err != nil {
			return nil, fmt.Errorf("do %s on %s fail: %w", proc.Type(), proc.Path(), err)
		}
	}
	return rule, nil
}
